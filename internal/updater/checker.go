// Package updater performs check-only online update queries against GitHub
// Releases. It never downloads or replaces binaries; callers surface the
// result to the user (CLI, tray, daemon API).
package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	semver "github.com/Masterminds/semver/v3"

	"flexconnect/internal/buildinfo"
	"flexconnect/internal/types"
)

const (
	githubAPIBase = "https://api.github.com/repos/"
	httpTimeout   = 20 * time.Second
)

// Asset describes one downloadable artifact attached to a release.
type Asset struct {
	Name        string `json:"name"`
	DownloadURL string `json:"browser_download_url"`
	Size        int64  `json:"size"`
}

// Release is the subset of the GitHub "latest release" payload we use.
type Release struct {
	TagName     string  `json:"tag_name"`
	HTMLURL     string  `json:"html_url"`
	PublishedAt string  `json:"published_at"`
	Assets      []Asset `json:"assets"`
}

// Info is the check result returned to callers (daemon/API/CLI/tray).
type Info struct {
	CurrentVersion  string
	LatestVersion   string
	UpdateAvailable bool
	ReleaseURL      string
	PublishedAt     string
	Assets          []Asset
	CheckedAt       string
	Disabled        bool // true when no repo is configured
	Error           string
}

// Checker queries GitHub Releases for the latest version of a repository.
type Checker struct {
	repo       string // "owner/name"
	baseURL    string // defaults to githubAPIBase; overridable in tests
	httpClient *http.Client
	now        func() time.Time
}

// New returns a Checker for the given "owner/name" repo. An empty repo yields
// a checker whose Check is a no-op returning Disabled=true.
func New(repo string) *Checker {
	return &Checker{
		repo:    strings.TrimSpace(repo),
		baseURL: githubAPIBase,
		httpClient: &http.Client{
			Timeout: httpTimeout,
		},
		now: time.Now,
	}
}

// Check queries the latest GitHub release and compares it to the current
// build version. When repo is empty it returns a disabled Info without any
// network activity.
func (c *Checker) Check(ctx context.Context) Info {
	current := buildinfo.Version
	if c == nil || c.repo == "" {
		return Info{CurrentVersion: current, Disabled: true, CheckedAt: nowString(c)}
	}

	url := c.baseURL + c.repo + "/releases/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return Info{CurrentVersion: current, CheckedAt: nowString(c), Error: err.Error()}
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "FlexConnect/"+current)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return Info{CurrentVersion: current, CheckedAt: nowString(c), Error: err.Error()}
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// Rate-limited or not-found: surface a friendly error without panic.
		msg := fmt.Sprintf("github releases returned HTTP %d", resp.StatusCode)
		if rem := resp.Header.Get("X-RateLimit-Remaining"); rem != "" {
			msg += " (rate limit remaining=" + rem + ")"
		}
		return Info{CurrentVersion: current, CheckedAt: nowString(c), Error: msg}
	}

	var release Release
	if err := json.NewDecoder(resp.Body).Decode(&release); err != nil {
		return Info{CurrentVersion: current, CheckedAt: nowString(c), Error: "decode release: " + err.Error()}
	}

	latest := strings.TrimPrefix(release.TagName, "v")
	available := compareVersions(latest, current) > 0
	return Info{
		CurrentVersion:  current,
		LatestVersion:   latest,
		UpdateAvailable: available,
		ReleaseURL:      release.HTMLURL,
		PublishedAt:     release.PublishedAt,
		Assets:          release.Assets,
		CheckedAt:       nowString(c),
	}
}

func nowString(c *Checker) string {
	if c == nil || c.now == nil {
		return time.Now().UTC().Format(time.RFC3339)
	}
	return c.now().UTC().Format(time.RFC3339)
}

func compareVersions(a, b string) int {
	left, leftErr := semver.StrictNewVersion(strings.TrimPrefix(strings.TrimSpace(a), "v"))
	right, rightErr := semver.StrictNewVersion(strings.TrimPrefix(strings.TrimSpace(b), "v"))
	if leftErr != nil || rightErr != nil {
		return 0
	}
	return left.Compare(right)
}

// ToTypes converts the check result into the wire type exposed by the local
// control API.
func (i Info) ToTypes() types.UpdateInfo {
	assets := make([]types.UpdateAsset, 0, len(i.Assets))
	for _, a := range i.Assets {
		assets = append(assets, types.UpdateAsset{Name: a.Name, DownloadURL: a.DownloadURL, Size: a.Size})
	}
	return types.UpdateInfo{
		CurrentVersion:  i.CurrentVersion,
		LatestVersion:   i.LatestVersion,
		UpdateAvailable: i.UpdateAvailable,
		ReleaseURL:      i.ReleaseURL,
		PublishedAt:     i.PublishedAt,
		Assets:          assets,
		CheckedAt:       i.CheckedAt,
		Disabled:        i.Disabled,
		Error:           i.Error,
	}
}
