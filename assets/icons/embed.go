package icons

import _ "embed"

var (
	//go:embed app.svg
	appSVG string

	//go:embed logo.svg
	logoSVG string

	//go:embed favicon.ico
	faviconICO []byte

	//go:embed favicon-32.png
	favicon32PNG []byte

	//go:embed tray.ico
	trayICO []byte

	//go:embed tray-blue.ico
	trayBlueICO []byte

	//go:embed tray-blue.png
	trayBluePNG []byte

	//go:embed tray-red.ico
	trayRedICO []byte

	//go:embed tray-red.png
	trayRedPNG []byte

	//go:embed tray-green.ico
	trayGreenICO []byte

	//go:embed tray-green.png
	trayGreenPNG []byte
)

func AppSVG() string {
	return appSVG
}

func LogoSVG() string {
	return logoSVG
}

func FaviconICO() []byte {
	return faviconICO
}

func Favicon32PNG() []byte {
	return favicon32PNG
}

func TrayICO() []byte {
	return trayICO
}

func TrayBlueICO() []byte {
	return trayBlueICO
}

func TrayBluePNG() []byte {
	return trayBluePNG
}

func TrayRedICO() []byte {
	return trayRedICO
}

func TrayRedPNG() []byte {
	return trayRedPNG
}

func TrayGreenICO() []byte {
	return trayGreenICO
}

func TrayGreenPNG() []byte {
	return trayGreenPNG
}
