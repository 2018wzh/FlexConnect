package updater

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"flexconnect/internal/buildinfo"
)

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0.6", "1.0.7", -1},
		{"1.0.10", "1.0.9", 1},
		{"1.0.0", "1.0.0", 0},
		// Missing segments are treated as 0, so 1.0 == 1.0.0.
		{"1.0", "1.0.0", 0},
		{"v1.0.6", "1.0.6", 0},
		{"1.0.7-rc1", "1.0.6", 1},
		{"1.0.6-rc1", "1.0.6", 0},
		{"2.0.0", "1.9.9", 1},
	}
	for _, tc := range cases {
		if got := compareVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compareVersions(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

func TestCheckDisabledWhenNoRepo(t *testing.T) {
	called := false
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		called = true
	}))
	defer server.Close()

	c := New("")
	info := c.Check(context.Background())
	if !info.Disabled {
		t.Fatalf("expected Disabled=true for empty repo, got %+v", info)
	}
	if info.Error != "" {
		t.Fatalf("expected no error for disabled check, got %q", info.Error)
	}
	if called {
		t.Fatal("expected no HTTP request for disabled check")
	}
}

func TestCheckParsesLatestRelease(t *testing.T) {
	release := Release{
		TagName:     "v9.9.9",
		HTMLURL:     "https://github.com/owner/repo/releases/tag/v9.9.9",
		PublishedAt: "2026-01-02T03:04:05Z",
		Assets: []Asset{
			{Name: "flexconnect_9.9.9_linux_amd64.tar.gz", DownloadURL: "https://example.com/dl", Size: 12345},
		},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("Accept"); got != "application/vnd.github+json" {
			t.Errorf("Accept header = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(release)
	}))
	defer server.Close()

	c := New("owner/repo")
	c.baseURL = server.URL + "/repos/"
	info := c.Check(context.Background())

	if info.Error != "" {
		t.Fatalf("unexpected error: %q", info.Error)
	}
	if info.Disabled {
		t.Fatal("expected check to be enabled")
	}
	if info.LatestVersion != "9.9.9" {
		t.Errorf("LatestVersion = %q, want 9.9.9", info.LatestVersion)
	}
	if info.CurrentVersion != buildinfo.Version {
		t.Errorf("CurrentVersion = %q, want %q", info.CurrentVersion, buildinfo.Version)
	}
	// 9.9.9 is newer than the current build version (1.2.1).
	if !info.UpdateAvailable {
		t.Error("expected UpdateAvailable=true")
	}
	if info.ReleaseURL != release.HTMLURL {
		t.Errorf("ReleaseURL = %q", info.ReleaseURL)
	}
	if len(info.Assets) != 1 || info.Assets[0].Name != release.Assets[0].Name || info.Assets[0].Size != 12345 {
		t.Errorf("Assets not parsed correctly: %+v", info.Assets)
	}
}

func TestCheckHandlesRateLimit(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer server.Close()

	c := New("owner/repo")
	c.baseURL = server.URL + "/repos/"
	info := c.Check(context.Background())

	if info.Error == "" {
		t.Fatal("expected non-empty Error for rate-limited response")
	}
	if info.UpdateAvailable {
		t.Error("expected UpdateAvailable=false on error")
	}
}

func TestCheckNoUpdateWhenCurrentIsNewer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(Release{TagName: "v0.0.1"})
	}))
	defer server.Close()

	c := New("owner/repo")
	c.baseURL = server.URL + "/repos/"
	info := c.Check(context.Background())

	if info.Error != "" {
		t.Fatalf("unexpected error: %q", info.Error)
	}
	if info.UpdateAvailable {
		t.Error("expected UpdateAvailable=false when current version is newer")
	}
}

func TestCheckRespectsContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
	}))
	defer server.Close()

	c := New("owner/repo")
	c.baseURL = server.URL + "/repos/"
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	info := c.Check(ctx)

	if info.Error == "" {
		t.Fatal("expected error when context is cancelled")
	}
}
