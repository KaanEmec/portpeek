package tui

import (
	"slices"
	"testing"

	"github.com/kaanemec/portpeek/internal/cli"
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

	controlCenter := func(b cli.Binding) row { return row{bind: b, process: "ControlCenter", pid: 1007} }
	want := []row{
		controlCenter(cli.Binding{
			Protocol: inspect.TCP, Address: "*", Port: 5000, State: inspect.StateListen, IPv4: true, IPv6: true,
		}),
		controlCenter(cli.Binding{
			Protocol: inspect.UDP, Address: "fe80::1%lo0", Port: 5000, State: inspect.StateBound, IPv6: true,
		}),
		{
			bind:    cli.Binding{Protocol: inspect.UDP, Address: "0.0.0.0", Port: 5353, State: inspect.StateBound, IPv4: true},
			process: "unknown",
		},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("rows = %+v, want %+v", got, want)
	}

	// Cells use the CLI's wording.
	cells := []struct {
		name, got, want string
	}{
		{name: "dual-stack binding is marked", got: got[0].bind.String(), want: "*:5000 (v4+v6)"},
		{name: "IPv6 binding is bracketed", got: got[1].bind.String(), want: "[fe80::1%lo0]:5000"},
		{name: "unknown PID", got: cli.PIDText(got[2].pid), want: "-"},
		{name: "known PID", got: cli.PIDText(got[0].pid), want: "1007"},
		{name: "exposure label", got: cli.ExposureLabel(got[0].bind.Exposure()), want: "all interfaces"},
	}
	for _, c := range cells {
		if c.got != c.want {
			t.Errorf("%s: got %q, want %q", c.name, c.got, c.want)
		}
	}
}
