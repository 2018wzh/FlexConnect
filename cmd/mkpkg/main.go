package main

import (
	"archive/tar"
	"compress/gzip"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/goreleaser/nfpm/v2"
	_ "github.com/goreleaser/nfpm/v2/deb"
	"github.com/goreleaser/nfpm/v2/files"
	_ "github.com/goreleaser/nfpm/v2/rpm"
)

func main() {
	var (
		pkgType   = flag.String("type", "tgz", "package type: tgz|deb|rpm")
		version   = flag.String("version", "0.1.0", "package version")
		goos      = flag.String("goos", runtime.GOOS, "target GOOS")
		goarch    = flag.String("goarch", runtime.GOARCH, "target GOARCH")
		outDir    = flag.String("outdir", "dist/packages", "output directory")
		buildDir  = flag.String("builddir", "", "temporary build directory")
	)
	flag.Parse()

	if *buildDir == "" {
		*buildDir = filepath.Join(os.TempDir(), "flexconnect-pkg-"+strings.ToLower(*pkgType)+"-"+*goos+"-"+*goarch)
	}
	if err := os.MkdirAll(*outDir, 0o755); err != nil {
		fail(err)
	}
	if err := os.MkdirAll(*buildDir, 0o755); err != nil {
		fail(err)
	}

	artifacts, err := buildArtifacts(*buildDir, *goos, *goarch)
	if err != nil {
		fail(err)
	}

	switch *pkgType {
	case "tgz":
		fail(buildTGZ(*outDir, *version, *goos, *goarch, artifacts))
	case "deb", "rpm":
		fail(buildLinuxPackage(*pkgType, *outDir, *version, *goarch, artifacts))
	default:
		fail(fmt.Errorf("unsupported package type: %s", *pkgType))
	}
}

type builtArtifacts struct {
	FlexConnect  string
	FlexConnectD string
	FlexTray     string
	ServiceFile  string
	LaunchdPlist string
	WintunDLL    string
}

func buildArtifacts(dir, goos, goarch string) (builtArtifacts, error) {
	env := append(os.Environ(), "GOOS="+goos, "GOARCH="+goarch)
	ext := ""
	if goos == "windows" {
		ext = ".exe"
	}

	out := builtArtifacts{
		FlexConnect:  filepath.Join(dir, "flexconnect"+ext),
		FlexConnectD: filepath.Join(dir, "flexconnectd"+ext),
		FlexTray:     filepath.Join(dir, "flextray"+ext),
		ServiceFile:  filepath.Join("scripts", "systemd", "flexconnectd.service"),
		LaunchdPlist: filepath.Join("scripts", "launchd", "com.flexconnect.flexconnectd.plist"),
		WintunDLL:    filepath.Join("assets", "windows", "wintun.dll"),
	}

	if err := goBuild("./cmd/flexconnect", out.FlexConnect, env); err != nil {
		return builtArtifacts{}, err
	}
	if err := goBuild("./cmd/flexconnectd", out.FlexConnectD, env); err != nil {
		return builtArtifacts{}, err
	}
	if err := goBuild("./cmd/flextray", out.FlexTray, env); err != nil {
		return builtArtifacts{}, err
	}
	return out, nil
}

func goBuild(pkg, out string, env []string) error {
	cmd := exec.Command("go", "build", "-o", out, pkg)
	cmd.Env = env
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

func buildTGZ(outDir, version, goos, goarch string, artifacts builtArtifacts) error {
	filename := fmt.Sprintf("flexconnect_%s_%s_%s.tgz", version, goos, goarch)
	outPath := filepath.Join(outDir, filename)
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	gw := gzip.NewWriter(f)
	defer gw.Close()
	tw := tar.NewWriter(gw)
	defer tw.Close()

	root := strings.TrimSuffix(filename, ".tgz")
	if err := addTarDir(tw, root); err != nil {
		return err
	}
	if err := addTarFile(tw, artifacts.FlexConnect, filepath.Join(root, filepath.Base(artifacts.FlexConnect)), 0o755); err != nil {
		return err
	}
	if err := addTarFile(tw, artifacts.FlexConnectD, filepath.Join(root, filepath.Base(artifacts.FlexConnectD)), 0o755); err != nil {
		return err
	}
	if err := addTarFile(tw, artifacts.FlexTray, filepath.Join(root, filepath.Base(artifacts.FlexTray)), 0o755); err != nil {
		return err
	}
	if goos == "linux" {
		if err := addTarDir(tw, filepath.Join(root, "systemd")); err != nil {
			return err
		}
		if err := addTarFile(tw, artifacts.ServiceFile, filepath.Join(root, "systemd", "flexconnectd.service"), 0o644); err != nil {
			return err
		}
	}
	if goos == "darwin" {
		if err := addTarDir(tw, filepath.Join(root, "launchd")); err != nil {
			return err
		}
		if err := addTarFile(tw, artifacts.LaunchdPlist, filepath.Join(root, "launchd", "com.flexconnect.flexconnectd.plist"), 0o644); err != nil {
			return err
		}
	}
	if goos == "windows" {
		if err := addTarFile(tw, artifacts.WintunDLL, filepath.Join(root, "wintun.dll"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func buildLinuxPackage(pkgType, outDir, version, arch string, artifacts builtArtifacts) error {
	flexconnectSrc := packageSourcePath(artifacts.FlexConnect)
	flexconnectdSrc := packageSourcePath(artifacts.FlexConnectD)
	flextraySrc := packageSourcePath(artifacts.FlexTray)
	serviceSrc := packageSourcePath(artifacts.ServiceFile)

	contents, err := files.ExpandContentGlobs(files.Contents{
		&files.Content{Source: flexconnectSrc, Destination: "/usr/bin/flexconnect", FileInfo: &files.ContentFileInfo{Mode: 0o755}},
		&files.Content{Source: flexconnectdSrc, Destination: "/usr/sbin/flexconnectd", FileInfo: &files.ContentFileInfo{Mode: 0o755}},
		&files.Content{Source: flextraySrc, Destination: "/usr/bin/flextray", FileInfo: &files.ContentFileInfo{Mode: 0o755}},
		&files.Content{Source: serviceSrc, Destination: "/lib/systemd/system/flexconnectd.service", FileInfo: &files.ContentFileInfo{Mode: 0o644}},
	}, false)
	if err != nil {
		return err
	}

	info := nfpm.WithDefaults(&nfpm.Info{
		Name:        "flexconnect",
		Arch:        linuxPkgArch(pkgType, arch),
		Platform:    "linux",
		Version:     version,
		Maintainer:  "FlexConnect Maintainers <maintainers@flexconnect.local>",
		Description: "Cross-platform AnyConnect client with daemon, CLI, and tray",
		Homepage:    "https://example.invalid/flexconnect",
		License:     "Proprietary",
		Overridables: nfpm.Overridables{
			Contents: contents,
			Scripts: nfpm.Scripts{
				PostInstall: filepath.Join("release", pkgType, pkgType+".postinst.sh"),
				PreRemove:   filepath.Join("release", pkgType, pkgType+".prerm.sh"),
				PostRemove:  filepath.Join("release", pkgType, pkgType+".postrm.sh"),
			},
		},
	})
	if pkgType == "deb" {
		info.Section = "net"
		info.Priority = "optional"
		info.Overridables.Depends = []string{"systemd"}
	} else {
		info.Overridables.RPM.Group = "Network"
		info.Overridables.Depends = []string{"systemd"}
	}

	packager, err := nfpm.Get(pkgType)
	if err != nil {
		return err
	}
	filename := fmt.Sprintf("flexconnect_%s_%s.%s", version, info.Arch, pkgType)
	outPath := filepath.Join(outDir, filename)
	f, err := os.Create(outPath)
	if err != nil {
		return err
	}
	defer f.Close()
	return packager.Package(info, f)
}

func packageSourcePath(path string) string {
	wd, err := os.Getwd()
	if err != nil {
		return filepath.ToSlash(path)
	}
	rel, err := filepath.Rel(wd, path)
	if err == nil && rel != "" && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(rel)
	}
	return filepath.ToSlash(path)
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

func addTarDir(tw *tar.Writer, name string) error {
	return tw.WriteHeader(&tar.Header{
		Name:     filepath.ToSlash(name) + "/",
		Mode:     0o755,
		Typeflag: tar.TypeDir,
		ModTime:  time.Now(),
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

func fail(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}
