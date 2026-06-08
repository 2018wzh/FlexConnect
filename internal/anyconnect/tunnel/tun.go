package vpn

import (
	"context"
	"net"
	"net/netip"
	"runtime"
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

func setupTun(cSess *session.ConnSession) error {
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
		return err
	}
	base.Info("tun interface created", "name", cSess.TunName, "mtu", cSess.MTU)
	if runtime.GOOS == "darwin" {
		cSess.TunName, _ = dev.Name()
	}

	base.Info("tun configured", "iface", cSess.TunName)

	manager, err := osnet.NewManager(dev, cSess.TunName)
	if err != nil {
		_ = dev.Close()
		return err
	}
	if err = waitManagerUp(context.Background(), manager, 30*time.Second); err != nil {
		_ = manager.Close(context.Background())
		_ = dev.Close()
		return err
	}
	if name, err := dev.Name(); err == nil && name != "" {
		cSess.TunName = name
	}
	cSess.NetworkManager = manager

	go tunToPayloadOut(dev, cSess) // read from apps
	go payloadInToTun(dev, cSess)  // write to apps
	return nil
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
		_ = dev.Close()
	}()
	var (
		err error
		n   int
	)

	sent := 0
	for {
		// 从池子申请一块内存，存放到 PayloadOutTLS 或 PayloadOutDTLS，在 payloadOutTLSToServer 或 payloadOutDTLSToServer 中释放
		// 由 payloadOutTLSToServer 或 payloadOutDTLSToServer 添加 header 后发送出去
		pl := getPayloadBuffer()
		bufs := [][]byte{pl.Data}
		sizes := []int{0}
		_, err = dev.Read(bufs, sizes, offset) // 如果 tun 没有 up，会在这等待
		n = sizes[0]
		if err != nil {
			base.Error("tun to payloadOut error:", err)
			return
		}
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

		dSess := cSess.DSess
		if cSess.DtlsConnected.Load() {
			select {
			case cSess.PayloadOutDTLS <- pl:
			case <-dSess.CloseChan:
			}
		} else {
			select {
			case cSess.PayloadOutTLS <- pl:
			case <-cSess.CloseChan:
				return
			}
		}
	}
}

// Step 22
// 读取 tlsChannel、dtlsChannel 放入 cSess.PayloadIn 的数据包（由服务端返回，已调整格式），写入 tun，网络栈交给应用
func payloadInToTun(dev wgtun.Device, cSess *session.ConnSession) {
	// tun 设备写错误或者cSess.CloseChan
	defer func() {
		base.Info("payloadIn to tun exit")
		if !cSess.Sess.ActiveClose {
			if cSess.NetworkManager != nil {
				_ = cSess.NetworkManager.Close(context.Background())
			}
		}
		// 可能由写错误触发，和 tunToPayloadOut 一起，只要有一处确保退出 cSess 即可，否则 tls 不会退出
		// 如果由外部触发，cSess.Close() 因为使用 sync.Once，所以没影响
		cSess.Close()
		_ = dev.Close()
	}()

	var (
		err error
		pl  *proto.Payload
	)

	received := 0
	for {
		select {
		case pl = <-cSess.PayloadIn:
		case <-cSess.CloseChan:
			return
		}

		// 只有当使用域名分流且返回数据包为 DNS 时才进一步分析，少建几个协程
		if cSess.DynamicSplitTunneling {
			_, srcPort, _, _ := utils.ResolvePacket(pl.Data)
			if srcPort == 53 {
				go dynamicSplitRoutes(pl.Data, cSess)
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
			_, err = dev.Write([][]byte{expand}, offset)
		} else {
			_, err = dev.Write([][]byte{pl.Data}, offset)
		}

		if received < 3 {
			base.Debug("payloadIn to tun", "size", len(pl.Data))
		}
		received++
		if err != nil {
			base.Error("payloadIn to tun error:", err)
			return
		}

		// 释放由 serverToPayloadIn 申请的内存
		putPayloadBuffer(pl)
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
	if info, err := getLocalInterface(context.Background()); err == nil {
		cfg.GatewayInterfaceIndex = info.InterfaceIndex
		if info.Gateway != "" {
			if addr, err := netip.ParseAddr(info.Gateway); err == nil {
				cfg.Gateway = addr.Unmap()
			}
		}
	}
	if addr, err := netip.ParseAddr(cSess.ServerAddress); err == nil {
		cfg.ServerAddress = addr.Unmap()
	}
	if !cfg.Gateway.IsValid() {
		if addr, err := netip.ParseAddr(base.LocalInterface.Gateway); err == nil {
			cfg.Gateway = addr.Unmap()
		}
	}
	if cfg.GatewayInterfaceIndex == 0 {
		iface, err := net.InterfaceByName(base.LocalInterface.Name)
		if err == nil {
			cfg.GatewayInterfaceIndex = iface.Index
		}
	}
	return cfg, nil
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
