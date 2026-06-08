package unixpkgs

import (
	"sort"

	"flexconnect/release/dist"
)

func Targets() []dist.Target {
	targets := []dist.Target{
		&tgzTarget{goos: "linux", goarch: "amd64"},
		&debTarget{goarch: "amd64"},
		&rpmTarget{goarch: "amd64"},
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].String() < targets[j].String()
	})
	return targets
}
