package inspect

import "testing"

func TestSocket_Exposure(t *testing.T) {
	cases := map[string]Exposure{
		"127.0.0.1":    ExposureLoopback,
		"127.1.2.3":    ExposureLoopback,
		"::1":          ExposureLoopback,
		"*":            ExposureAllInterfaces,
		"0.0.0.0":      ExposureAllInterfaces,
		"::":           ExposureAllInterfaces,
		"192.168.1.10": ExposureInterface,
		"fe80::1%lo0":  ExposureInterface,
		"fe80::1%4":    ExposureInterface,
		"":             ExposureUnknown,
		"not-an-ip":    ExposureUnknown,
	}
	for addr, want := range cases {
		if got := (Socket{Address: addr}).Exposure(); got != want {
			t.Errorf("Socket{Address: %q}.Exposure() = %q, want %q", addr, got, want)
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

func TestMarkUnavailable(t *testing.T) {
	p := Process{Unavailable: map[Field]string{}}
	p.MarkUnavailable(FieldCommand, "permission denied")
	if p.Unavailable[FieldCommand] != "permission denied" {
		t.Errorf("got %v", p.Unavailable)
	}
}
