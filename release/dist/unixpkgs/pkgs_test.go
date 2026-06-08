package unixpkgs

import "testing"

func TestLinuxPkgArch(t *testing.T) {
	if got := linuxPkgArch("deb", "amd64"); got != "amd64" {
		t.Fatalf("deb amd64 = %q", got)
	}
	if got := linuxPkgArch("rpm", "amd64"); got != "x86_64" {
		t.Fatalf("rpm amd64 = %q", got)
	}
	if got := linuxPkgArch("rpm", "arm64"); got != "aarch64" {
		t.Fatalf("rpm arm64 = %q", got)
	}
}

func TestPackageFilenames(t *testing.T) {
	if got := tgzFilename("1.2.3", "linux", "amd64"); got != "flexconnect_1.2.3_linux_amd64.tgz" {
		t.Fatalf("unexpected tgz filename %q", got)
	}
	if got := linuxPackageFilename("deb", "1.2.3", "amd64"); got != "flexconnect_1.2.3_amd64.deb" {
		t.Fatalf("unexpected package filename %q", got)
	}
}
