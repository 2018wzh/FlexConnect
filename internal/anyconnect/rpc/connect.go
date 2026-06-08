package rpc

import (
	"context"
	"strings"

	"flexconnect/internal/anyconnect/auth"
	"flexconnect/internal/anyconnect/base"
	"flexconnect/internal/anyconnect/session"
	acvpn "flexconnect/internal/anyconnect/tunnel"
	"flexconnect/internal/osnet"
)

// Connect 调用之前必须由前端填充 auth.Prof，建议填充 base.Interface
func Connect() error {
	base.Info("ac rpc connect start")
	err := prepareConnection()
	if err != nil {
		base.Error("prepare connection failed:", err)
		return err
	}
	err = auth.PasswordAuth()
	if err != nil {
		base.Error("password auth failed:", err)
		return err
	}
	base.Info("password auth done, setup tunnel")

	return SetupTunnel(false)
}

// SetupTunnel 操作系统长时间睡眠后再自动连接会失败，仅用于短时间断线自动重连
func SetupTunnel(reconnect bool) error {
	// 为适应复杂网络环境，必须能够感知网卡变化，建议由前端获取当前网络信息发送过来，而不是登陆前由 Go 处理
	// 断网重连时网卡信息可能已经变化，所以建立隧道时重新获取网卡信息
	if reconnect && !auth.Prof.Initialized {
		err := refreshLocalInterface()
		if err != nil {
			base.Error("reconnect get local interface failed:", err)
			return err
		}
	}
	base.Info("setup tunnel via rpc", "reconnect", reconnect)
	return acvpn.SetupTunnel()
}

func prepareConnection() error {
	if strings.Contains(auth.Prof.Host, ":") {
		auth.Prof.HostWithPort = auth.Prof.Host
	} else {
		auth.Prof.HostWithPort = auth.Prof.Host + ":443"
	}
	if !auth.Prof.Initialized {
		base.Info("prepare connection: fetch local interface")
		err := refreshLocalInterface()
		if err != nil {
			base.Error("prepare connection failed to get local interface:", err)
			return err
		}
	}
	base.Info("prepare connection completed", "host", auth.Prof.HostWithPort)
	return auth.InitAuth()
}

// DisConnect 主动断开或者 ctrl+c，不包括网络或tun异常退出
func DisConnect() {
	session.Sess.ActiveClose = true
	if auth.Conn != nil {
		_ = auth.Conn.Close()
		auth.Conn = nil
	}
	if session.Sess.CSess != nil {
		if session.Sess.CSess.NetworkManager != nil {
			_ = session.Sess.CSess.NetworkManager.Close(context.Background())
		}
		session.Sess.CSess.Close()
	}
}

func refreshLocalInterface() error {
	info, err := osnet.GetLocalInterface(context.Background())
	if err != nil {
		return err
	}
	base.LocalInterface.Name = info.Name
	base.LocalInterface.Ip4 = info.IP4
	base.LocalInterface.Mac = info.MAC
	base.LocalInterface.Gateway = info.Gateway
	return nil
}
