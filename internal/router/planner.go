package router

import "flexconnect/internal/types"

type Planner interface {
	Plan(serverInclude, serverExclude []string, profile types.Profile) []types.RouteSpec
}

type DefaultPlanner struct{}

func (DefaultPlanner) Plan(serverInclude, serverExclude []string, profile types.Profile) []types.RouteSpec {
	routes := make([]types.RouteSpec, 0, len(serverInclude)+len(serverExclude)+len(profile.CustomInclude)+len(profile.CustomExclude))
	if profile.AcceptServerRoutes {
		for _, cidr := range serverInclude {
			if cidr == "" {
				continue
			}
			routes = append(routes, types.RouteSpec{
				Destination: cidr,
				Action:      "include",
				Metric:      6,
				Source:      "server",
				Enabled:     true,
			})
		}
		for _, cidr := range serverExclude {
			if cidr == "" {
				continue
			}
			routes = append(routes, types.RouteSpec{
				Destination: cidr,
				Action:      "exclude",
				Metric:      5,
				Source:      "server",
				Enabled:     true,
			})
		}
	}
	for _, cidr := range profile.CustomInclude {
		if cidr == "" {
			continue
		}
		routes = append(routes, types.RouteSpec{
			Destination: cidr,
			Action:      "include",
			Metric:      4,
			Source:      "local",
			Enabled:     true,
		})
	}
	for _, cidr := range profile.CustomExclude {
		if cidr == "" {
			continue
		}
		routes = append(routes, types.RouteSpec{
			Destination: cidr,
			Action:      "exclude",
			Metric:      3,
			Source:      "local",
			Enabled:     true,
		})
	}
	return routes
}

