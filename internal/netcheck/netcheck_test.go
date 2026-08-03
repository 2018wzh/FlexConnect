package netcheck

import (
	"encoding/json"
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

func TestEndpointForOutputRemovesSchemeAndPath(t *testing.T) {
	if got := endpointForOutput("https://vpn.example.test/path"); got != "vpn.example.test" {
		t.Fatalf("endpoint output = %q", got)
	}
}

func TestNewSpeedtestConfigValidatesTargetAndLimits(t *testing.T) {
	config, err := NewSpeedtestConfig("https://speed.example.test/download?bytes=1024", 1024, 5*time.Second)
	if err != nil {
		t.Fatalf("NewSpeedtestConfig: %v", err)
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
		if _, err := NewSpeedtestConfig(rawURL, maxBytes, timeout); err == nil {
			t.Fatalf("accepted invalid speedtest configuration %q", rawURL)
		}
	}
	if _, err := NewSpeedtestConfig("https://speed.example.test/download", 1024, 0); err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("invalid timeout error = %v", err)
	}
}

func TestParseMTUUsesConfiguredFallbackAndCapsServerValue(t *testing.T) {
	if got, err := parseMTU("", 1399); err != nil || got != 1399 {
		t.Fatalf("empty MTU = %d, %v", got, err)
	}
	if got, err := parseMTU("1400", 1399); err != nil || got != 1399 {
		t.Fatalf("server MTU above configured value = %d, %v", got, err)
	}
	if got, err := parseMTU("1200", 1399); err != nil || got != 1200 {
		t.Fatalf("server MTU below configured value = %d, %v", got, err)
	}
	if _, err := parseMTU("bad", 1399); err == nil {
		t.Fatal("invalid MTU was accepted")
	}
}

func TestResultDoesNotCarryCredentials(t *testing.T) {
	data, err := json.Marshal(Result{Endpoint: "vpn.example.test", Speedtest: &SpeedtestResult{TargetHost: "speed.example.test"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"password", "session-token", "webvpn"} {
		if strings.Contains(strings.ToLower(string(data)), secret) {
			t.Fatalf("result contains credential marker %q: %s", secret, data)
		}
	}
}
