package dist

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type stubTarget string

func (t stubTarget) String() string                 { return string(t) }
func (t stubTarget) Build(*Build) ([]string, error) { return nil, nil }

func TestFilterTargets(t *testing.T) {
	targets := []Target{
		stubTarget("linux/amd64/tgz"),
		stubTarget("linux/amd64/deb"),
		stubTarget("windows/amd64/msi"),
	}

	got, err := FilterTargets(targets, []string{"linux/*/deb", "windows/*"})
	if err != nil {
		t.Fatalf("FilterTargets error: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 targets, got %d", len(got))
	}
	if got[0].String() != "linux/amd64/deb" || got[1].String() != "windows/amd64/msi" {
		t.Fatalf("unexpected targets: %v %v", got[0].String(), got[1].String())
	}
}

func TestWriteManifest(t *testing.T) {
	root := t.TempDir()
	outDir := filepath.Join(root, "dist")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	manifest := filepath.Join(root, "artifacts", "manifest.txt")

	build := &Build{Repo: root, Out: outDir}
	files := []string{
		filepath.Join(outDir, "flexconnect_1.0.0_amd64.deb"),
		filepath.Join(outDir, "flexconnect_1.0.0_x86_64.rpm"),
	}
	if err := build.WriteManifest(manifest, files); err != nil {
		t.Fatalf("WriteManifest error: %v", err)
	}

	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 2 {
		t.Fatalf("expected 2 lines, got %d", len(lines))
	}
	if filepath.Clean(lines[0]) != filepath.Join("..", "dist", "flexconnect_1.0.0_amd64.deb") {
		t.Fatalf("unexpected first manifest entry %q", lines[0])
	}
}
