// Package inspect defines the shared model for Port Peek: what a query is, what
// an answer looks like, and the Inspector interface that platform adapters
// implement. It contains no OS-specific code and never prints.
package inspect

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"
)

// Protocol is a transport protocol a socket uses.
type Protocol string

const (
	TCP Protocol = "tcp"
	UDP Protocol = "udp"
)

// Family is the IP address family of a socket.
type Family string

const (
	IPv4 Family = "ipv4"
	IPv6 Family = "ipv6"
)

// Exposure classifies how reachable a bound address is from other hosts. It
// is derived from the bound address only; it says nothing about firewalls.
type Exposure string

const (
	// ExposureLoopback means only processes on this machine can connect.
	ExposureLoopback Exposure = "loopback"
	// ExposureAllInterfaces means the socket is bound to a wildcard address and
	// is reachable on every local interface, including external ones.
	ExposureAllInterfaces Exposure = "all-interfaces"
	// ExposureInterface means the socket is bound to one specific non-loopback
	// address.
	ExposureInterface Exposure = "interface"
	// ExposureUnknown means the address could not be classified.
	ExposureUnknown Exposure = "unknown"
)

// Query asks which local processes own a port. Protocol empty means both.
type Query struct {
	Port     int
	Protocol Protocol
}

// Validate reports whether the query is well formed.
func (q Query) Validate() error {
	if q.Port < 1 || q.Port > 65535 {
		return fmt.Errorf("port must be between 1 and 65535, got %d", q.Port)
	}
	switch q.Protocol {
	case "", TCP, UDP:
		return nil
	default:
		return fmt.Errorf("unknown protocol %q", q.Protocol)
	}
}

// Socket is one bound local socket.
type Socket struct {
	Protocol Protocol
	Family   Family
	// Address is the bound local address as the OS reports it, without the
	// port: "127.0.0.1", "*", "::1", "0.0.0.0", "fe80::1". Wildcards appear
	// as "*", "0.0.0.0" or "::".
	Address string
	Port    int
	// State is the socket state as reported ("LISTEN" for TCP listeners,
	// empty for UDP, which has no listening state).
	State string
}

// Exposure classifies the socket's bound address.
func (s Socket) Exposure() Exposure {
	return ClassifyAddress(s.Address)
}

// ClassifyAddress derives an Exposure from a bound address string.
func ClassifyAddress(addr string) Exposure {
	addr = strings.Trim(addr, "[]")
	if addr == "*" || addr == "" {
		return ExposureAllInterfaces
	}
	ip, err := netip.ParseAddr(strings.SplitN(addr, "%", 2)[0])
	if err != nil {
		return ExposureUnknown
	}
	switch {
	case ip.IsUnspecified():
		return ExposureAllInterfaces
	case ip.IsLoopback():
		return ExposureLoopback
	default:
		return ExposureInterface
	}
}

// Field names a Process detail that an adapter may be unable to read.
type Field string

const (
	FieldName       Field = "name"
	FieldUser       Field = "user"
	FieldCommand    Field = "command"
	FieldWorkingDir Field = "working_dir"
)

// Process describes the owning process. Empty strings are not meaningful on
// their own; check Unavailable for the reason a field is missing.
type Process struct {
	PID        int
	Name       string
	User       string
	Command    string
	WorkingDir string
	// Unavailable maps a field to a short human-readable reason it could not
	// be read, e.g. "permission denied" or "process exited". Never nil.
	Unavailable map[Field]string
}

// MarkUnavailable records why a field is missing.
func (p *Process) MarkUnavailable(f Field, reason string) {
	if p.Unavailable == nil {
		p.Unavailable = map[Field]string{}
	}
	p.Unavailable[f] = reason
}

// Owner is one process together with every matching socket it holds.
type Owner struct {
	Process Process
	Sockets []Socket
}

// Result is the answer to a Query. Zero owners means no matching socket; that
// is a valid answer, not an error.
type Result struct {
	Query  Query
	Owners []Owner
}

// Inspector finds the local owners of a port.
type Inspector interface {
	Inspect(ctx context.Context, q Query) (Result, error)
}

// Kind categorises an inspection failure.
type Kind string

const (
	// KindToolMissing means a required system command is not installed.
	KindToolMissing Kind = "tool-missing"
	// KindPermissionDenied means the OS refused access to socket or process
	// information.
	KindPermissionDenied Kind = "permission-denied"
	// KindCommandFailed means a system command ran but failed unexpectedly.
	KindCommandFailed Kind = "command-failed"
	// KindUnsupported means no adapter exists for this platform.
	KindUnsupported Kind = "unsupported-platform"
)

// Error is a categorised inspection failure.
type Error struct {
	Kind Kind
	// Op names what was attempted, e.g. "lsof", "ps".
	Op  string
	Err error
}

func (e *Error) Error() string {
	if e.Err == nil {
		return fmt.Sprintf("%s: %s", e.Op, e.Kind)
	}
	return fmt.Sprintf("%s: %s: %v", e.Op, e.Kind, e.Err)
}

func (e *Error) Unwrap() error { return e.Err }

// KindOf returns the Kind of err if it is an *Error, or "" otherwise.
func KindOf(err error) Kind {
	var ie *Error
	if errors.As(err, &ie) {
		return ie.Kind
	}
	return ""
}
