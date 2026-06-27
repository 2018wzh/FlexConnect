package main

import (
	"image/color"
	"testing"
)

func TestTrayStatusGradients(t *testing.T) {
	tests := map[string]color.NRGBA{
		"green": {R: 0, G: 240, B: 0, A: 255},
		"red":   {R: 240, G: 0, B: 0, A: 255},
	}
	for name, want := range tests {
		stops := trayStatusGradients[name]
		if got := stops[1]; got != want {
			t.Fatalf("%s midpoint = %#v, want %#v", name, got, want)
		}
		if stops[0] == stops[1] || stops[1] == stops[2] {
			t.Fatalf("%s does not preserve a gradient: %#v", name, stops)
		}
	}
}

func TestGradientColorUsesOriginalDiagonalStops(t *testing.T) {
	stops := trayStatusGradients["green"]
	if got := gradientColor(stops, 0); got != stops[0] {
		t.Fatalf("start = %#v, want %#v", got, stops[0])
	}
	if got := gradientColor(stops, 0.5); got != stops[1] {
		t.Fatalf("middle = %#v, want %#v", got, stops[1])
	}
	if got := gradientColor(stops, 1); got != stops[2] {
		t.Fatalf("end = %#v, want %#v", got, stops[2])
	}
}
