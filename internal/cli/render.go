package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// privileged reports whether the process can see every user's sockets. See
// platform_unix.go and platform_windows.go.
var privileged = allSocketsVisible()

// Completeness hints end a text result that may be missing information.
// unknownOwnerHint wins over hiddenSocketsHint when both apply, because it
// names the gap the user can actually see in the output.
const (
	hiddenSocketsHint = "Sockets owned by other users are not visible without elevated privileges (try sudo).\n"
	unknownOwnerHint  = "Owner details for some sockets are not readable without elevated privileges (try sudo).\n"
)

// labelWidth aligns field values: the longest label is "Working dir:".
const labelWidth = 14

// render writes the result as JSON or text, including the no-match case.
func render(w io.Writer, q inspect.Query, owners []inspect.Owner, asJSON bool) error {
	if asJSON {
		return writeJSON(w, q, owners)
	}

	var text string
	hint := completenessHint(owners)
	switch {
	case len(owners) == 0:
		text = fmt.Sprintf("No listening or bound socket on port %d (%s).\n", q.Port, protocolPhrase(q))
		text += hint
	default:
		text = renderText(q, owners)
		if hint != "" {
			text += "\n" + hint
		}
	}
	_, err := io.WriteString(w, text)
	return err
}

// isUnknownOwner reports whether an adapter listed a socket but could not
// attribute it to a process, which it signals with PID 0. Such an owner has
// no PID to show or signal.
func isUnknownOwner(p inspect.Process) bool {
	return p.PID == 0
}

func hasUnknownOwner(owners []inspect.Owner) bool {
	return slices.ContainsFunc(owners, func(o inspect.Owner) bool { return isUnknownOwner(o.Process) })
}

// complete reports whether the result is known to show every socket on the
// port together with its owner: the process must be privileged, and no
// listed socket may have an unknown owner.
func complete(owners []inspect.Owner) bool {
	return privileged && !hasUnknownOwner(owners)
}

// completenessHint returns the line that ends a possibly incomplete text
// result, or "" when the result is complete.
func completenessHint(owners []inspect.Owner) string {
	switch {
	case hasUnknownOwner(owners):
		return unknownOwnerHint
	case !privileged:
		return hiddenSocketsHint
	default:
		return ""
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

	protos := protocols(owner.Sockets)
	fmt.Fprintf(
		b,
		"Port %d/%s is used by %s\n",
		q.Port,
		strings.Join(protos, "+"),
		headlineProcess(p),
	)

	unknown := isUnknownOwner(p)
	if unknown {
		writeField(b, "Owner", processField(p, inspect.FieldName, ""))
	}
	mixed := len(protos) > 1
	for _, s := range owner.Sockets {
		writeField(b, "Address", socketDescription(s, mixed))
	}
	writeExposure(b, owner.Sockets)
	writeField(b, "User", processField(p, inspect.FieldUser, p.User))
	writeField(b, "Command", processField(p, inspect.FieldCommand, p.Command))
	writeField(b, "Working dir", processField(p, inspect.FieldWorkingDir, p.WorkingDir))
	// An unknown owner has no PID, and "kill 0" would signal the user's own
	// process group, so no stop hint is printed for it.
	if !unknown {
		writeField(b, "Stop", stopHint(p.PID))
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
// unavailable and to "an unknown process" when the owner is unknown.
func headlineProcess(p inspect.Process) string {
	switch {
	case isUnknownOwner(p):
		return "an unknown process"
	case p.Name == "":
		return fmt.Sprintf("PID %d", p.PID)
	default:
		return fmt.Sprintf("%s (PID %d)", p.Name, p.PID)
	}
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
	return net.JoinHostPort(s.Address, strconv.Itoa(s.Port))
}

// socketDescription renders "127.0.0.1:3000 (IPv4, LISTEN)". The protocol is
// added only when the owner holds sockets of more than one protocol.
func socketDescription(s inspect.Socket, withProtocol bool) string {
	parts := []string{}
	if withProtocol {
		parts = append(parts, string(s.Protocol))
	}
	parts = append(parts, familyLabel(s.Family), stateLabel(s))
	return fmt.Sprintf("%s (%s)", socketAddress(s), strings.Join(parts, ", "))
}

// stateLabel describes how the socket holds its port. A bound TCP socket is
// called out because it holds the port without accepting connections.
func stateLabel(s inspect.Socket) string {
	switch {
	case s.State == inspect.StateListen:
		return "LISTEN"
	case s.State == inspect.StateBound && s.Protocol == inspect.TCP:
		return "bound, not listening"
	case s.State == inspect.StateBound:
		return "bound"
	default:
		return s.State
	}
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
		return "loopback only — accepts connections from this machine only"
	case inspect.ExposureAllInterfaces:
		return "all interfaces — accepts connections on every network interface (firewall not checked)"
	case inspect.ExposureInterface:
		return fmt.Sprintf(
			"specific interface %s — accepts connections on that address only (firewall not checked)",
			s.Address,
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
	Schema int       `json:"schema"`
	Query  jsonQuery `json:"query"`
	// Complete is false when other users' sockets may be hidden because the
	// process is not privileged, or when some socket's owner is unknown.
	Complete bool        `json:"complete"`
	Owners   []jsonOwner `json:"owners"`
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
	// PID is 0 when the owner is unknown.
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
		Schema:   jsonSchemaVersion,
		Query:    jsonQuery{Port: q.Port, Protocol: string(q.Protocol)},
		Complete: complete(owners),
		Owners:   make([]jsonOwner, 0, len(owners)),
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
