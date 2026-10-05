package cli

import (
	"bytes"
	"regexp"
	"strings"
	"testing"
	"unicode"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// tcpSocket is a TCP listener on port 3000.
func tcpSocket(family inspect.Family, addr string) inspect.Socket {
	return inspect.Socket{Protocol: inspect.TCP, Family: family, Address: addr, Port: 3000, State: inspect.StateListen}
}

func udpSocket(family inspect.Family, addr string, port int) inspect.Socket {
	return inspect.Socket{Protocol: inspect.UDP, Family: family, Address: addr, Port: port, State: inspect.StateBound}
}

// mdnsOwner is a process holding the dual-stack mDNS wildcard, as several
// processes do on macOS.
func mdnsOwner(pid int, name, command string) inspect.Owner {
	return inspect.Owner{
		Process: inspect.Process{
			PID:         pid,
			Name:        name,
			User:        "kaanemec",
			Command:     command,
			WorkingDir:  "/",
			Unavailable: map[inspect.Field]string{},
		},
		Sockets: []inspect.Socket{
			udpSocket(inspect.IPv4, "*", 5353),
			udpSocket(inspect.IPv6, "*", 5353),
		},
	}
}

// chromeCommand is a browser helper's command line: argv[0] is an app bundle
// path with spaces, followed by many long arguments.
const chromeCommand = "/Applications/Google Chrome.app/Contents/Frameworks/Google Chrome Framework.framework/" +
	"Versions/141.0.7390.55/Helpers/Google Chrome Helper.app/Contents/MacOS/Google Chrome Helper " +
	"--type=utility --utility-sub-type=network.mojom.NetworkService --lang=en-US " +
	"--service-sandbox-type=network --shared-files --field-trial-handle=1718379636,r,358309612294444706"

func mdnsOwners() []inspect.Owner {
	root := mdnsOwner(647, "mDNSResponder", "/usr/sbin/mDNSResponder")
	root.Process.User = "_mdnsresponder"
	return []inspect.Owner{
		root,
		mdnsOwner(19212, "Codex (Service)", "/Applications/ChatGPT.app/Contents/MacOS/Codex (Service) --type=utility"),
		mdnsOwner(43947, "Google Chrome Helper", chromeCommand),
	}
}

func mixedOwner() inspect.Owner {
	o := nodeOwner()
	o.Sockets = []inspect.Socket{
		tcpSocket(inspect.IPv4, "127.0.0.1"),
		udpSocket(inspect.IPv4, "192.168.1.5", 3000),
		tcpSocket(inspect.IPv6, "::1"),
	}
	return o
}

func dualStackOwner() inspect.Owner {
	o := nodeOwner()
	o.Sockets = []inspect.Socket{
		tcpSocket(inspect.IPv4, "*"),
		tcpSocket(inspect.IPv6, "*"),
	}
	return o
}

func restrictedOwner() inspect.Owner {
	p := inspect.Process{PID: 77, Unavailable: map[inspect.Field]string{}}
	p.MarkUnavailable(inspect.FieldName, "permission denied")
	p.MarkUnavailable(inspect.FieldUser, "process exited")
	p.MarkUnavailable(inspect.FieldCommand, "permission denied")
	p.MarkUnavailable(inspect.FieldWorkingDir, "permission denied")
	return inspect.Owner{Process: p, Sockets: []inspect.Socket{tcpSocket(inspect.IPv4, "*")}}
}

func boundTCPOwner() inspect.Owner {
	o := nodeOwner()
	o.Sockets = []inspect.Socket{
		{Protocol: inspect.TCP, Family: inspect.IPv4, Address: "127.0.0.1", Port: 3000, State: inspect.StateBound},
		{Protocol: inspect.TCP, Family: inspect.IPv6, Address: "fe80::1%lo0", Port: 3000, State: inspect.StateBound},
	}
	return o
}

func deviceBoundOwner() inspect.Owner {
	o := nodeOwner()
	o.Sockets = []inspect.Socket{tcpSocket(inspect.IPv4, "*%eth0")}
	return o
}

func chromeOwner() inspect.Owner {
	o := mdnsOwner(43947, "Google Chrome Helper", chromeCommand)
	o.Sockets = []inspect.Socket{tcpSocket(inspect.IPv4, "127.0.0.1")}
	return o
}

// stop is the stop line of the default view on this platform.
func stop(pid int) string {
	return "  stop: " + stopHint(pid) + "\n"
}

// textCase is one golden rendering of a result.
type textCase struct {
	name   string
	q      inspect.Query
	owners []inspect.Owner
	// width is the view width; 0 means defaultWidth.
	width int
	want  string
}

func runTextCases(t *testing.T, tests []textCase, render func(textView, inspect.Query, []inspect.Owner) string) {
	t.Helper()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			width := tt.width
			if width == 0 {
				width = defaultWidth
			}
			got := render(newTextView(width, false), tt.q, tt.owners)

			if got != tt.want {
				t.Errorf("output mismatch\n got:\n%s\nwant:\n%s", got, tt.want)
			}
			for _, line := range strings.Split(strings.TrimSuffix(got, "\n"), "\n") {
				if strings.HasSuffix(line, " ") {
					t.Errorf("line has trailing spaces: %q", line)
				}
			}
		})
	}
}

func TestTextView_Compact(t *testing.T) {
	setPrivileged(t, true)
	port3000 := inspect.Query{Port: 3000}

	runTextCases(t, []textCase{
		{
			name:   "single owner",
			q:      port3000,
			owners: []inspect.Owner{nodeOwner()},
			want: "3000/tcp  node  (PID 48213)\n" +
				"  127.0.0.1:3000   listening   loopback only\n" +
				"  node server.js\n" +
				stop(48213),
		},
		{
			name:   "several owners",
			q:      inspect.Query{Port: 5353, Protocol: inspect.UDP},
			owners: mdnsOwners(),
			want: `5353/udp  3 processes
  mDNSResponder          PID 647    *:5353 (v4+v6)   all interfaces
  Codex (Service)        PID 19212  *:5353 (v4+v6)   all interfaces
  Google Chrome Helper   PID 43947  *:5353 (v4+v6)   all interfaces
`,
		},
		{
			name:   "several owners with different bindings",
			q:      port3000,
			owners: []inspect.Owner{nodeOwner(), pythonOwner(), mixedOwner()},
			want: `3000/tcp+udp  3 processes
  node      PID 48213  tcp 127.0.0.1:3000                       loopback only
  python3   PID 500    tcp *:3000                               all interfaces
  node      PID 48213  tcp 127.0.0.1:3000, udp 192.168.1.5:3…   loopback only, interface 192.168.1.5
`,
		},
		{
			name:   "mixed tcp and udp",
			q:      port3000,
			owners: []inspect.Owner{mixedOwner()},
			want: "3000/tcp+udp  node  (PID 48213)\n" +
				"  tcp 127.0.0.1:3000     listening   loopback only\n" +
				"  udp 192.168.1.5:3000   bound       interface 192.168.1.5\n" +
				"  tcp [::1]:3000         listening   loopback only\n" +
				"  node server.js\n" +
				stop(48213),
		},
		{
			name:   "ipv4 and ipv6 collapse",
			q:      port3000,
			owners: []inspect.Owner{dualStackOwner()},
			want: "3000/tcp  node  (PID 48213)\n" +
				"  *:3000 (v4+v6)   listening   all interfaces\n" +
				"  node server.js\n" +
				stop(48213),
		},
		{
			name:   "different addresses of both families stay apart",
			q:      port3000,
			owners: []inspect.Owner{boundTCPOwner()},
			want: "3000/tcp  node  (PID 48213)\n" +
				"  127.0.0.1:3000       bound, not listening   loopback only\n" +
				"  [fe80::1%lo0]:3000   bound, not listening   interface fe80::1%lo0\n" +
				"  node server.js\n" +
				stop(48213),
		},
		{
			name:   "wildcard bound to a device",
			q:      port3000,
			owners: []inspect.Owner{deviceBoundOwner()},
			want: "3000/tcp  node  (PID 48213)\n" +
				"  *%eth0:3000   listening   interface eth0\n" +
				"  node server.js\n" +
				stop(48213),
		},
		{
			name:   "unknown owner",
			q:      port3000,
			owners: []inspect.Owner{unknownOwner()},
			want: `3000/tcp  unknown process
  127.0.0.1:3000   listening   loopback only
some owners unreadable; run with sudo
`,
		},
		{
			name:   "known and unknown owners",
			q:      port3000,
			owners: []inspect.Owner{nodeOwner(), unknownOwner()},
			want: `3000/tcp  2 processes
  node              PID 48213  127.0.0.1:3000   loopback only
  unknown process   -          127.0.0.1:3000   loopback only
some owners unreadable; run with sudo
`,
		},
		{
			name:   "unavailable fields",
			q:      port3000,
			owners: []inspect.Owner{restrictedOwner()},
			want: "3000/tcp  name unavailable  (PID 77)\n" +
				"  *:3000   listening   all interfaces\n" +
				"  command unavailable (permission denied)\n" +
				stop(77),
		},
		{
			name:   "long command is shortened and cut",
			q:      port3000,
			owners: []inspect.Owner{chromeOwner()},
			want: "3000/tcp  Google Chrome Helper  (PID 43947)\n" +
				"  127.0.0.1:3000   listening   loopback only\n" +
				"  Google Chrome Helper --type=utility --utility-sub-type=network.mojom.NetworkService --lang=en-US…\n" +
				stop(43947),
		},
		{
			name:   "narrow single owner",
			q:      port3000,
			owners: []inspect.Owner{chromeOwner()},
			width:  40,
			want: "3000/tcp  Google Chrome Helper  (PID 43…\n" +
				"  127.0.0.1:3000   listening   loopback…\n" +
				"  Google Chrome Helper --type=utility -…\n" +
				stop(43947),
		},
		{
			name:   "narrow several owners",
			q:      inspect.Query{Port: 5353, Protocol: inspect.UDP},
			owners: mdnsOwners(),
			width:  40,
			want: `5353/udp  3 processes
  mDNSResponder          PID 647    *:5…
  Codex (Service)        PID 19212  *:5…
  Google Chrome Helper   PID 43947  *:5…
`,
		},
		{
			name: "narrower than the minimum uses the minimum",
			q:    port3000,
			owners: []inspect.Owner{
				chromeOwner(),
			},
			width: 10,
			want: "3000/tcp  Google Chrome Helper  (PID 43…\n" +
				"  127.0.0.1:3000   listening   loopback…\n" +
				"  Google Chrome Helper --type=utility -…\n" +
				stop(43947),
		},
		{
			name: "no match",
			q:    port3000,
			want: "no listening or bound socket on 3000 (tcp or udp)\n",
		},
		{
			name: "no match with protocol",
			q:    inspect.Query{Port: 53, Protocol: inspect.UDP},
			want: "no listening or bound socket on 53 (udp)\n",
		},
	}, textView.compact)
}

func TestTextView_Detail(t *testing.T) {
	setPrivileged(t, true)
	port3000 := inspect.Query{Port: 3000}

	runTextCases(t, []textCase{
		{
			name:   "single owner",
			q:      port3000,
			owners: []inspect.Owner{nodeOwner()},
			want: `3000/tcp  node  (PID 48213)

  Sockets
    127.0.0.1:3000   IPv4   listening   loopback only (this machine only)

  Process
    user          kaanemec
    command       node server.js
    working dir   /Users/kaanemec/app

  Stop
    ` + stopHint(48213) + stopPad(48213) + `or: portpeek 3000 --stop
`,
		},
		{
			name:   "several owners",
			q:      inspect.Query{Port: 5353, Protocol: inspect.UDP},
			owners: mdnsOwners()[:2],
			want: `5353/udp  2 processes

5353/udp  mDNSResponder  (PID 647)

  Sockets
    *:5353   IPv4+IPv6   bound   all interfaces (every interface, firewall not checked)

  Process
    user          _mdnsresponder
    command       /usr/sbin/mDNSResponder
    working dir   /

  Stop
    ` + stopHint(647) + stopPad(647) + `or: portpeek 5353 --stop --pid 647 --udp

5353/udp  Codex (Service)  (PID 19212)

  Sockets
    *:5353   IPv4+IPv6   bound   all interfaces (every interface, firewall not checked)

  Process
    user          kaanemec
    command       /Applications/ChatGPT.app/Contents/MacOS/Codex (Service) --type=utility
    working dir   /

  Stop
    ` + stopHint(19212) + stopPad(19212) + `or: portpeek 5353 --stop --pid 19212 --udp
`,
		},
		{
			name:   "mixed tcp and udp",
			q:      port3000,
			owners: []inspect.Owner{mixedOwner()},
			want: `3000/tcp+udp  node  (PID 48213)

  Sockets
    tcp 127.0.0.1:3000     IPv4   listening   loopback only (this machine only)
    udp 192.168.1.5:3000   IPv4   bound       interface 192.168.1.5 (that address only, firewall not checked)
    tcp [::1]:3000         IPv6   listening   loopback only (this machine only)

  Process
    user          kaanemec
    command       node server.js
    working dir   /Users/kaanemec/app

  Stop
    ` + stopHint(48213) + stopPad(48213) + `or: portpeek 3000 --stop
`,
		},
		{
			name:   "unknown owner",
			q:      port3000,
			owners: []inspect.Owner{unknownOwner()},
			want: `3000/tcp  unknown process

  Sockets
    127.0.0.1:3000   IPv4   listening   loopback only (this machine only)

  Process
    name          unavailable (not readable without elevated privileges)
    user          unavailable (not readable without elevated privileges)
    command       unavailable (not readable without elevated privileges)
    working dir   unavailable (not readable without elevated privileges)

  some owners unreadable; run with sudo
`,
		},
		{
			name:   "unavailable fields",
			q:      inspect.Query{Port: 3000, Protocol: inspect.TCP},
			owners: []inspect.Owner{restrictedOwner()},
			want: `3000/tcp  name unavailable  (PID 77)

  Sockets
    *:3000   IPv4   listening   all interfaces (every interface, firewall not checked)

  Process
    name          unavailable (permission denied)
    user          unavailable (process exited)
    command       unavailable (permission denied)
    working dir   unavailable (permission denied)

  Stop
    ` + stopHint(77) + stopPad(77) + `or: portpeek 3000 --stop --tcp
`,
		},
		{
			name:   "long command is printed in full at any width",
			q:      port3000,
			owners: []inspect.Owner{chromeOwner()},
			width:  40,
			want: `3000/tcp  Google Chrome Helper  (PID 43947)

  Sockets
    127.0.0.1:3000   IPv4   listening   loopback only (this machine only)

  Process
    user          kaanemec
    command       ` + chromeCommand + `
    working dir   /

  Stop
    ` + stopHint(43947) + stopPad(43947) + `or: portpeek 3000 --stop
`,
		},
		{
			name: "no match",
			q:    port3000,
			want: "no listening or bound socket on 3000 (tcp or udp)\n",
		},
	}, textView.detail)
}

// stopPad is the space between the stop hint and its alternative.
func stopPad(pid int) string {
	return strings.Repeat(" ", max(stopColumn-len(stopHint(pid)), columnGap))
}

func TestTextView_HiddenSocketsHint(t *testing.T) {
	setPrivileged(t, false)
	q := inspect.Query{Port: 3000}
	view := newTextView(defaultWidth, false)

	tests := []struct {
		name string
		got  string
		want string
	}{
		{
			name: "no match",
			got:  view.compact(q, nil),
			want: "no listening or bound socket on 3000 (tcp or udp)\n" +
				"other users' sockets hidden; run with sudo\n",
		},
		{
			name: "default view",
			got:  view.compact(q, []inspect.Owner{nodeOwner()}),
			want: "3000/tcp  node  (PID 48213)\n" +
				"  127.0.0.1:3000   listening   loopback only\n" +
				"  node server.js\n" +
				stop(48213) +
				"other users' sockets hidden; run with sudo\n",
		},
		{
			name: "unknown owner hint wins",
			got:  view.compact(q, []inspect.Owner{unknownOwner()}),
			want: "3000/tcp  unknown process\n" +
				"  127.0.0.1:3000   listening   loopback only\n" +
				"some owners unreadable; run with sudo\n",
		},
		{
			name: "detail view ends with the hint",
			got:  view.detail(q, []inspect.Owner{nodeOwner()}),
			want: view.unlimited().detailBody(q) + "\n  other users' sockets hidden; run with sudo\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("output mismatch\n got:\n%s\nwant:\n%s", tt.got, tt.want)
			}
		})
	}
}

// detailBody is the single node owner's detail block followed by a blank
// line, as it precedes a hint.
func (v textView) detailBody(q inspect.Query) string {
	return strings.Join(v.detailOwner(q, nodeOwner(), false), "\n") + "\n"
}

var ansiEscape = regexp.MustCompile(`\x1b\[[0-9;]*m`)

func TestTextView_Styled(t *testing.T) {
	setPrivileged(t, false)
	q := inspect.Query{Port: 3000}
	owners := []inspect.Owner{nodeOwner()}
	styled := newTextView(defaultWidth, true)
	plainText := newTextView(defaultWidth, false)

	tests := []struct {
		name   string
		styled string
		plain  string
	}{
		{name: "default view", styled: styled.compact(q, owners), plain: plainText.compact(q, owners)},
		{name: "detail view", styled: styled.detail(q, owners), plain: plainText.detail(q, owners)},
		{
			name:   "several owners",
			styled: styled.compact(q, []inspect.Owner{nodeOwner(), pythonOwner()}),
			plain:  plainText.compact(q, []inspect.Owner{nodeOwner(), pythonOwner()}),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if !ansiEscape.MatchString(tt.styled) {
				t.Errorf("styled output has no escape codes:\n%s", tt.styled)
			}
			if got := ansiEscape.ReplaceAllString(tt.styled, ""); got != tt.plain {
				t.Errorf("styled output without escapes differs from plain\n got:\n%s\nwant:\n%s", got, tt.plain)
			}
		})
	}
}

func TestStdoutView(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		width int
		want  textView
	}{
		{name: "piped output is plain at the default width", want: textView{width: defaultWidth}},
		{name: "width override", width: 60, want: textView{width: 60}},
		{name: "override below the minimum", width: 10, want: textView{width: minWidth}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var b bytes.Buffer
			if got := stdoutView(stdio{stdout: &b, width: tt.width}); got != tt.want {
				t.Errorf("view = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestStylingAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		env  map[string]string
		want bool
	}{
		{name: "plain terminal", env: map[string]string{"TERM": "xterm-256color"}, want: true},
		{name: "no TERM", env: map[string]string{}, want: true},
		{name: "NO_COLOR set", env: map[string]string{"TERM": "xterm", "NO_COLOR": "1"}, want: false},
		{name: "NO_COLOR empty is unset", env: map[string]string{"TERM": "xterm", "NO_COLOR": ""}, want: true},
		{name: "dumb terminal", env: map[string]string{"TERM": "dumb"}, want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			getenv := func(key string) string { return tt.env[key] }
			if got := StylingAllowed(getenv); got != tt.want {
				t.Errorf("StylingAllowed = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRenderText_IsPlainDefaultView(t *testing.T) {
	setPrivileged(t, true)
	q := inspect.Query{Port: 5353, Protocol: inspect.UDP}
	owners := mdnsOwners()

	if got, want := RenderText(q, owners), newTextView(defaultWidth, false).compact(q, owners); got != want {
		t.Errorf("RenderText differs from the default view\n got:\n%s\nwant:\n%s", got, want)
	}
	if got, want := RenderDetail(q, owners), newTextView(defaultWidth, false).detail(q, owners); got != want {
		t.Errorf("RenderDetail differs from the detail view\n got:\n%s\nwant:\n%s", got, want)
	}
}

func TestEscapeControls(t *testing.T) {
	t.Parallel()

	tests := []struct{ in, want string }{
		{in: "plain name", want: "plain name"},
		{in: "名前", want: "名前"},
		{in: "a\tb", want: `a\tb`},
		{in: "x\x1b[31my", want: `x\x1b[31my`},
		{in: "nl\n", want: `nl\n`},
		{in: "c1\u0085", want: `c1\u0085`},
	}
	for _, tt := range tests {
		if got := EscapeControls(tt.in); got != tt.want {
			t.Errorf("EscapeControls(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}

// TestTextView_EscapesProcessText checks that control characters from a
// process never reach the terminal: names, users, commands and directories
// are shown escaped in every text view.
func TestTextView_EscapesProcessText(t *testing.T) {
	setPrivileged(t, true)
	q := inspect.Query{Port: 3000}
	o := nodeOwner()
	o.Process.Name = "no\x1b]0;pwned\x07de"
	o.Process.User = "us\ter"
	o.Process.Command = "/bin/no\x1b]0;pwned\x07de --flag\x1b[2J"
	o.Process.WorkingDir = "/tmp/\r"
	other := pythonOwner()
	other.Process.Name = "py\x1b[31m"

	v := newTextView(defaultWidth, false)
	views := map[string]string{
		"compact":       v.compact(q, []inspect.Owner{o}),
		"detail":        v.detail(q, []inspect.Owner{o}),
		"several":       v.compact(q, []inspect.Owner{o, other}),
		"stop headline": headlineProcess(o.Process),
	}
	for name, text := range views {
		if strings.ContainsFunc(text, func(r rune) bool { return r != '\n' && unicode.IsControl(r) }) {
			t.Errorf("%s: a control character reached the output: %q", name, text)
		}
		if !strings.Contains(text, `no\x1b]0;pwned\a`) {
			t.Errorf("%s: escaped name missing: %q", name, text)
		}
	}
	for _, want := range []string{`us\ter`, `--flag\x1b[2J`, `/tmp/\r`} {
		if !strings.Contains(views["detail"], want) {
			t.Errorf("detail lacks escaped %q:\n%s", want, views["detail"])
		}
	}
	if !strings.Contains(views["several"], `py\x1b[31m`) {
		t.Errorf("several-owner table lacks the escaped name:\n%s", views["several"])
	}
}
