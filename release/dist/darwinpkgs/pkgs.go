package darwinpkgs

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"flexconnect/release/dist"
)

type pkgTarget struct{ goarch string }

func (t *pkgTarget) String() string { return fmt.Sprintf("darwin/%s/pkg", t.goarch) }

func (t *pkgTarget) Build(b *dist.Build) ([]string, error) {
	if _, err := b.LookPath("pkgbuild"); err != nil {
		return nil, fmt.Errorf("pkgbuild not found in PATH")
	}
	if _, err := b.LookPath("productbuild"); err != nil {
		return nil, fmt.Errorf("productbuild not found in PATH")
	}

	artifacts, err := b.BuildCommonArtifacts("darwin", t.goarch)
	if err != nil {
		return nil, err
	}
	workDir, err := b.TmpDir("darwin-pkg-*")
	if err != nil {
		return nil, err
	}
	rootDir := filepath.Join(workDir, "root")
	scriptsDir := filepath.Join(workDir, "scripts")
	componentPkg := filepath.Join(workDir, "FlexConnect.pkg")
	finalPkg := filepath.Join(b.Out, pkgFilename(b.Version, t.goarch))
	if err := stageRoot(rootDir, artifacts); err != nil {
		return nil, err
	}
	if err := copyScripts(scriptsDir, b); err != nil {
		return nil, err
	}

	componentArgs := componentPkgbuildArgs(rootDir, scriptsDir, b.Version, componentPkg)
	if err := runCommand(b, "pkgbuild", componentArgs...); err != nil {
		return nil, err
	}
	productArgs := productBuildArgs(componentPkg, finalPkg)
	if err := runCommand(b, "productbuild", productArgs...); err != nil {
		return nil, err
	}
	return []string{finalPkg}, nil
}

func stageRoot(root string, artifacts dist.CommonArtifacts) error {
	for _, dir := range []string{
		filepath.Join(root, "usr", "local", "bin"),
		filepath.Join(root, "Library", "LaunchDaemons"),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := copyFile(artifacts.FlexConnect, filepath.Join(root, "usr", "local", "bin", "flexconnect"), 0o755); err != nil {
		return err
	}
	if err := copyFile(artifacts.FlexConnectD, filepath.Join(root, "usr", "local", "bin", "flexconnectd"), 0o755); err != nil {
		return err
	}
	if err := copyFile(artifacts.FlexTray, filepath.Join(root, "usr", "local", "bin", "flextray"), 0o755); err != nil {
		return err
	}
	return copyFile(artifacts.LaunchdPlist, filepath.Join(root, "Library", "LaunchDaemons", "com.flexconnect.flexconnectd.plist"), 0o644)
}

func copyScripts(dst string, b *dist.Build) error {
	if err := os.MkdirAll(dst, 0o755); err != nil {
		return err
	}
	for _, name := range []string{"preinstall", "postinstall"} {
		src := b.RepoPath("release", "pkg", name)
		if err := copyFile(src, filepath.Join(dst, name), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func componentPkgbuildArgs(rootDir, scriptsDir, version, outPkg string) []string {
	return []string{
		"--root", rootDir,
		"--scripts", scriptsDir,
		"--identifier", "com.flexconnect.pkg",
		"--version", version,
		outPkg,
	}
}

func productBuildArgs(componentPkg, finalPkg string) []string {
	return []string{
		"--package", componentPkg,
		finalPkg,
	}
}

func pkgFilename(version, arch string) string {
	return fmt.Sprintf("flexconnect_%s_darwin_%s.pkg", version, arch)
}

func runCommand(b *dist.Build, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = b.Repo
	if b.Verbose {
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
	}
	return cmd.Run()
}

func copyFile(src, dst string, mode os.FileMode) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dst, data, mode)
}
