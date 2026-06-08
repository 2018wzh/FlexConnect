package darwinpkgs

import (
	"sort"

	"flexconnect/release/dist"
)

func Targets() []dist.Target {
	targets := []dist.Target{
		&pkgTarget{goarch: "amd64"},
		&pkgTarget{goarch: "arm64"},
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].String() < targets[j].String()
	})
	return targets
}
