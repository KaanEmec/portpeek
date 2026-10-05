package inspect

import (
	"errors"
	"fmt"
	"testing"
)

func TestClassifyAddress(t *testing.T) {
	cases := map[string]Exposure{
		"127.0.0.1":    ExposureLoopback,
		"127.1.2.3":    ExposureLoopback,
		"::1":          ExposureLoopback,
		"[::1]":        ExposureLoopback,
		"*":            ExposureAllInterfaces,
		"":             ExposureAllInterfaces,
		"0.0.0.0":      ExposureAllInterfaces,
		"::":           ExposureAllInterfaces,
		"[::]":         ExposureAllInterfaces,
		"192.168.1.10": ExposureInterface,
		"fe80::1%en0":  ExposureInterface,
		"not-an-ip":    ExposureUnknown,
	}
	for addr, want := range cases {
		if got := ClassifyAddress(addr); got != want {
			t.Errorf("ClassifyAddress(%q) = %q, want %q", addr, got, want)
		}
	}
}

func TestQueryValidate(t *testing.T) {
	valid := []Query{{Port: 1}, {Port: 65535}, {Port: 80, Protocol: TCP}, {Port: 53, Protocol: UDP}}
	for _, q := range valid {
		if err := q.Validate(); err != nil {
			t.Errorf("%+v: unexpected error %v", q, err)
		}
	}
	invalid := []Query{{Port: 0}, {Port: 65536}, {Port: -1}, {Port: 80, Protocol: "sctp"}}
	for _, q := range invalid {
		if err := q.Validate(); err == nil {
			t.Errorf("%+v: expected error", q)
		}
	}
}

func TestKindOf(t *testing.T) {
	base := &Error{Kind: KindPermissionDenied, Op: "lsof", Err: errors.New("denied")}
	wrapped := fmt.Errorf("inspecting: %w", base)
	if got := KindOf(wrapped); got != KindPermissionDenied {
		t.Errorf("KindOf(wrapped) = %q", got)
	}
	if got := KindOf(errors.New("plain")); got != "" {
		t.Errorf("KindOf(plain) = %q, want empty", got)
	}
	if !errors.Is(wrapped, base) {
		t.Error("errors.Is should find the base error")
	}
}

func TestMarkUnavailable(t *testing.T) {
	var p Process
	p.MarkUnavailable(FieldCommand, "permission denied")
	if p.Unavailable[FieldCommand] != "permission denied" {
		t.Errorf("got %v", p.Unavailable)
	}
}
