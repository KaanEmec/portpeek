package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"slices"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// privileged reports whether the process can see every user's sockets. See
// platform_unix.go and platform_windows.go.
var privileged = allSocketsVisible()

// Completeness hints end a text result that may be missing information.
// unknownOwnerHint wins over hiddenSocketsHint when both apply, because it
// names the gap the user can actually see in the output.
const (
	hiddenSocketsHint = "other users' sockets hidden; run with sudo"
	unknownOwnerHint  = "some owners unreadable; run with sudo"
)

// format says how a result is written.
type format struct {
	json bool
	// detail selects the --detail text view; it is ignored with json.
	detail bool
	view   textView
}

// render writes the result as JSON or text, including the no-match case.
func render(w io.Writer, q inspect.Query, owners []inspect.Owner, f format) error {
	if f.json {
		return writeJSON(w, q, owners)
	}
	return f.view.write(w, q, owners, f.detail)
}

// RenderText returns the default text answer for a query as `portpeek <port>`
// prints it when piped: a short headline and a few lines per owner, or the
// no-match line, followed by the completeness hint when the answer may be
// incomplete. Lines are cut to 100 columns and carry no styling. The
// terminal interface uses it so both describe a port in the same words.
func RenderText(q inspect.Query, owners []inspect.Owner) string {
	return plainView.compact(q, owners)
}

// RenderDetail returns the --detail text answer for a query, unstyled:
// every owner with its sockets, process details and stop commands in
// labelled sections. Nothing is cut.
func RenderDetail(q inspect.Query, owners []inspect.Owner) string {
	return plainView.detail(q, owners)
}

// CompletenessHint returns the one-line hint, without a trailing newline,
// that the CLI prints under a possibly incomplete answer for owners, or ""
// when the answer is complete.
func CompletenessHint(owners []inspect.Owner) string {
	return completenessHint(owners)
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

// completenessHint returns the line, without a newline, that ends a possibly
// incomplete text result, or "" when the result is complete.
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

// headlineProcess names the process in the --stop flow's messages, falling
// back to the PID when the name is unavailable and to "an unknown process"
// when the owner is unknown.
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
