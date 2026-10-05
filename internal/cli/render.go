package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// privileged reports whether the process can see every user's sockets. On
// macOS, lsof silently omits other users' sockets unless run as root, so an
// unprivileged "no match" may simply mean "not visible".
var privileged = os.Geteuid() == 0

// labelWidth aligns field values: the longest label is "Working dir:".
const labelWidth = 14

// render writes the result as JSON or text, including the no-match case.
func render(w io.Writer, q inspect.Query, owners []inspect.Owner, asJSON bool) error {
	switch {
	case asJSON:
		return writeJSON(w, q, owners)
	case len(owners) == 0:
		msg := fmt.Sprintf("No process is using port %d (%s).\n", q.Port, protocolPhrase(q))
		if !privileged {
			msg += "Sockets owned by other users are not visible without elevated privileges (try sudo).\n"
		}
		_, err := io.WriteString(w, msg)
		return err
	default:
		_, err := io.WriteString(w, renderText(q, owners))
		return err
	}
}

// protocolPhrase describes the protocols a query covered.
func protocolPhrase(q inspect.Query) string {
	if q.Protocol == "" {
		return "tcp or udp"
	}
	return string(q.Protocol)
}

// renderText formats owners as labelled blocks separated by blank lines.
func renderText(q inspect.Query, owners []inspect.Owner) string {
	var b strings.Builder
	if len(owners) > 1 {
		fmt.Fprintf(&b, "%d processes use port %d:\n\n", len(owners), q.Port)
	}
	for i, owner := range owners {
		if i > 0 {
			b.WriteString("\n")
		}
		writeOwner(&b, q, owner)
	}
	return b.String()
}

func writeOwner(b *strings.Builder, q inspect.Query, owner inspect.Owner) {
	p := owner.Process

	fmt.Fprintf(b, "Port %d%s is used by %s\n", q.Port, protocolSuffix(q, owner), headlineProcess(p))

	mixed := len(protocols(owner.Sockets)) > 1
	for _, s := range owner.Sockets {
		writeField(b, "Address", socketDescription(s, mixed))
	}
	writeExposure(b, owner.Sockets)
	writeField(b, "User", processField(p, inspect.FieldUser, p.User))
	writeField(b, "Command", processField(p, inspect.FieldCommand, p.Command))
	writeField(b, "Working dir", processField(p, inspect.FieldWorkingDir, p.WorkingDir))
	if p.PID > 0 {
		writeField(b, "Stop", "kill "+strconv.Itoa(p.PID))
	}
}

// writeField writes one labelled line. An empty label continues the previous
// field's value column.
func writeField(b *strings.Builder, label, value string) {
	if label != "" {
		label += ":"
	}
	fmt.Fprintf(b, "  %-*s%s\n", labelWidth, label, value)
}

// headlineProcess names the process, falling back to the PID when the name is
// unavailable.
func headlineProcess(p inspect.Process) string {
	if p.Name == "" {
		return fmt.Sprintf("PID %d", p.PID)
	}
	return fmt.Sprintf("%s (PID %d)", p.Name, p.PID)
}

// protocolSuffix returns "/tcp", "/udp", "/tcp+udp", or the query protocol
// when the owner has no sockets.
func protocolSuffix(q inspect.Query, owner inspect.Owner) string {
	names := protocols(owner.Sockets)
	if len(names) == 0 && q.Protocol != "" {
		names = []string{string(q.Protocol)}
	}
	if len(names) == 0 {
		return ""
	}
	return "/" + strings.Join(names, "+")
}

// protocols returns the distinct protocols of sockets in first-seen order.
func protocols(sockets []inspect.Socket) []string {
	names := []string{}
	for _, s := range sockets {
		name := string(s.Protocol)
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	return names
}

// socketAddress formats the bound address and port, bracketing IPv6.
func socketAddress(s inspect.Socket) string {
	host := strings.Trim(s.Address, "[]")
	return net.JoinHostPort(host, strconv.Itoa(s.Port))
}

// socketDescription renders "127.0.0.1:3000 (IPv4, LISTEN)". The protocol is
// added only when the owner holds sockets of more than one protocol.
func socketDescription(s inspect.Socket, withProtocol bool) string {
	state := s.State
	if state == "" {
		state = "bound"
	}
	parts := []string{}
	if withProtocol {
		parts = append(parts, string(s.Protocol))
	}
	parts = append(parts, familyLabel(s.Family), state)
	return fmt.Sprintf("%s (%s)", socketAddress(s), strings.Join(parts, ", "))
}

func familyLabel(f inspect.Family) string {
	switch f {
	case inspect.IPv4:
		return "IPv4"
	case inspect.IPv6:
		return "IPv6"
	default:
		return "unknown family"
	}
}

// exposureText explains an exposure in plain language.
func exposureText(s inspect.Socket) string {
	switch s.Exposure() {
	case inspect.ExposureLoopback:
		return "loopback only — reachable from this machine only"
	case inspect.ExposureAllInterfaces:
		return "all interfaces — reachable from other machines on the network"
	case inspect.ExposureInterface:
		return fmt.Sprintf(
			"specific interface %s — reachable by hosts that can route to it",
			strings.Trim(s.Address, "[]"),
		)
	default:
		return "unknown"
	}
}

// writeExposure writes one Exposure line when every socket agrees, otherwise
// one line per socket prefixed with its address.
func writeExposure(b *strings.Builder, sockets []inspect.Socket) {
	if len(sockets) == 0 {
		return
	}

	texts := make([]string, len(sockets))
	same := true
	for i, s := range sockets {
		texts[i] = exposureText(s)
		same = same && texts[i] == texts[0]
	}
	if same {
		writeField(b, "Exposure", texts[0])
		return
	}

	for i, s := range sockets {
		label := ""
		if i == 0 {
			label = "Exposure"
		}
		writeField(b, label, fmt.Sprintf("%s: %s", socketAddress(s), texts[i]))
	}
}

// processField returns the value, or the unavailable reason when the field
// could not be read.
func processField(p inspect.Process, f inspect.Field, value string) string {
	if value != "" {
		return value
	}
	if reason, ok := p.Unavailable[f]; ok && reason != "" {
		return fmt.Sprintf("unavailable (%s)", reason)
	}
	return "unavailable"
}

// jsonSchemaVersion is the version of the JSON output contract.
const jsonSchemaVersion = 1

type jsonOutput struct {
	Schema int         `json:"schema"`
	Query  jsonQuery   `json:"query"`
	Owners []jsonOwner `json:"owners"`
}

type jsonQuery struct {
	Port     int    `json:"port"`
	Protocol string `json:"protocol"`
}

type jsonOwner struct {
	Process jsonProcess  `json:"process"`
	Sockets []jsonSocket `json:"sockets"`
}

type jsonProcess struct {
	PID         int               `json:"pid"`
	Name        string            `json:"name"`
	User        string            `json:"user"`
	Command     string            `json:"command"`
	WorkingDir  string            `json:"working_dir"`
	Unavailable map[string]string `json:"unavailable"`
}

type jsonSocket struct {
	Protocol string `json:"protocol"`
	Family   string `json:"family"`
	Address  string `json:"address"`
	Port     int    `json:"port"`
	State    string `json:"state"`
	Exposure string `json:"exposure"`
}

type jsonErrorOutput struct {
	Schema int       `json:"schema"`
	Error  jsonError `json:"error"`
}

type jsonError struct {
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// writeJSON writes the versioned JSON document for a result. Owners and
// unavailable maps are never null.
func writeJSON(w io.Writer, q inspect.Query, owners []inspect.Owner) error {
	out := jsonOutput{
		Schema: jsonSchemaVersion,
		Query:  jsonQuery{Port: q.Port, Protocol: string(q.Protocol)},
		Owners: make([]jsonOwner, 0, len(owners)),
	}
	for _, o := range owners {
		out.Owners = append(out.Owners, toJSONOwner(o))
	}
	return encodeJSON(w, out)
}

func toJSONOwner(o inspect.Owner) jsonOwner {
	p := o.Process
	unavailable := make(map[string]string, len(p.Unavailable))
	for field, reason := range p.Unavailable {
		unavailable[string(field)] = reason
	}

	sockets := make([]jsonSocket, 0, len(o.Sockets))
	for _, s := range o.Sockets {
		sockets = append(sockets, jsonSocket{
			Protocol: string(s.Protocol),
			Family:   string(s.Family),
			Address:  s.Address,
			Port:     s.Port,
			State:    s.State,
			Exposure: string(s.Exposure()),
		})
	}

	return jsonOwner{
		Process: jsonProcess{
			PID:         p.PID,
			Name:        p.Name,
			User:        p.User,
			Command:     p.Command,
			WorkingDir:  p.WorkingDir,
			Unavailable: unavailable,
		},
		Sockets: sockets,
	}
}

// writeJSONError writes the JSON error document used with --json.
func writeJSONError(w io.Writer, kind, message string) error {
	return encodeJSON(w, jsonErrorOutput{
		Schema: jsonSchemaVersion,
		Error:  jsonError{Kind: kind, Message: message},
	})
}

func encodeJSON(w io.Writer, v any) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	return enc.Encode(v)
}
