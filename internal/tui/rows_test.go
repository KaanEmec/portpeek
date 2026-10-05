package tui

import (
	"slices"
	"testing"

	"github.com/kaanemec/portpeek/internal/inspect"
)

func TestBuildRows(t *testing.T) {
	t.Parallel()

	snap := inspect.Snapshot{Owners: []inspect.Owner{
		{
			Process: process(1007, "ControlCenter"),
			Sockets: []inspect.Socket{
				// A dual-stack wildcard listener as lsof reports it.
				sock(inspect.TCP, inspect.IPv4, "*", 5000),
				sock(inspect.TCP, inspect.IPv6, "*", 5000),
				sock(inspect.UDP, inspect.IPv6, "fe80::1%lo0", 5000),
			},
		},
		unknownOwner(),
	}}

	got := buildRows(snap)

	want := []row{
		{port: 5000, proto: inspect.TCP, address: "*", process: "ControlCenter", pid: 1007, exposure: inspect.ExposureAllInterfaces},
		{port: 5000, proto: inspect.UDP, address: "fe80::1%lo0", process: "ControlCenter", pid: 1007, exposure: inspect.ExposureInterface},
		{port: 5353, proto: inspect.UDP, address: "0.0.0.0", process: "unknown", exposure: inspect.ExposureAllInterfaces},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}

	cells := []struct {
		name, got, want string
	}{
		{name: "IPv6 binding is bracketed", got: got[1].binding(), want: "[fe80::1%lo0]:5000"},
		{name: "unknown PID", got: got[2].pidText(), want: "-"},
		{name: "known PID", got: got[0].pidText(), want: "1007"},
		{name: "exposure label", got: exposureLabel(got[0].exposure), want: "all interfaces"},
	}
	for _, c := range cells {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}
