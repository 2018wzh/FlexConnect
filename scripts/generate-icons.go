package main

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"math"
	"os"
	"path/filepath"

	"github.com/nfnt/resize"
	"github.com/sergeymakinen/go-ico"
)

const appSVGContent = `<svg width="512" height="512" viewBox="0 0 512 512" fill="none" xmlns="http://www.w3.org/2000/svg">
  <defs>
    <linearGradient id="fc-gradient" x1="0" y1="0" x2="512" y2="512" gradientUnits="userSpaceOnUse">
      <stop stop-color="#00F2FE"/>
      <stop offset="0.5" stop-color="#4FACFE"/>
      <stop offset="1" stop-color="#00FF87"/>
    </linearGradient>
  </defs>
  <g transform="translate(256 256)">
    <!-- Outer ring -->
    <circle cx="0" cy="0" r="200" stroke="url(#fc-gradient)" stroke-width="24" fill="none"/>
    
    <!-- Symmetrical spiral arms -->
    <g stroke="url(#fc-gradient)" stroke-width="20" stroke-linecap="round" stroke-linejoin="round" fill="none">
      <path d="M 0 0 L 0 -96 Q 120 -96 173.2 100" />
      <path d="M 0 0 L 0 -96 Q 120 -96 173.2 100" transform="rotate(120)" />
      <path d="M 0 0 L 0 -96 Q 120 -96 173.2 100" transform="rotate(240)" />
    </g>
    
    <!-- Nodes -->
    <g fill="url(#fc-gradient)" stroke="none">
      <circle cx="0" cy="-200" r="16" />
      <circle cx="0" cy="-200" r="16" transform="rotate(120)" />
      <circle cx="0" cy="-200" r="16" transform="rotate(240)" />
      <circle cx="0" cy="0" r="24" />
    </g>
  </g>
</svg>
`

const logoSVGContent = `<svg width="512" height="128" viewBox="0 0 512 128" fill="none" xmlns="http://www.w3.org/2000/svg">
  <defs>
    <linearGradient id="fc-gradient" x1="0" y1="0" x2="512" y2="128" gradientUnits="userSpaceOnUse">
      <stop stop-color="#00F2FE"/>
      <stop offset="0.5" stop-color="#4FACFE"/>
      <stop offset="1" stop-color="#00FF87"/>
    </linearGradient>
  </defs>
  <!-- Logo Icon on the left -->
  <g transform="translate(64 64) scale(0.24)">
    <circle cx="0" cy="0" r="200" stroke="url(#fc-gradient)" stroke-width="24" fill="none"/>
    <g stroke="url(#fc-gradient)" stroke-width="20" stroke-linecap="round" stroke-linejoin="round" fill="none">
      <path d="M 0 0 L 0 -96 Q 120 -96 173.2 100" />
      <path d="M 0 0 L 0 -96 Q 120 -96 173.2 100" transform="rotate(120)" />
      <path d="M 0 0 L 0 -96 Q 120 -96 173.2 100" transform="rotate(240)" />
    </g>
    <g fill="url(#fc-gradient)" stroke="none">
      <circle cx="0" cy="-200" r="16" />
      <circle cx="0" cy="-200" r="16" transform="rotate(120)" />
      <circle cx="0" cy="-200" r="16" transform="rotate(240)" />
      <circle cx="0" cy="0" r="24" />
    </g>
  </g>
  <!-- Logo Text on the right -->
  <text x="136" y="80" fill="url(#fc-gradient)" font-family="Inter, system-ui, -apple-system, sans-serif" font-size="48" font-weight="800" letter-spacing="-0.03em">FlexConnect</text>
</svg>
`

type Point struct {
	X, Y float64
}

type colorStops [3]color.NRGBA

var trayStatusGradients = map[string]colorStops{
	"blue": {
		{R: 0x00, G: 0xF2, B: 0xFE, A: 0xFF},
		{R: 0x4F, G: 0xAC, B: 0xFE, A: 0xFF},
		{R: 0x00, G: 0x60, B: 0xDC, A: 0xFF},
	},
	"red": {
		{R: 0xFF, G: 0x78, B: 0x78, A: 0xFF},
		{R: 0xF0, G: 0x00, B: 0x00, A: 0xFF},
		{R: 0xA0, G: 0x00, B: 0x00, A: 0xFF},
	},
	"green": {
		{R: 0x78, G: 0xFF, B: 0x78, A: 0xFF},
		{R: 0x00, G: 0xF0, B: 0x00, A: 0xFF},
		{R: 0x00, G: 0xA0, B: 0x00, A: 0xFF},
	},
}

func rotate(p Point, deg float64) Point {
	rad := deg * math.Pi / 180.0
	cos := math.Cos(rad)
	sin := math.Sin(rad)
	return Point{
		X: p.X*cos - p.Y*sin,
		Y: p.X*sin + p.Y*cos,
	}
}

func distance(p1, p2 Point) float64 {
	dx := p1.X - p2.X
	dy := p1.Y - p2.Y
	return math.Sqrt(dx*dx + dy*dy)
}

func gradientColor(stops colorStops, u float64) color.NRGBA {
	if u < 0 {
		u = 0
	}
	if u > 1 {
		u = 1
	}
	if u < 0.5 {
		return mixColor(stops[0], stops[1], u/0.5)
	}
	return mixColor(stops[1], stops[2], (u-0.5)/0.5)
}

func mixColor(a, b color.NRGBA, t float64) color.NRGBA {
	return color.NRGBA{
		R: uint8(float64(a.R) + (float64(b.R)-float64(a.R))*t),
		G: uint8(float64(a.G) + (float64(b.G)-float64(a.G))*t),
		B: uint8(float64(a.B) + (float64(b.B)-float64(a.B))*t),
		A: uint8(float64(a.A) + (float64(b.A)-float64(a.A))*t),
	}
}

func gradientIcon(src *image.NRGBA, stops colorStops) *image.NRGBA {
	dst := image.NewNRGBA(src.Bounds())
	bounds := src.Bounds()
	for y := src.Bounds().Min.Y; y < src.Bounds().Max.Y; y++ {
		for x := src.Bounds().Min.X; x < src.Bounds().Max.X; x++ {
			a := src.NRGBAAt(x, y).A
			if a == 0 {
				continue
			}
			u := (float64(x-bounds.Min.X) + 0.5 + float64(y-bounds.Min.Y) + 0.5) / float64(bounds.Dx()+bounds.Dy())
			c := gradientColor(stops, u)
			c.A = a
			dst.SetNRGBA(x, y, c)
		}
	}
	return dst
}

func writePNG(path string, img image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return png.Encode(f, img)
}

func writeICO(path string, images []image.Image) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer f.Close()
	return ico.EncodeAll(f, images)
}

func writeTrayStatusAssets(assetsDir, name string, img image.Image) error {
	img256 := resize.Resize(256, 256, img, resize.Lanczos3)
	img64 := resize.Resize(64, 64, img, resize.Lanczos3)
	img48 := resize.Resize(48, 48, img, resize.Lanczos3)
	img32 := resize.Resize(32, 32, img, resize.Lanczos3)
	img16 := resize.Resize(16, 16, img, resize.Lanczos3)
	if err := writePNG(filepath.Join(assetsDir, "tray-"+name+".png"), img32); err != nil {
		return err
	}
	return writeICO(filepath.Join(assetsDir, "tray-"+name+".ico"), []image.Image{img16, img32, img48, img64, img256})
}

func bezierQ(p0, p1, p2 Point, t float64) Point {
	mt := 1.0 - t
	return Point{
		X: mt*mt*p0.X + 2*mt*t*p1.X + t*t*p2.X,
		Y: mt*mt*p0.Y + 2*mt*t*p1.Y + t*t*p2.Y,
	}
}

func main() {
	fmt.Println("Generating SVG files...")
	assetsDir := filepath.Join("assets", "icons")
	windowsDir := filepath.Join("assets", "windows")

	// Ensure directories exist
	_ = os.MkdirAll(assetsDir, 0755)
	_ = os.MkdirAll(windowsDir, 0755)

	err := os.WriteFile(filepath.Join(assetsDir, "app.svg"), []byte(appSVGContent), 0644)
	if err != nil {
		panic(err)
	}

	err = os.WriteFile(filepath.Join(assetsDir, "logo.svg"), []byte(logoSVGContent), 0644)
	if err != nil {
		panic(err)
	}

	fmt.Println("Rasterizing app.png at 512x512 using antialiased distance field...")

	// Pre-generate arm points
	var pathPoints []Point
	for i := 0; i <= 96; i++ {
		t := float64(i) / 96.0
		pathPoints = append(pathPoints, Point{0, -96.0 * t})
	}
	p0 := Point{0, -96}
	p1 := Point{120, -96}
	p2 := Point{173.20508, 100.0}
	for i := 0; i <= 250; i++ {
		t := float64(i) / 250.0
		pathPoints = append(pathPoints, bezierQ(p0, p1, p2, t))
	}

	var allPathPoints []Point
	for _, p := range pathPoints {
		allPathPoints = append(allPathPoints, p)
		allPathPoints = append(allPathPoints, rotate(p, 120))
		allPathPoints = append(allPathPoints, rotate(p, 240))
	}

	outerNodes := []Point{
		{0, -200},
		{173.20508, 100},
		{-173.20508, 100},
	}

	// Inside checker for a point
	checkInside := func(p Point) (bool, float64) {
		r := math.Sqrt(p.X*p.X + p.Y*p.Y)

		// 1. Center node
		if r <= 24.0 {
			return true, math.Abs(r - 24.0)
		}
		minDist := math.Abs(r - 24.0)

		// 2. Outer nodes
		for _, node := range outerNodes {
			d := distance(p, node)
			if d <= 16.0 {
				return true, math.Abs(d - 16.0)
			}
			if math.Abs(d-16.0) < minDist {
				minDist = math.Abs(d - 16.0)
			}
		}

		// 3. Outer ring
		if r >= 188.0 && r <= 212.0 {
			d1 := math.Abs(r - 188.0)
			d2 := math.Abs(r - 212.0)
			d := d1
			if d2 < d {
				d = d2
			}
			return true, d
		}
		dRing := math.Min(math.Abs(r-188.0), math.Abs(r-212.0))
		if dRing < minDist {
			minDist = dRing
		}

		// 4. Arms
		dArms := 999999.0
		for _, pathPt := range allPathPoints {
			d := distance(p, pathPt)
			if d < dArms {
				dArms = d
			}
		}
		if dArms <= 10.0 {
			return true, math.Abs(dArms - 10.0)
		}
		if math.Abs(dArms-10.0) < minDist {
			minDist = math.Abs(dArms - 10.0)
		}

		return false, minDist
	}

	// Linear gradient color function
	getColor := func(spx, spy float64) color.NRGBA {
		u := (spx + spy) / 1024.0
		if u < 0.0 {
			u = 0.0
		}
		if u > 1.0 {
			u = 1.0
		}

		var r, g, b float64
		if u < 0.5 {
			t := u / 0.5
			r = 0.0 + (79.0-0.0)*t
			g = 242.0 + (172.0-242.0)*t
			b = 254.0 + (254.0-254.0)*t
		} else {
			t := (u - 0.5) / 0.5
			r = 79.0 + (0.0-79.0)*t
			g = 172.0 + (255.0-172.0)*t
			b = 254.0 + (135.0-254.0)*t
		}
		return color.NRGBA{R: uint8(r), G: uint8(g), B: uint8(b), A: 255}
	}

	img := image.NewNRGBA(image.Rect(0, 0, 512, 512))

	// Draw 512x512 canvas
	for y := 0; y < 512; y++ {
		for x := 0; x < 512; x++ {
			cx := float64(x) + 0.5 - 256.0
			cy := float64(y) + 0.5 - 256.0

			inside, borderDist := checkInside(Point{cx, cy})

			if borderDist > 1.5 {
				// Far from boundary - either 100% inside or 100% outside
				if inside {
					img.SetNRGBA(x, y, getColor(float64(x)+0.5, float64(y)+0.5))
				} else {
					img.SetNRGBA(x, y, color.NRGBA{0, 0, 0, 0})
				}
			} else {
				// Near boundary - perform 4x4 supersampling
				var rSum, gSum, bSum, aSum float64
				for sy := 0; sy < 4; sy++ {
					for sx := 0; sx < 4; sx++ {
						spx := float64(x) + (float64(sx)+0.5)/4.0
						spy := float64(y) + (float64(sy)+0.5)/4.0

						subInside, _ := checkInside(Point{spx - 256.0, spy - 256.0})
						if subInside {
							c := getColor(spx, spy)
							rSum += float64(c.R)
							gSum += float64(c.G)
							bSum += float64(c.B)
							aSum += 255.0
						}
					}
				}
				img.SetNRGBA(x, y, color.NRGBA{
					R: uint8(rSum / 16.0),
					G: uint8(gSum / 16.0),
					B: uint8(bSum / 16.0),
					A: uint8(aSum / 16.0),
				})
			}
		}
	}

	fmt.Println("Generating PNG and ICO formats...")

	// Resizing images
	img256 := resize.Resize(256, 256, img, resize.Lanczos3)
	img64 := resize.Resize(64, 64, img, resize.Lanczos3)
	img48 := resize.Resize(48, 48, img, resize.Lanczos3)
	img32 := resize.Resize(32, 32, img, resize.Lanczos3)
	img16 := resize.Resize(16, 16, img, resize.Lanczos3)

	// Save app-256.png
	fApp256, err := os.Create(filepath.Join(assetsDir, "app-256.png"))
	if err != nil {
		panic(err)
	}
	_ = png.Encode(fApp256, img256)
	fApp256.Close()

	// Save favicon-32.png
	fFav32, err := os.Create(filepath.Join(assetsDir, "favicon-32.png"))
	if err != nil {
		panic(err)
	}
	_ = png.Encode(fFav32, img32)
	fFav32.Close()

	// Save favicon.ico (sizes: 16, 32, 48)
	fFavIco, err := os.Create(filepath.Join(assetsDir, "favicon.ico"))
	if err != nil {
		panic(err)
	}
	err = ico.EncodeAll(fFavIco, []image.Image{img16, img32, img48})
	if err != nil {
		panic(err)
	}
	fFavIco.Close()

	// Save tray.ico (sizes: 16, 32, 48, 64, 256)
	fTrayIco, err := os.Create(filepath.Join(assetsDir, "tray.ico"))
	if err != nil {
		panic(err)
	}
	err = ico.EncodeAll(fTrayIco, []image.Image{img16, img32, img48, img64, img256})
	if err != nil {
		panic(err)
	}
	fTrayIco.Close()

	// Save assets/windows/flextray.ico (same content as tray.ico)
	fFlexTrayIco, err := os.Create(filepath.Join(windowsDir, "flextray.ico"))
	if err != nil {
		panic(err)
	}
	// Decode from bytes to verify / ensure no locked descriptors
	var buf bytes.Buffer
	err = ico.EncodeAll(&buf, []image.Image{img16, img32, img48, img64, img256})
	if err != nil {
		panic(err)
	}
	_, _ = fFlexTrayIco.Write(buf.Bytes())
	fFlexTrayIco.Close()

	for name, stops := range trayStatusGradients {
		if err := writeTrayStatusAssets(assetsDir, name, gradientIcon(img, stops)); err != nil {
			panic(err)
		}
	}

	fmt.Println("All assets successfully generated!")
}
