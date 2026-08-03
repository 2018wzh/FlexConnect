package main

import (
	"strings"
	"testing"
	"time"
)

func TestParseDotenvSupportsCommentsQuotesAndExport(t *testing.T) {
	got, err := parseDotenv("# comment\nexport ENDPOINT=https://vpn.example.test # inline\nUSERNAME=alice\nPASSWORD='p#ss'\nGROUP=\"engineering\\nteam\"\n")
	if err != nil {
		t.Fatalf("parseDotenv: %v", err)
	}
	if got["ENDPOINT"] != "https://vpn.example.test" {
		t.Fatalf("endpoint = %q", got["ENDPOINT"])
	}
	if got["PASSWORD"] != "p#ss" {
		t.Fatalf("password = %q", got["PASSWORD"])
	}
	if got["GROUP"] != "engineering\nteam" {
		t.Fatalf("group = %q", got["GROUP"])
	}
}

func TestParseDotenvRejectsMalformedAssignment(t *testing.T) {
	if _, err := parseDotenv("ENDPOINT\n"); err == nil {
		t.Fatal("malformed assignment was accepted")
	}
}

func TestEndpointForOutputRemovesSchemeOnly(t *testing.T) {
	if got := endpointForOutput("https://vpn.example.test/path"); got != "vpn.example.test" {
		t.Fatalf("endpoint output = %q", got)
	}
}

func TestNewSpeedtestConfigValidatesTargetAndLimits(t *testing.T) {
	config, err := newSpeedtestConfig("https://speed.example.test/download?bytes=1024", 1024, 5*time.Second)
	if err != nil {
		t.Fatalf("newSpeedtestConfig: %v", err)
	}
	if config.URL == "" || config.MaxBytes != 1024 || config.Timeout != 5*time.Second {
		t.Fatalf("config = %+v", config)
	}
	if got := speedtestHost(config.URL); got != "speed.example.test" {
		t.Fatalf("speedtest host = %q", got)
	}
}

func TestNewSpeedtestConfigRejectsCredentialsAndInvalidLimits(t *testing.T) {
	tests := []string{
		"https://user:password@speed.example.test/download",
		"ftp://speed.example.test/download",
		"https://speed.example.test/download",
	}
	for index, rawURL := range tests {
		maxBytes := int64(1024)
		timeout := 5 * time.Second
		if index == 2 {
			maxBytes = 0
		}
		if _, err := newSpeedtestConfig(rawURL, maxBytes, timeout); err == nil {
			t.Fatalf("accepted invalid speedtest configuration %q", rawURL)
		}
	}
	if _, err := newSpeedtestConfig("https://speed.example.test/download", 1024, 0); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("invalid timeout error = %v", err)
	}
}
