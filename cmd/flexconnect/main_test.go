package main

import (
	"strings"
	"testing"

	"flexconnect/internal/types"
)

func TestFormatTrafficSnapshotText(t *testing.T) {
	got := formatTrafficSnapshot(types.TrafficSnapshot{
		Connected:              true,
		BytesSent:              1024,
		BytesReceived:          2048,
		BytesSentPerSecond:     512,
		BytesReceivedPerSecond: 1536,
		SampledAt:              "2026-06-27T00:00:00Z",
	})

	for _, want := range []string{
		"Connected: true",
		"Traffic Sent: 1024 B",
		"Traffic Received: 2048 B",
		"Speed Sent: 512 B/s",
		"Speed Received: 1536 B/s",
		"Sampled: 2026-06-27T00:00:00Z",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in:\n%s", want, got)
		}
	}
}
