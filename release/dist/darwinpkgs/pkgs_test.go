package darwinpkgs

import (
	"os"
	"path/filepath"
	"testing"

	"flexconnect/release/dist"
)

func TestPkgFilename(t *testing.T) {
	if got := pkgFilename("1.2.3", "arm64"); got != "flexconnect_1.2.3_darwin_arm64.pkg" {
		t.Fatalf("unexpected pkg filename %q", got)
	}
}

func TestPkgCommandArgs(t *testing.T) {
	got := componentPkgbuildArgs("/tmp/root", "/tmp/scripts", "1.2.3", "/tmp/FlexConnect.pkg")
	if len(got) != 9 {
		t.Fatalf("unexpected arg count %d", len(got))
	}
	if got[0] != "--root" || got[2] != "--scripts" || got[4] != "--identifier" || got[6] != "--version" || got[7] != "1.2.3" {
		t.Fatalf("unexpected pkgbuild args %v", got)
	}
	product := productBuildArgs("/tmp/FlexConnect.pkg", "/tmp/out.pkg")
	if len(product) != 3 || product[0] != "--package" {
		t.Fatalf("unexpected productbuild args %v", product)
	}
}

func TestStageRoot(t *testing.T) {
	temp := t.TempDir()
	artifacts := dist.CommonArtifacts{
		FlexConnect:  writeTempFile(t, temp, "flexconnect", "cli"),
		FlexConnectD: writeTempFile(t, temp, "flexconnectd", "daemon"),
		FlexTray:     writeTempFile(t, temp, "flextray", "tray"),
		LaunchdPlist: writeTempFile(t, temp, "com.flexconnect.flexconnectd.plist", "plist"),
	}

	root := filepath.Join(temp, "root")
	if err := stageRoot(root, artifacts); err != nil {
		t.Fatalf("stageRoot error: %v", err)
	}

	want := []string{
		filepath.Join(root, "usr", "local", "bin", "flexconnect"),
		filepath.Join(root, "usr", "local", "bin", "flexconnectd"),
		filepath.Join(root, "usr", "local", "bin", "flextray"),
		filepath.Join(root, "Library", "LaunchDaemons", "com.flexconnect.flexconnectd.plist"),
	}
	for _, path := range want {
		if _, err := os.Stat(path); err != nil {
			t.Fatalf("expected staged file %q: %v", path, err)
		}
	}
}

func TestCopyScripts(t *testing.T) {
	repo := t.TempDir()
	scriptsSource := filepath.Join(repo, "release", "pkg")
	if err := os.MkdirAll(scriptsSource, 0o755); err != nil {
		t.Fatalf("mkdir scripts source: %v", err)
	}
	for _, name := range []string{"preinstall", "postinstall"} {
		if err := os.WriteFile(filepath.Join(scriptsSource, name), []byte(name), 0o755); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}

	build := &dist.Build{Repo: repo}
	dst := filepath.Join(t.TempDir(), "scripts")
	if err := copyScripts(dst, build); err != nil {
		t.Fatalf("copyScripts error: %v", err)
	}
	for _, name := range []string{"preinstall", "postinstall"} {
		if _, err := os.Stat(filepath.Join(dst, name)); err != nil {
			t.Fatalf("expected copied script %q: %v", name, err)
		}
	}
}

func TestPkgTargetBuildWithFakeTools(t *testing.T) {
	repo := t.TempDir()
	outDir := filepath.Join(t.TempDir(), "out")
	tmpDir := filepath.Join(t.TempDir(), "tmp")
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		t.Fatalf("mkdir out: %v", err)
	}
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		t.Fatalf("mkdir tmp: %v", err)
	}

	scriptsSource := filepath.Join(repo, "release", "pkg")
	if err := os.MkdirAll(scriptsSource, 0o755); err != nil {
		t.Fatalf("mkdir scripts source: %v", err)
	}
	for _, name := range []string{"preinstall", "postinstall"} {
		if err := os.WriteFile(filepath.Join(scriptsSource, name), []byte(name), 0o755); err != nil {
			t.Fatalf("write script %s: %v", name, err)
		}
	}

	toolsDir := filepath.Join(t.TempDir(), "tools")
	if err := os.MkdirAll(toolsDir, 0o755); err != nil {
		t.Fatalf("mkdir tools: %v", err)
	}
	writeBatch(t, filepath.Join(toolsDir, "pkgbuild.cmd"), "@echo off\r\nset out=\r\nfor %%I in (%*) do set out=%%~I\r\necho pkgbuild %*>\"%out%\"\r\n")
	writeBatch(t, filepath.Join(toolsDir, "productbuild.cmd"), "@echo off\r\nset out=\r\nfor %%I in (%*) do set out=%%~I\r\necho productbuild %*>\"%out%\"\r\n")

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", toolsDir+string(os.PathListSeparator)+oldPath)

	artifactsDir := filepath.Join(t.TempDir(), "artifacts")
	if err := os.MkdirAll(artifactsDir, 0o755); err != nil {
		t.Fatalf("mkdir artifacts: %v", err)
	}
	artifacts := dist.CommonArtifacts{
		FlexConnect:  writeTempFile(t, artifactsDir, "flexconnect", "cli"),
		FlexConnectD: writeTempFile(t, artifactsDir, "flexconnectd", "daemon"),
		FlexTray:     writeTempFile(t, artifactsDir, "flextray", "tray"),
		LaunchdPlist: writeTempFile(t, artifactsDir, "com.flexconnect.flexconnectd.plist", "plist"),
	}

	build := &dist.Build{
		Repo:    repo,
		Out:     outDir,
		Tmp:     tmpDir,
		Version: "1.2.3",
		CommonArtifactsFn: func(goos, goarch string) (dist.CommonArtifacts, error) {
			if goos != "darwin" || goarch != "amd64" {
				t.Fatalf("unexpected target %s/%s", goos, goarch)
			}
			return artifacts, nil
		},
	}

	files, err := (&pkgTarget{goarch: "amd64"}).Build(build)
	if err != nil {
		t.Fatalf("pkg target build error: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("expected 1 output file, got %d (%v)", len(files), files)
	}
	if _, err := os.Stat(files[0]); err != nil {
		t.Fatalf("expected package output %q: %v", files[0], err)
	}
}

func writeTempFile(t *testing.T, dir, name, content string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("write temp file %s: %v", name, err)
	}
	return path
}

func writeBatch(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write batch %s: %v", path, err)
	}
}
