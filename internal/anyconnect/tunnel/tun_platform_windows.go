//go:build windows

package vpn

import wgtun "github.com/tailscale/wireguard-go/tun"

func setPlatformTunnelType() {
	wgtun.WintunTunnelType = "FlexConnect"
}
