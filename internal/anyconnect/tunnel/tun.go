package vpn

import (
	"context"
	"fmt"
	"io"
	"net/netip"
	"runtime"
	"strings"
	"time"

	"flexconnect/internal/anyconnect/base"
	"flexconnect/internal/anyconnect/proto"
	"flexconnect/internal/anyconnect/session"
	"flexconnect/internal/anyconnect/utils"
	"flexconnect/internal/osnet"

	"github.com/gopacket/gopacket"
	"github.com/gopacket/gopacket/layers"
	wgtun "github.com/tailscale/wireguard-go/tun"
)

var offset = 0 // reserve space for header

var getLocalInterface = osnet.GetLocalInterface

func setupTun(cSess *session.ConnSession) (wgtun.Device, error) {
	if runtime.GOOS == "windows" {
		cSess.TunName = "FlexConnect"
		setPlatformTunnelType()
	} else if runtime.GOOS == "darwin" {
		cSess.TunName = "utun"
		offset = 4
	} else {
		cSess.TunName = "flexconnect"
	}
	dev, err := wgtun.CreateTUN(cSess.TunName, cSess.MTU)
	if err != nil {
		base.Error("failed to creates a new tun interface")
		return nil, err
	}
	base.Info("tun interface created", "name", cSess.TunName, "mtu", cSess.MTU)
	if runtime.GOOS == "darwin" {
		cSess.TunName, _ = dev.Name()
	}

	base.Info("tun configured", "iface", cSess.TunName)

	manager, err := osnet.NewManager(dev, cSess.TunName)
	if err != nil {
		_ = dev.Close()
		return nil, err
	}
	if err = waitManagerUp(context.Background(), manager, 30*time.Second); err != nil {
		_ = manager.Close(context.Background())
		_ = dev.Close()
		return nil, err
	}
	if name, err := dev.Name(); err == nil && name != "" {
		cSess.TunName = name
	}
	cSess.NetworkManager = manager
	return dev, nil
}

// startTun begins packet I/O only after the operating-system address and
// routes have been installed. This mirrors the Tailscale TUN lifecycle: the
// device can exist while it is being configured, but no packet worker owns it
// until the configuration is known to be usable.
func startTun(dev wgtun.Device, cSess *session.ConnSession) (*TunnelController, error) {
	base.Info("start tun packet workers", "iface", cSess.TunName)
	controller, err := newTunnelController(dev, cSess.NetworkManager, cSess)
	if err != nil {
		return nil, err
	}
	if err := controller.Start(); err != nil {
		return nil, err
	}
	return controller, nil
}

// watchTunEvents turns a native-device failure into a session failure. The
// wireguard-go Device contract exposes Events for exactly this lifecycle
// boundary; ignoring it leaves the CSTP workers alive after the interface is
// down and makes the next failure look like a remote disconnect.
func watchTunEvents(dev wgtun.Device, cSess *session.ConnSession) {
	for {
		select {
		case <-cSess.CloseChan:
			return
		case event, ok := <-dev.Events():
			if !ok {
				select {
				case <-cSess.CloseChan:
					return
				default:
				}
				cSess.RecordClose("tun_events_closed", "tun", io.EOF)
				cSess.Close()
				return
			}
			base.Debug("tun event", "event", event)
			if event&wgtun.EventDown != 0 {
				err := fmt.Errorf("TUN interface reported down")
				cSess.RecordClose("tun_down", "tun", err)
				cSess.Close()
				return
			}
		}
	}
}

func waitManagerUp(ctx context.Context, manager osnet.Manager, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	var lastErr error
	for {
		if err := manager.Up(ctx); err == nil {
			return nil
		} else {
			lastErr = err
		}
		select {
		case <-ctx.Done():
			if lastErr != nil {
				return lastErr
			}
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// Step 3
// 网络栈将应用数据包转给 tun 后，该函数从 tun 读取数据包，放入 cSess.PayloadOutTLS 或 cSess.PayloadOutDTLS
// 之后由 payloadOutTLSToServer 或 payloadOutDTLSToServer 调整格式，发送给服务端
func tunToPayloadOut(dev wgtun.Device, cSess *session.ConnSession) {
	// tun 设备读错误
	defer func() {
		base.Info("tun to payloadOut exit")
	}()

	sent := 0
	for {
		// 从池子申请一块内存，存放到 PayloadOutTLS 或 PayloadOutDTLS，在 payloadOutTLSToServer 或 payloadOutDTLSToServer 中释放
		// 由 payloadOutTLSToServer 或 payloadOutDTLSToServer 添加 header 后发送出去
		pl := getPayloadBuffer()
		bufs := [][]byte{pl.Data}
		sizes := []int{0}
		readCount, err := dev.Read(bufs, sizes, offset) // 如果 tun 没有 up，会在这等待
		if err != nil {
			cSess.Stat.TUNReadErrors.Inc()
			putPayloadBuffer(pl)
			cSess.RecordClose("tun_read_error", "tun", err)
			base.Error("tun to payloadOut error:", err)
			cSess.Close()
			return
		}
		if readCount == 0 {
			putPayloadBuffer(pl)
			continue
		}
		cSess.Stat.TUNReads.Inc()
		if readCount != 1 || sizes[0] <= 0 || sizes[0] > len(bufs[0])-offset {
			err := fmt.Errorf("TUN read returned count=%d size=%d", readCount, sizes[0])
			putPayloadBuffer(pl)
			cSess.RecordClose("tun_read_invalid", "tun", err)
			base.Error("tun to payloadOut invalid read:", err)
			cSess.Close()
			return
		}
		n := sizes[0]
		if sent < 3 {
			base.Debug("tun to payloadOut", "size", n, "useDTLS", cSess.DtlsConnected.Load())
		}
		sent++

		// 更新数据长度
		pl.Data = (pl.Data)[offset : offset+n]

		// base.Debug("tunToPayloadOut")
		// if base.Cfg.LogLevel == "Debug" {
		//     src, srcPort, dst, dstPort := utils.ResolvePacket(pl.Data)
		//     if dst == "8.8.8.8" {
		//         base.Debug("client from", src, srcPort, "request target", dst, dstPort)
		//     }
		// }

		if !sendPayloadToServer(cSess, pl) {
			putPayloadBuffer(pl)
			return
		}
	}
}

// Step 22
// 读取 tlsChannel、dtlsChannel 放入 cSess.PayloadIn 的数据包（由服务端返回，已调整格式），写入 tun，网络栈交给应用
func payloadInToTun(dev wgtun.Device, cSess *session.ConnSession) {
	// tun 设备写错误或者cSess.CloseChan
	defer func() {
		base.Info("payloadIn to tun exit")
		closeUserTunnel(cSess)
		// 可能由写错误触发，和 tunToPayloadOut 一起，只要有一处确保退出 cSess 即可，否则 tls 不会退出
		// 如果由外部触发，cSess.Close() 因为使用 sync.Once，所以没影响
		cSess.Close()
	}()

	var (
		err        error
		pl         *proto.Payload
		writeCount int
	)

	received := 0
	for {
		select {
		case payload, ok := <-cSess.PayloadIn:
			if !ok {
				return
			}
			pl = payload
		case <-cSess.CloseChan:
			return
		}
		if pl == nil {
			err := fmt.Errorf("nil payload received from CSTP worker")
			cSess.RecordClose("payload_in_invalid", "tun", err)
			base.Error("payloadIn to tun invalid payload:", err)
			return
		}

		if routeUserInbound(cSess, pl.Data) {
			putPayloadBuffer(pl)
			continue
		}

		// 只有当使用域名分流且返回数据包为 DNS 时才进一步分析，少建几个协程
		if cSess.DynamicSplitTunneling {
			_, srcPort, _, _ := utils.ResolvePacket(pl.Data)
			if srcPort == 53 {
				// The payload buffer returns to a sync.Pool immediately after the
				// TUN write. Give the asynchronous DNS parser an owned snapshot so
				// a later packet cannot overwrite the bytes it is still parsing.
				data := append([]byte(nil), pl.Data...)
				go dynamicSplitRoutes(data, cSess)
			}
		}
		// base.Debug("payloadInToTun")
		// if base.Cfg.LogLevel == "Debug" {
		//     src, srcPort, dst, dstPort := utils.ResolvePacket(pl.Data)
		//     if src == "8.8.8.8" {
		//         base.Debug("target from", src, srcPort, "response to client", dst, dstPort)
		//     }
		// }

		if offset > 0 {
			expand := make([]byte, offset+len(pl.Data))
			copy(expand[offset:], pl.Data)
			writeCount, err = dev.Write([][]byte{expand}, offset)
		} else {
			writeCount, err = dev.Write([][]byte{pl.Data}, offset)
		}

		if received < 3 {
			base.Debug("payloadIn to tun", "size", len(pl.Data))
		}
		received++
		if err == nil && writeCount != 1 {
			err = fmt.Errorf("TUN write returned count=%d", writeCount)
		}
		if err != nil {
			cSess.Stat.TUNWriteErrors.Inc()
		} else {
			cSess.Stat.TUNWrites.Inc()
		}
		// 释放由 serverToPayloadIn 申请的内存 before handling either success
		// or failure; the failure path otherwise leaks a pooled buffer.
		putPayloadBuffer(pl)
		if err != nil {
			cSess.RecordClose("tun_write_error", "tun", err)
			base.Error("payloadIn to tun error:", err)
			return
		}
	}
}

func dynamicSplitRoutes(data []byte, cSess *session.ConnSession) {
	packet := gopacket.NewPacket(data, layers.LayerTypeIPv4, gopacket.Default)
	dnsLayer := packet.Layer(layers.LayerTypeDNS)
	if dnsLayer != nil {
		dns, _ := dnsLayer.(*layers.DNS)

		query := string(dns.Questions[0].Name)
		// base.Debug("Query:", query)

		if utils.InArrayGeneric(cSess.DynamicSplitIncludeDomains, query) {
			// 分析流量后才知道请求的域名，即使已经设置路由，仍然需要分析流量，不可避免的 overhead
			if _, ok := cSess.DynamicSplitIncludeResolved.Load(query); !ok && dns.ANCount > 0 {
				var answers []string
				for _, v := range dns.Answers {
					// log.Printf("DNS Answer: %+v", v)
					if v.Type == layers.DNSTypeA {
						// fmt.Println("Name:", string(v.Name)) // cname, canonical name
						// base.Debug("Address:", v.IP.String())
						answers = append(answers, v.IP.String())
					}
				}
				if len(answers) > 0 {
					cSess.DynamicSplitIncludeResolved.Store(query, answers)
					if cSess.NetworkManager != nil {
						_ = cSess.NetworkManager.SetDynamicRoutes(context.Background(), collectDynamicRoutes(cSess))
					}
				}
			}
		} else if utils.InArrayGeneric(cSess.DynamicSplitExcludeDomains, query) {
			if _, ok := cSess.DynamicSplitExcludeResolved.Load(query); !ok && dns.ANCount > 0 {
				var answers []string
				for _, v := range dns.Answers {
					// log.Printf("DNS Answer: %+v", v)
					if v.Type == layers.DNSTypeA {
						// fmt.Println("Name:", string(v.Name)) // cname, canonical name
						// base.Debug("Address:", v.IP.String())
						answers = append(answers, v.IP.String())
					}
				}
				if len(answers) > 0 {
					cSess.DynamicSplitExcludeResolved.Store(query, answers)
					if cSess.NetworkManager != nil {
						_ = cSess.NetworkManager.SetDynamicRoutes(context.Background(), collectDynamicRoutes(cSess))
					}
				}
			}
		}
	}
}

func buildOSNetConfig(cSess *session.ConnSession) (*osnet.Config, error) {
	vpnPrefix, err := osnet.PrefixFromIPMask(cSess.VPNAddress, cSess.VPNMask)
	if err != nil {
		return nil, err
	}
	serverAddress, err := parseServerAddress(cSess.ServerAddress)
	if err != nil {
		return nil, err
	}
	include, err := osnet.ParsePrefixes(cSess.SplitInclude)
	if err != nil {
		return nil, err
	}
	exclude, err := osnet.ParsePrefixes(cSess.SplitExclude)
	if err != nil {
		return nil, err
	}
	if cSess.UseDefaultRouteWhenEmpty && len(include) == 0 {
		include = append(include, netip.PrefixFrom(netip.IPv4Unspecified(), 0))
	}
	if serverAddress.IsValid() {
		exclude = appendUniquePrefix(exclude, netip.PrefixFrom(serverAddress, serverAddress.BitLen()))
		base.Debug("protect vpn server route", "serverAddress", serverAddress.String())
	}
	dns, err := osnet.ParseAddrs(cSess.DNS)
	if err != nil {
		return nil, err
	}
	cfg := &osnet.Config{
		InterfaceName: cSess.TunName,
		VPNAddress:    vpnPrefix,
		MTU:           cSess.MTU,
		IncludeRoutes: append([]netip.Prefix(nil), include...),
		ExcludeRoutes: append([]netip.Prefix(nil), exclude...),
		DNSServers:    dns,
	}
	if cSess.Underlay.LocalIPv4.IsValid() {
		cfg.Underlay = cSess.Underlay
		cfg.GatewayInterfaceIndex = cSess.Underlay.GatewayInterface
		cfg.Gateway = cSess.Underlay.Gateway
	} else {
		info, err := getLocalInterface(context.Background())
		if err != nil {
			return nil, fmt.Errorf("resolve physical underlay: %w", err)
		}
		cfg.GatewayInterfaceIndex = info.InterfaceIndex
		if info.Gateway != "" {
			if addr, err := netip.ParseAddr(info.Gateway); err == nil {
				cfg.Gateway = addr.Unmap()
			}
		}
	}
	cfg.ServerAddress = serverAddress
	return cfg, nil
}

func parseServerAddress(raw string) (netip.Addr, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return netip.Addr{}, nil
	}
	addr, err := netip.ParseAddr(raw)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("invalid VPN server address %q: %w", raw, err)
	}
	return addr.Unmap(), nil
}

func appendUniquePrefix(prefixes []netip.Prefix, prefix netip.Prefix) []netip.Prefix {
	prefix = prefix.Masked()
	for _, existing := range prefixes {
		if existing.Masked() == prefix {
			return prefixes
		}
	}
	return append(prefixes, prefix)
}

func collectDynamicRoutes(cSess *session.ConnSession) osnet.DynamicRoutes {
	var routes osnet.DynamicRoutes
	cSess.DynamicSplitIncludeResolved.Range(func(_, value any) bool {
		routes.Include = append(routes.Include, parseDynamicAddrs(value)...)
		return true
	})
	cSess.DynamicSplitExcludeResolved.Range(func(_, value any) bool {
		routes.Exclude = append(routes.Exclude, parseDynamicAddrs(value)...)
		return true
	})
	return routes
}

func parseDynamicAddrs(value any) []netip.Addr {
	raw, ok := value.([]string)
	if !ok {
		return nil
	}
	addrs, _ := osnet.ParseAddrs(raw)
	return addrs
}
