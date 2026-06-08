package windowspkgs

import (
	"sort"

	"flexconnect/release/dist"
)

func Targets() []dist.Target {
	targets := []dist.Target{
		&zipTarget{goarch: "amd64"},
		&msiTarget{goarch: "amd64"},
	}
	sort.Slice(targets, func(i, j int) bool {
		return targets[i].String() < targets[j].String()
	})
	return targets
}
