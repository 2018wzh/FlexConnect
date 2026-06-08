package vpnc

import (
	"encoding/json"
	"fmt"
	"net"
	"net/netip"
	"os/exec"
	"strings"

	"flexconnect/internal/anyconnect/base"
	"flexconnect/internal/anyconnect/session"
	"flexconnect/internal/anyconnect/tun"
	"flexconnect/internal/anyconnect/utils"
)

var (
	localInterfaceIndex int
	ifaceIndex          int
	nextHopVPN          string
	nextHopGateway      string
)

type routeInfo struct {
	InterfaceIndex int    `json:"InterfaceIndex"`
	NextHop        string `json:"NextHop"`
	IPAddress      string `json:"IPAddress"`
	InterfaceAlias string `json:"InterfaceAlias"`
}

func ConfigInterface(cSess *session.ConnSession) error {
	mtu, _ := tun.NativeTunDevice.MTU()
	if err := SetMTU(cSess.TunName, mtu); err != nil {
		return err
	}

	iface, err := net.InterfaceByName(cSess.TunName)
	if err != nil {
		return err
	}
	ifaceIndex = iface.Index
	nextHopVPN = cSess.VPNAddress

	cmds := []string{
		fmt.Sprintf(`netsh interface ipv4 set address name="%s" static %s %s`, cSess.TunName, cSess.VPNAddress, cSess.VPNMask),
	}
	return execCmd(cmds)
}

func SetRoutes(cSess *session.ConnSession) error {
	if cSess.ServerAddress != "" && nextHopGateway != "" {
		if err := addRoute(cSess.ServerAddress, "255.255.255.255", nextHopGateway, 5, localInterfaceIndex); err != nil && !routeExists(err) {
			return routingError(cSess.ServerAddress+"/32", err)
		}
	}

	if cSess.UseDefaultRouteWhenEmpty {
		cSess.SplitInclude = append(cSess.SplitInclude, "0.0.0.0/0.0.0.0")
	}
	for _, ipMask := range cSess.SplitInclude {
		ip, mask := splitIPMask(ipMask)
		if err := addRoute(ip, mask, nextHopVPN, 6, ifaceIndex); err != nil && !routeExists(err) {
			return routingError(utils.IpMaskToCIDR(ipMask), err)
		}
	}

	for _, ipMask := range cSess.SplitExclude {
		ip, mask := splitIPMask(ipMask)
		if err := addRoute(ip, mask, nextHopGateway, 5, localInterfaceIndex); err != nil && !routeExists(err) {
			return routingError(utils.IpMaskToCIDR(ipMask), err)
		}
	}

	if len(cSess.DNS) > 0 {
		if err := setDNS(cSess); err != nil {
			return err
		}
	}
	return nil
}

func ResetRoutes(cSess *session.ConnSession) {
	if cSess.ServerAddress != "" && nextHopGateway != "" {
		_ = deleteRoute(cSess.ServerAddress, "255.255.255.255", nextHopGateway)
	}
	for _, ipMask := range cSess.SplitExclude {
		ip, mask := splitIPMask(ipMask)
		_ = deleteRoute(ip, mask, nextHopGateway)
	}
	if len(cSess.DynamicSplitExcludeDomains) > 0 {
		cSess.DynamicSplitExcludeResolved.Range(func(_, value any) bool {
			for _, ip := range value.([]string) {
				_ = deleteRoute(ip, "255.255.255.255", nextHopGateway)
			}
			return true
		})
	}
}

func DynamicAddIncludeRoutes(ips []string) {
	for _, ip := range ips {
		_ = addRoute(ip, "255.255.255.255", nextHopVPN, 6, ifaceIndex)
	}
}

func DynamicAddExcludeRoutes(ips []string) {
	for _, ip := range ips {
		_ = addRoute(ip, "255.255.255.255", nextHopGateway, 5, localInterfaceIndex)
	}
}

func GetLocalInterface() error {
	info, err := getPrimaryRouteInfo()
	if err != nil {
		return err
	}
	base.Info("GetLocalInterface:", info.InterfaceAlias, info.IPAddress, info.NextHop, info.InterfaceIndex)
	base.LocalInterface.Name = info.InterfaceAlias
	base.LocalInterface.Ip4 = info.IPAddress
	base.LocalInterface.Gateway = info.NextHop
	nextHopGateway = info.NextHop
	localInterfaceIndex = info.InterfaceIndex

	if iface, err := net.InterfaceByIndex(info.InterfaceIndex); err == nil {
		base.LocalInterface.Mac = iface.HardwareAddr.String()
	}
	return nil
}

func SetMTU(ifname string, mtu int) error {
	cmdStr := fmt.Sprintf(`netsh interface ipv4 set subinterface "%s" mtu=%d`, ifname, mtu)
	return execCmd([]string{cmdStr})
}

func routingError(dst string, err error) error {
	return fmt.Errorf("routing error: %s %s", dst, err)
}

func execCmd(cmdStrs []string) error {
	for _, cmdStr := range cmdStrs {
		cmd := exec.Command("cmd", "/C", cmdStr)
		cmd.Env = []string{"Path=C:\\WINDOWS\\system32;C:\\WINDOWS"}
		stdoutStderr, err := cmd.CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s %s %s", err, cmd.String(), string(stdoutStderr))
		}
	}
	return nil
}

func setDNS(cSess *session.ConnSession) error {
	if len(cSess.DynamicSplitIncludeDomains) > 0 {
		DynamicAddIncludeRoutes(cSess.DNS)
	}
	if len(cSess.DNS) == 0 {
		return nil
	}
	cmds := []string{
		fmt.Sprintf(`netsh interface ipv4 set dnsservers name="%s" static %s primary`, cSess.TunName, cSess.DNS[0]),
	}
	for i := 1; i < len(cSess.DNS); i++ {
		cmds = append(cmds, fmt.Sprintf(`netsh interface ipv4 add dnsservers name="%s" %s index=%d`, cSess.TunName, cSess.DNS[i], i+1))
	}
	return execCmd(cmds)
}

func splitIPMask(ipMask string) (string, string) {
	parts := strings.Split(ipMask, "/")
	if len(parts) != 2 {
		return ipMask, "255.255.255.255"
	}
	return parts[0], parts[1]
}

func addRoute(ip, mask, gateway string, metric, index int) error {
	cmdStr := fmt.Sprintf(`route ADD %s MASK %s %s METRIC %d IF %d`, ip, mask, gateway, metric, index)
	return execCmd([]string{cmdStr})
}

func deleteRoute(ip, mask, gateway string) error {
	cmdStr := fmt.Sprintf(`route DELETE %s MASK %s %s`, ip, mask, gateway)
	return execCmd([]string{cmdStr})
}

func routeExists(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "object already exists") ||
		strings.Contains(strings.ToLower(err.Error()), "the route addition failed")
}

func getPrimaryRouteInfo() (routeInfo, error) {
	cmd := exec.Command("powershell", "-NoProfile", "-Command", `
$route = Get-NetRoute -AddressFamily IPv4 -DestinationPrefix "0.0.0.0/0" |
  Sort-Object RouteMetric, InterfaceMetric |
  Select-Object -First 1 InterfaceIndex, NextHop;
$cfg = Get-NetIPConfiguration -InterfaceIndex $route.InterfaceIndex;
[pscustomobject]@{
  InterfaceIndex = $route.InterfaceIndex
  NextHop = $route.NextHop
  IPAddress = $cfg.IPv4Address.IPAddress
  InterfaceAlias = $cfg.InterfaceAlias
} | ConvertTo-Json -Compress`)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return routeInfo{}, fmt.Errorf("%w: %s", err, string(out))
	}
	var info routeInfo
	if err := json.Unmarshal(out, &info); err != nil {
		return routeInfo{}, err
	}
	if info.InterfaceIndex == 0 || info.IPAddress == "" || info.NextHop == "" {
		return routeInfo{}, fmt.Errorf("incomplete route info: %s", string(out))
	}
	if _, err := netip.ParseAddr(info.NextHop); err != nil {
		return routeInfo{}, err
	}
	return info, nil
}

