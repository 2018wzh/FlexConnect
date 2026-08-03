package router

import (
	"reflect"
	"testing"

	"flexconnect/internal/types"
)

func TestDefaultPlannerLocalRoutesOverrideServerRoutes(t *testing.T) {
	profile := types.Profile{
		AcceptServerRoutes: true,
		CustomInclude: []string{
			"172.20.0.0/12",
			"49.52.0.0/15",
		},
		CustomExclude: []string{"202.120.0.0/255.255.0.0"},
	}

	got := (DefaultPlanner{}).Plan(
		[]string{"172.20.0.0/12", "49.52.0.0/15", "202.120.0.0/16"},
		nil,
		profile,
	)
	want := []types.RouteSpec{
		{Destination: "172.16.0.0/12", Action: "include", Metric: 4, Source: "local", Enabled: true},
		{Destination: "49.52.0.0/15", Action: "include", Metric: 4, Source: "local", Enabled: true},
		{Destination: "202.120.0.0/16", Action: "exclude", Metric: 3, Source: "local", Enabled: true},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Plan = %#v, want %#v", got, want)
	}
}

func TestMergeRouteListsLocalActionOverridesServerAction(t *testing.T) {
	got := MergeRouteLists(
		[]string{"10.1.2.3/8"},
		[]string{"192.168.0.0/16"},
		[]string{"192.168.0.0/16"},
		[]string{"10.0.0.0/8"},
	)
	want := RouteLists{
		Include: []string{"192.168.0.0/16"},
		Exclude: []string{"10.0.0.0/8"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("MergeRouteLists = %#v, want %#v", got, want)
	}
}
