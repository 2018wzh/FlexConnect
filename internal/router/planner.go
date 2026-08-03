package router

import (
	"strings"

	"flexconnect/internal/osnet"
	"flexconnect/internal/types"
)

type Planner interface {
	Plan(serverInclude, serverExclude []string, profile types.Profile) []types.RouteSpec
}

type DefaultPlanner struct{}

// RouteLists is the normalized route input consumed by the tunnel setup.
// Local entries are applied after server entries and therefore replace a
// server entry with the same destination, even when the action differs.
type RouteLists struct {
	Include []string
	Exclude []string
}

func (DefaultPlanner) Plan(serverInclude, serverExclude []string, profile types.Profile) []types.RouteSpec {
	if !profile.AcceptServerRoutes {
		serverInclude = nil
		serverExclude = nil
	}
	merged := mergeRoutes(serverInclude, serverExclude, profile.CustomInclude, profile.CustomExclude)
	routes := make([]types.RouteSpec, 0, len(merged))
	for _, route := range merged {
		routes = append(routes, types.RouteSpec{
			Destination: route.destination,
			Action:      route.action,
			Metric:      route.metric,
			Source:      route.source,
			Enabled:     true,
		})
	}
	return routes
}

// MergeRouteLists applies the same precedence rules used by Plan to the
// route lists passed to the platform network manager. This keeps the routes
// installed on the host identical to the routes reported by the daemon.
func MergeRouteLists(serverInclude, serverExclude, localInclude, localExclude []string) RouteLists {
	routes := mergeRoutes(serverInclude, serverExclude, localInclude, localExclude)
	merged := RouteLists{}
	for _, route := range routes {
		switch route.action {
		case "include":
			merged.Include = append(merged.Include, route.destination)
		case "exclude":
			merged.Exclude = append(merged.Exclude, route.destination)
		}
	}
	return merged
}

type mergedRoute struct {
	destination string
	action      string
	metric      int
	source      string
}

func mergeRoutes(serverInclude, serverExclude, localInclude, localExclude []string) []mergedRoute {
	routes := make([]mergedRoute, 0, len(serverInclude)+len(serverExclude)+len(localInclude)+len(localExclude))
	byDestination := make(map[string]int, cap(routes))
	appendRoutes := func(values []string, action, source string, metric int) {
		for _, raw := range values {
			destination := normalizeDestination(raw)
			if destination == "" {
				continue
			}
			entry := mergedRoute{
				destination: destination,
				action:      action,
				metric:      metric,
				source:      source,
			}
			if index, ok := byDestination[destination]; ok {
				// Later entries have higher precedence. Local entries are
				// appended after server entries, so local configuration wins.
				routes[index] = entry
				continue
			}
			byDestination[destination] = len(routes)
			routes = append(routes, entry)
		}
	}

	appendRoutes(serverInclude, "include", "server", 6)
	appendRoutes(serverExclude, "exclude", "server", 5)
	appendRoutes(localInclude, "include", "local", 4)
	appendRoutes(localExclude, "exclude", "local", 3)
	return routes
}

func normalizeDestination(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if prefix, err := osnet.ParsePrefix(raw); err == nil {
		return prefix.String()
	}
	return raw
}
