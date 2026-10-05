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
internal/inspect/<os>   later: windows (PowerShell/API)
cmd/portpeek/platform_* build-tagged selection of the default Inspector per OS
internal/tui            v1.1: Bubble Tea overview, reuses inspect + cli formatting
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
- `Error{Kind, Op, Err}` with kinds `ToolMissing`, `PermissionDenied`, `CommandFailed`.
  The CLI maps kinds to messages and exit codes; adapters never print.

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
portpeek <port> [--tcp|--udp] [--json]
portpeek <port> --stop [--pid N] [--force]
```

Exit codes: `0` at least one owner (with `--stop`: SIGTERM sent), `1` no matching
socket, `2` invalid input/usage, `3` inspection failed (tool missing, permission
denied, command error), `4` stop not performed (declined, ambiguous, stale identity,
signal failed), `130` interrupted.
Text output lists each owner with labelled fields; unavailable fields print the reason,
never a guess. Text ends with a one-line exposure explanation and a manual stop hint
(`kill <pid>`) that the tool itself does not run. JSON (`--json`) is versioned
(`"schema": 1`) and stable from 1.0; multiple owners and unavailable fields are explicit.

## Key decisions

| Decision | Choice | Why |
|---|---|---|
| Language / deps | Go, standard library plus `golang.org/x/term` (TTY detection for `--stop`); Charm libs only in `internal/tui` | Single small binary, no runtime. A mode-bits check mistakes `/dev/null` for a terminal |
| macOS source | `lsof -nP -F pcnLTtfP0 -i :PORT` then `ps -o command=` and `lsof -d cwd` per PID | Machine-readable, present on every macOS, no entitlements |
| Linux source | `ss -H -a -n -p -t -u 'sport = :PORT'` then `/proc/PID/{comm,cmdline,cwd,status}` | iproute2 is ubiquitous; procfs needs no extra tool. `-H` needs iproute2 ≥ 4.13; wildcard `*` means dual-stack IPv6, `0.0.0.0`/`[::]`/`*` all stored as `*` |
| No-match detection | `lsof` exit 1 with empty stdout and no non-WARNING stderr = no match; otherwise failure | Observed behaviour; avoids false "nothing found" |
| Link-local IPv6 | `lsof` packs the scope index into the second group (`fe80:1::1`); adapter rewrites to `fe80::1%lo0` | Otherwise the shown address cannot be connected to |
| Testing | Parsers and renderers tested with recorded `lsof` fixtures; one live-socket test per adapter, skipped under `go test -short` | Deterministic CI, honest platform claims |
| Module path | `github.com/kaanemec/portpeek` | Placeholder until a remote exists; rename is one `sed` |
| Platform selection | `//go:build` files in `cmd/portpeek/platform_*.go` choosing the default `Inspector` (adapters import the model, so selection sits above both) | Unsupported OS fails at runtime with a clear message, not silently |
| Stop action | `--stop`: print the normal result, pick one owner (`--pid` when several), confirm on a TTY or require `--force`, re-inspect and require same PID + name, send SIGTERM, wait 2s, report; never SIGKILL | Stale PID can never receive a signal unreviewed; `internal/cli/stop.go` |

## Roadmap state

| Version | Status | Notes |
|---|---|---|
| v0.1 macOS answer | done 2026-10-05 | model, CLI, lsof adapter, validation record in docs/validation.md |
| v0.2 safe control + Linux | done 2026-10-05 | `--stop` flow; `ss`/procfs adapter validated in Docker (golang:1.27, iproute2 6.15) and CI ubuntu runner |
| v1.0 cross-platform release | planned | Windows adapter, JSON freeze, GoReleaser, CI |
| v1.1 port TUI | planned | inventory API on adapters, Bubble Tea table + details |

Out of scope: remote scanning, Docker management, traffic measurement, history.
