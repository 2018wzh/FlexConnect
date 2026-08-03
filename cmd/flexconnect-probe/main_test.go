package main

import "testing"

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
