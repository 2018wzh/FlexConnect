package unixpkgs

import (
	"archive/tar"
	"compress/gzip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"flexconnect/release/dist"

	"github.com/goreleaser/nfpm/v2"
	_ "github.com/goreleaser/nfpm/v2/deb"
	"github.com/goreleaser/nfpm/v2/files"
	_ "github.com/goreleaser/nfpm/v2/rpm"
)

type tgzTarget struct {
	goos   string
	goarch string
}

func (t *tgzTarget) String() string {
	return fmt.Sprintf("%s/%s/tgz", t.goos, t.goarch)
}

func (t *tgzTarget) Build(b *dist.Build) ([]string, error) {
	artifacts, err := b.BuildCommonArtifacts(t.goos, t.goarch)
	if err != nil {
		return nil, err
	}
	filename := tgzFilename(b.Version, t.goos, t.goarch)
	outPath := filepath.Join(b.Out, filename)

	f, err := os.Create(outPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	gw := gzip.NewWriter(f)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	root := strings.TrimSuffix(filename, ".tgz")
	if err := addTarDir(tw, root, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := addTarFile(tw, artifacts.FlexConnect, filepath.Join(root, filepath.Base(artifacts.FlexConnect)), 0o755); err != nil {
		return nil, err
	}
	if err := addTarFile(tw, artifacts.FlexConnectD, filepath.Join(root, filepath.Base(artifacts.FlexConnectD)), 0o755); err != nil {
		return nil, err
	}
	if err := addTarFile(tw, artifacts.FlexTray, filepath.Join(root, filepath.Base(artifacts.FlexTray)), 0o755); err != nil {
		return nil, err
	}
	systemdDir := filepath.Join(root, "systemd")
	if err := addTarDir(tw, systemdDir, time.Now().UTC()); err != nil {
		return nil, err
	}
	if err := addTarFile(tw, artifacts.ServiceFile, filepath.Join(systemdDir, "flexconnectd.service"), 0o644); err != nil {
		return nil, err
	}
	return []string{outPath}, nil
}

type debTarget struct{ goarch string }

func (t *debTarget) String() string { return fmt.Sprintf("linux/%s/deb", t.goarch) }

func (t *debTarget) Build(b *dist.Build) ([]string, error) {
	return buildLinuxPackage(b, "deb", t.goarch)
}

type rpmTarget struct{ goarch string }

func (t *rpmTarget) String() string { return fmt.Sprintf("linux/%s/rpm", t.goarch) }

func (t *rpmTarget) Build(b *dist.Build) ([]string, error) {
	return buildLinuxPackage(b, "rpm", t.goarch)
}

func buildLinuxPackage(b *dist.Build, pkgType, goarch string) ([]string, error) {
	artifacts, err := b.BuildCommonArtifacts("linux", goarch)
	if err != nil {
		return nil, err
	}

	contents, err := files.ExpandContentGlobs(files.Contents{
		&files.Content{Source: packageSourcePath(b.Repo, artifacts.FlexConnect), Destination: "/usr/bin/flexconnect", FileInfo: &files.ContentFileInfo{Mode: 0o755}},
		&files.Content{Source: packageSourcePath(b.Repo, artifacts.FlexConnectD), Destination: "/usr/sbin/flexconnectd", FileInfo: &files.ContentFileInfo{Mode: 0o755}},
		&files.Content{Source: packageSourcePath(b.Repo, artifacts.FlexTray), Destination: "/usr/bin/flextray", FileInfo: &files.ContentFileInfo{Mode: 0o755}},
		&files.Content{Source: packageSourcePath(b.Repo, artifacts.ServiceFile), Destination: "/lib/systemd/system/flexconnectd.service", FileInfo: &files.ContentFileInfo{Mode: 0o644}},
	}, false)
	if err != nil {
		return nil, err
	}

	info := nfpm.WithDefaults(&nfpm.Info{
		Name:        "flexconnect",
		Arch:        linuxPkgArch(pkgType, goarch),
		Platform:    "linux",
		Version:     b.Version,
		Maintainer:  "FlexConnect Maintainers <maintainers@flexconnect.local>",
		Description: "Cross-platform AnyConnect client with daemon, CLI, and tray",
		Homepage:    "https://example.invalid/flexconnect",
		License:     "Proprietary",
		Overridables: nfpm.Overridables{
			Contents: contents,
			Scripts: nfpm.Scripts{
				PostInstall: b.RepoPath("release", pkgType, pkgType+".postinst.sh"),
				PreRemove:   b.RepoPath("release", pkgType, pkgType+".prerm.sh"),
				PostRemove:  b.RepoPath("release", pkgType, pkgType+".postrm.sh"),
			},
		},
	})
	if pkgType == "deb" {
		info.Section = "net"
		info.Priority = "optional"
		info.Overridables.Depends = []string{"systemd"}
	} else {
		info.Overridables.Depends = []string{"systemd"}
		info.Overridables.RPM.Group = "Network"
	}

	pkg, err := nfpm.Get(pkgType)
	if err != nil {
		return nil, err
	}
	filename := linuxPackageFilename(pkgType, b.Version, info.Arch)
	outPath := filepath.Join(b.Out, filename)
	f, err := os.Create(outPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := pkg.Package(info, f); err != nil {
		return nil, err
	}
	return []string{outPath}, nil
}

func packageSourcePath(repoRoot, file string) string {
	rel, err := filepath.Rel(repoRoot, file)
	if err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(file)
}

func tgzFilename(version, goos, goarch string) string {
	return fmt.Sprintf("flexconnect_%s_%s_%s.tgz", version, goos, goarch)
}

func linuxPackageFilename(pkgType, version, arch string) string {
	return fmt.Sprintf("flexconnect_%s_%s.%s", version, arch, pkgType)
}

func linuxPkgArch(pkgType, arch string) string {
	switch pkgType {
	case "deb":
		switch arch {
		case "amd64":
			return "amd64"
		case "arm64":
			return "arm64"
		}
	case "rpm":
		switch arch {
		case "amd64":
			return "x86_64"
		case "arm64":
			return "aarch64"
		}
	}
	return arch
}

func addTarDir(tw *tar.Writer, name string, modTime time.Time) error {
	return tw.WriteHeader(&tar.Header{
		Name:     filepath.ToSlash(name) + "/",
		Mode:     0o755,
		Typeflag: tar.TypeDir,
		ModTime:  modTime,
	})
}

func addTarFile(tw *tar.Writer, src, dst string, mode int64) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return err
	}
	if err := tw.WriteHeader(&tar.Header{
		Name:    filepath.ToSlash(dst),
		Mode:    mode,
		Size:    fi.Size(),
		ModTime: fi.ModTime(),
	}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}
