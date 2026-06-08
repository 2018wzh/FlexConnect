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
