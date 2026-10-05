# Port Peek architecture

Port Peek answers "what is using this port?" on the local machine. This document is the
working map of the system: components, decisions, and the roadmap state. Keep it short;
update it when a decision changes.

## System shape

```
cmd/portpeek            thin main: wires the platform inspector into the CLI
internal/cli            argument parsing, text/JSON rendering, exit codes
internal/inspect        shared model: Query, Result, Owner, Socket, Process, Error kinds,
                        Inspector interface, exposure classification
internal/inspect/lsof   macOS adapter: runs `lsof`, parses -F output, enriches with `ps`
internal/inspect/ss     Linux adapter: runs `ss`, parses rows, enriches from /proc
internal/inspect/netstat Windows adapter: `netstat -ano` per protocol, PowerShell Win32_Process
cmd/portpeek/platform_* build-tagged selection of the default Inspector per OS
internal/tui            `portpeek tui`: Bubble Tea table over `Lister`, details via `Inspector`,
                        stop via cli.StopVerified. cli does not import tui: main injects
                        tui.Run through cli.Deps to avoid the cycle (tui uses cli's renderer)
```

Data flow: `cli` builds a `Query` → platform `Inspector.Inspect` → `Result` → `cli` renders.
OS commands never leak past the adapter package; every adapter exposes a `Runner`
seam so parsers are tested against recorded fixtures, not live sockets.

## Shared model (internal/inspect)

- `Query{Port, Protocol}`: port 1–65535, protocol `tcp`, `udp`, or empty for both.
- `Result{Query, Owners}`: zero owners means "no matching socket" and is not an error.
- `Owner{Process, Sockets}`: one process may bind several sockets on the port
  (IPv4 + IPv6, TCP + UDP). Several owners may share a port (SO_REUSEPORT, forks).
- `Socket{Protocol, Family, Address, Port, State}`: `Address` is the bound local
  address as reported (`127.0.0.1`, `*`, `::1`, `fe80::1%lo0`). `State` is `LISTEN`
  or `BOUND` (UDP, and TCP bound without listen). `Exposure()` derives loopback /
  all-interfaces / specific-interface from the address. No claims about firewalls.
- `Process{PID, Name, User, Command, WorkingDir}` with `Unavailable map[Field]string`
  recording why a field could not be read (permission, process exited, tool limit).
- `Lister.List` returns a `Snapshot{Taken, Owners}` of every listening TCP and bound UDP
  socket, cheaply: PID/name/user from the listing only, no per-process enrichment. The
  TUI lists with it and calls `Inspect` on one port for details.
- `Error{Kind, Op, Err}` with kinds `ToolMissing`, `PermissionDenied`, `CommandFailed`,
  `Unsupported`. The CLI maps kinds to messages and exit codes; adapters never print.
  With `--json`, inspection errors go to stdout as `{"schema":1,"error":{kind,message}}`;
  usage errors and interrupts stay plain text on stderr. Contract: docs/json.md.

Ownership rule: a process owns a port through any socket whose local port matches and
that has no peer: TCP in LISTEN (reported `LISTEN`) or TCP bound without listen and
UDP (reported `BOUND`). Sockets with a peer (established connections, connected UDP)
are never owners, because they do not block the port.

Visibility: without root, macOS `lsof` silently omits other users' sockets, while
Linux `ss` lists them without a process. The adapter then returns an owner with
`PID 0` and every field marked unavailable; the CLI renders it as "an unknown
process", never prints a stop command for it, and refuses `--stop`. An answer is
`"complete": false` when unprivileged or when any owner is unknown, and text carries
a matching hint line. "No listening or bound socket" never claims the port is free.

## CLI contract (internal/cli)

```
portpeek <port> [--tcp|--udp] [--detail|--json]
portpeek <port> --stop [--pid N] [--force]
portpeek tui [--interval 5s]
```

Exit codes: `0` at least one owner (with `--stop`: SIGTERM sent), `1` no matching
socket, `2` invalid input/usage, `3` inspection failed (tool missing, permission
denied, command error), `4` stop not performed (declined, ambiguous, stale identity,
signal failed), `130` interrupted.
Default text is compact: a `port/proto  name  (PID n)` headline, one binding line per
address (families collapsed to `(v4+v6)`), the command shortened to the terminal width,
and a `stop:` hint the tool never runs itself; several owners become one aligned row
each. `--detail` prints one compact block per owner: `name  PID n  user u`, one line
per socket, `cmd` shortened to one line with a `(+N args)` count (full command in JSON),
`cwd`, and `stop` with both the kill and the portpeek command; the port is stated once. Unavailable fields print the reason, never a guess. Hidden-socket and
unknown-owner hints are one short trailing line. Styling (bold, dim) only on a
terminal with `NO_COLOR` unset and `TERM != dumb`; width from the terminal, else 100.
`RenderText`/`RenderDetail` are exported for the TUI, always plain at width 100.
The TUI has a 256-color theme (`internal/tui/theme.go`, screenshots in `docs/tui*.png`):
exposure colored by risk (green loopback, amber all interfaces, blue interface, gray
unknown), tcp blue / udp magenta tags, teal accent for ports, selection bar, title and
footer, red bar for the stop prompt. Light/dark variants chosen from the terminal's
background; fully plain under NO_COLOR, TERM=dumb or no terminal. Process text is
control-character escaped in every text view. JSON (`--json`) is versioned
(`"schema": 1`) and stable from 1.0; multiple owners and unavailable fields are explicit.

## Key decisions

| Decision | Choice | Why |
|---|---|---|
| Language / deps | Go, standard library plus `golang.org/x/term` (TTY detection, width); `charm.land/lipgloss/v2` for optional styling in `internal/cli`; Bubble Tea and Bubbles only in `internal/tui` | Single small binary, no runtime. A mode-bits check mistakes `/dev/null` for a terminal |
| macOS source | `lsof -nP -F pcnLTtfP0 -i :PORT` then `ps -o command=` and `lsof -d cwd` per PID | Machine-readable, present on every macOS, no entitlements |
| Windows source | `netstat -a -n -o -p {TCP,TCPv6,UDP,UDPv6}` then `Get-CimInstance Win32_Process` per PID via `powershell -NoProfile` | Present on every Windows, no admin needed for PIDs. Live test passed on windows-latest CI (2026-10-05). `Get-NetTCPConnection` is the locale-independent fallback if netstat's translated state words prove a problem |
| Linux source | `ss -H -a -n -p -t -u 'sport = :PORT'` then `/proc/PID/{comm,cmdline,cwd,status}` | iproute2 is ubiquitous; procfs needs no extra tool. `-H` needs iproute2 ≥ 4.13; wildcard `*` means dual-stack IPv6, `0.0.0.0`/`[::]`/`*` all stored as `*` |
| No-match detection | `lsof` exit 1 with empty stdout and no non-WARNING stderr = no match; otherwise failure | Observed behaviour; avoids false "nothing found" |
| Link-local IPv6 | `lsof` packs the scope index into the second group (`fe80:1::1`); adapter rewrites to `fe80::1%lo0` | Otherwise the shown address cannot be connected to |
| Testing | Parsers and renderers tested with recorded `lsof` fixtures; one live-socket test per adapter, skipped under `go test -short` | Deterministic CI, honest platform claims |
| Module path | `github.com/kaanemec/portpeek` | Matches the public GitHub repo |
| Platform selection | `//go:build` files in `cmd/portpeek/platform_*.go` choosing the default `Inspector` (adapters import the model, so selection sits above both) | Unsupported OS fails at runtime with a clear message, not silently |
| Stop action | `--stop`: print the normal result, pick one owner (`--pid` when several), confirm on a TTY or require `--force`, re-inspect and require same PID + name, send SIGTERM, wait 2s, report; never SIGKILL | Stale PID can never receive a signal unreviewed; `internal/cli/stop.go` |

## Roadmap state

| Version | Status | Notes |
|---|---|---|
| v0.1 macOS answer | done 2026-10-05 | model, CLI, lsof adapter, validation record in docs/validation.md |
| v0.2 safe control + Linux | done 2026-10-05 | `--stop` flow; `ss`/procfs adapter validated in Docker (golang:1.27, iproute2 6.15) and CI ubuntu runner |
| v1.0 cross-platform release | released in v1.2.0 (2026-10-05) | Windows live test green on CI; GoReleaser + release workflow ready; remote github.com/kaanemec/portpeek, MIT confirmed by owner 2026-10-05 |
| v1.2 readable output | done 2026-10-05 (1.2.2 compacts `--detail`) | compact default, `--detail` view, TUI details pane uses `RenderDetail` |
| v1.1 port TUI | done 2026-10-05 | `Lister` on all adapters; Bubble Tea table, details, refresh, search, stop from details; verified live on macOS |

Out of scope: remote scanning, Docker management, traffic measurement, history.
