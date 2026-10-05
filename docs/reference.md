# Port Peek reference

The full manual: install options, every flag, output formats, stop semantics,
exit codes, permissions per OS and known limitations. The short version is the
[README](../README.md); the JSON contract is [docs/json.md](json.md).

- [Install](#install)
- [Usage and options](#usage-and-options)
- [Output](#output)
- [Port overview: `portpeek`](#port-overview-portpeek)
- [Stopping a process](#stopping-a-process)
- [Exit codes](#exit-codes)
- [Permissions](#permissions)
- [Limitations](#limitations)
- [Design and boundaries](#design-and-boundaries)

## Install

### Install script (macOS, Linux)

```
curl -fsSL https://raw.githubusercontent.com/kaanemec/portpeek/main/install.sh | sh
```

[`install.sh`](../install.sh) is a POSIX `sh` script. It detects the OS
(`darwin` or `linux`) and architecture (`amd64` or `arm64`), asks the GitHub API
for the latest release tag, downloads the matching archive and `checksums.txt`
to a temporary directory, checks the archive's SHA-256 (with `sha256sum` or
`shasum -a 256`) and stops without installing anything on a mismatch. It then
copies the binary into place and prints `portpeek --version`. It uses `curl`,
or `wget` when `curl` is missing, and never runs `sudo`.

| Variable | Effect |
|---|---|
| `PORTPEEK_VERSION` | Release tag to install, for example `v1.2.2`. Default: the latest release. |
| `PORTPEEK_INSTALL_DIR` | Directory for the binary. Default: `/usr/local/bin` when writable, otherwise `$HOME/.local/bin` (created if needed). |

```
curl -fsSL https://raw.githubusercontent.com/kaanemec/portpeek/main/install.sh | PORTPEEK_VERSION=v1.3.0 sh
```

If the directory is not writable the script says so and exits; re-run it with
`sudo sh` or set `PORTPEEK_INSTALL_DIR`. If the directory is not on your
`PATH`, it prints the `export PATH=...` line to add. On other systems it prints
the Windows and `go install` instructions and exits 1.

### From source

Go 1.27 or newer, either of:

```
go install github.com/kaanemec/portpeek/cmd/portpeek@latest
go build -o portpeek ./cmd/portpeek        # from a checkout
```

Runtime dependencies: `lsof` and `ps` on macOS (both ship with it), `ss` from
iproute2 plus `/proc` on Linux, `netstat` and PowerShell on Windows.

### Verifying a release

Prebuilt archives for macOS (amd64, arm64), Linux (amd64, arm64) and Windows
(amd64) are attached to each
[GitHub Release](https://github.com/kaanemec/portpeek/releases), named
`portpeek_<version>_<os>_<arch>.tar.gz` (`.zip` on Windows). Download the
archive for your OS and architecture together with `checksums.txt`, then verify
and unpack:

```
shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf portpeek_<version>_<os>_<arch>.tar.gz     # .zip on Windows
```

(`--ignore-missing` is a GNU/BSD option that skips archives you did not
download; plain `-c` complains about them.) The binary is a single file with no
runtime dependencies beyond the OS tools listed above; put it anywhere on your
`PATH`.

## Usage and options

```
Usage: portpeek                       open the port overview (TUI)
       portpeek <port> [--tcp|--udp] [--detail|--json]
       portpeek <port> --stop [--pid <pid>] [--force] [--tcp|--udp]
       portpeek tui [--interval <duration>]   same as plain portpeek

Options:
  --tcp        only look at TCP sockets
  --udp        only look at UDP sockets
  --detail     show everything known: every socket, user, command,
               working directory and stop commands
  --json       print machine-readable JSON (schema 1); --detail is ignored
  --stop       send SIGTERM to the process using the port, after confirmation
  --pid <pid>  with --stop, the process to stop when several use the port
  --force      with --stop, skip confirmation (required when stdin is not a terminal)
  --version    print the version and exit
  -h, --help   show this help and exit

Overview options (portpeek, portpeek tui):
  --interval <duration>  auto-refresh period, e.g. 10s (default 5s, minimum 1s)
```

A process owns a port when it has a TCP socket in LISTEN state, a TCP socket
bound but not listening, or a UDP socket bound to the port. Connections that
merely talk to the port, such as a client connected to a server, are not
reported.

## Output

By default the answer is a few short lines; `--detail` shows everything Port
Peek knows. Text is cut to the terminal width (100 columns when piped) and is
bold and dim only on a terminal, never when piped or with `NO_COLOR` set or
`TERM=dumb`. Process names, users, commands and working directories have
control characters escaped.

### One process

```
$ portpeek 3000
3000/tcp  Python  (PID 13337)
  127.0.0.1:3000   listening   loopback only
  Python -m http.server 3000 --bind 127.0.0.1
  stop: kill 13337
other users' sockets hidden; run with sudo
```

One line per binding (address, state, exposure), the command with argv[0]
shortened to its base name, and `stop:`, a command for you to run; Port Peek
does not run it (see [Stopping a process](#stopping-a-process)). The last line
appears when the answer may be incomplete (see [Permissions](#permissions)).
State is `listening`, `bound` (UDP) or `bound, not listening` (TCP); exposure is
`loopback only`, `all interfaces`, `interface <address>` or `unknown`.

### Several processes

```
$ portpeek 5353
5353/udp  2 processes
  Codex (Service)        PID 19212  *:5353 (v4+v6)   all interfaces
  Google Chrome Helper   PID 43947  *:5353 (v4+v6)   all interfaces
other users' sockets hidden; run with sudo
```

One row per process. `(v4+v6)` means the same address is bound on IPv4 and
IPv6. Commands and stop lines are left out; `--detail` has them, and `--stop`
asks for `--pid` when several processes share the port.

### `--detail`

```
$ portpeek 3000 --detail
3000/tcp  Python  PID 13337  user kaanemec
  127.0.0.1:3000  v4  listening  loopback only
  cmd   Python -m http.server 3000 --bind 127.0.0.1
  cwd   /Users/kaanemec/Developer/Port-Peek
  stop  kill 13337  ·  portpeek 3000 --stop

other users' sockets hidden; run with sudo
```

The port is named once, then the owner with its PID and user, one line per
binding (`v4`, `v6` or `v4+v6`), and the `cmd`, `cwd` and `stop` lines. The
command is shortened to one line like the default view; arguments that do not
fit are dropped whole and counted, as in
`Codex (Service) --type=utility --utility-sub-type=network.mojom.NetworkService …  (+22 args)`.
The full command is in `--json`. With several processes the headline is
`5353/udp  2 processes` and each process gets its own block, separated by a
blank line, with `--pid` in its stop command. A field that could not be read
prints the reason, for example `cwd   unavailable (process exited)`.

### `--json`

The interpreter path in `command` is shortened to `...` here.

```
$ portpeek 3000 --json
{
  "schema": 1,
  "query": {
    "port": 3000,
    "protocol": ""
  },
  "complete": false,
  "owners": [
    {
      "process": {
        "pid": 13337,
        "name": "Python",
        "user": "kaanemec",
        "command": ".../MacOS/Python -m http.server 3000 --bind 127.0.0.1",
        "working_dir": "/Users/kaanemec/Developer/Port-Peek",
        "unavailable": {}
      },
      "sockets": [
        {
          "protocol": "tcp",
          "family": "ipv4",
          "address": "127.0.0.1",
          "port": 3000,
          "state": "LISTEN",
          "exposure": "loopback"
        }
      ]
    }
  ]
}
```

`complete` is `false` without root (other users' sockets may be missing) or
when any owner is unknown, and `true` otherwise. `"pid": 0` means the owner is
unknown: the socket was seen but its process could not be read (see
[Linux](#linux)). `state` is `LISTEN` for TCP listeners and `BOUND` for UDP
sockets and TCP sockets bound without listening. With no match, `owners` is
`[]` and the exit code is 1; several owners appear as several entries.

The full field reference, the error object and the compatibility policy are in
[docs/json.md](json.md).

## Port overview: `portpeek`

`portpeek` with no port, or the explicit alias `portpeek tui`, opens the
overview; `--interval 5s` sets the refresh. It lists every listening TCP and
bound UDP socket with its port, protocol, binding, process, PID and exposure,
and refreshes on the interval (minimum 1s). Keys: `↑/↓` move, `/` search by port or process,
`Esc` clear, `s` sort by port or process, `r` refresh, `p` pause, `Enter`
details, `q` quit. The details pane shows the same text as
`portpeek <port> --detail`; `k` there stops the shown process after a `y`
confirmation, using the same identity recheck and SIGTERM-only rule as
`--stop`. Browsing never changes anything.

The overview needs an interactive terminal. When stdin or stdout is not one
(`portpeek </dev/null`, `portpeek | cat`, a script that forgot the port),
plain `portpeek` prints
`portpeek: no port given and no interactive terminal; usage: portpeek <port>`
and exits 2 instead of starting it. A one-port flag without a port, such as
`portpeek --json`, is also a usage error (`--json requires a port`). The table
respects `NO_COLOR`, hides columns below 80 columns, and shows the
hidden-sockets hint when it applies. "Usage" here means which process owns a
port; the table measures no traffic.

Exposure is colored by risk (green loopback, amber all interfaces, blue
interface, gray unknown), with light and dark variants chosen from the
terminal background. Screenshots: [table](tui.png),
[details](tui-details.png), [light terminal](tui-light.png).

## Stopping a process

`portpeek <port> --stop` prints the default result, then sends SIGTERM to the
owner. When several processes use the port, choose one with `--pid <pid>`;
Port Peek never picks for you. On a terminal it asks
`Send SIGTERM to node (PID 48213)? [y/N]` and only `y` or `yes` proceeds.
Without a terminal (scripts, pipes) it refuses unless `--force` is given.

Right before signalling it inspects the port again and stops nothing if the
PID no longer owns the port or its name changed. It sends SIGTERM only, waits
up to 2 seconds, and reports whether the process exited; if not, it prints
`kill -9 <pid>` for you to run and never sends SIGKILL itself. An unknown
owner (`"pid": 0`) is never signalled. `--stop` cannot be combined with
`--json`. Every case where nothing was signalled exits 4. On Windows `--stop`
is not supported yet (see [Windows](#windows)).

## Exit codes

- `0` at least one process uses the port (with `--stop`: SIGTERM was sent);
  `1` no listening or bound socket
- `2` invalid input (bad port, `--tcp` with `--udp`, unknown flag, a one-port
  flag without a port, no port and no interactive terminal)
- `3` inspection failed (`lsof` or `ss` missing, permission denied, command error)
- `4` `--stop` stopped nothing (declined, no `--force` without a terminal,
  several owners and no `--pid`, unknown owner, process changed, signal failed)
- `130` interrupted (Ctrl-C)

## Permissions

On macOS without `sudo`, `lsof` only sees your own user's sockets, so a port
held by a root-owned service is reported as no socket (exit 1). To flag this, a
non-root run ends every text result with
`other users' sockets hidden; run with sudo`
and sets `"complete": false` in JSON. To see everything:

```
sudo portpeek <port>
```

Without `--stop`, Port Peek only runs `lsof` and `ps` (macOS) or `ss` and
reads `/proc` (Linux). It does not write files, send signals, open network
connections, or change anything on the machine.

### Linux

Discovery runs `ss -H -a -n -p -t -u 'sport = :PORT'` and reads name, command,
user and working directory from `/proc/PID`. Without root, `ss` lists other
users' sockets but not their owners, so such a socket shows as
`unknown process` with no PID and no stop line (`--detail` prints each field as
`unavailable (not readable without elevated privileges)`), `"pid": 0` and
`"complete": false`; the text then ends with
`some owners unreadable; run with sudo`.
Root inside a container may still lack `CAP_SYS_PTRACE` and see unknown owners.
`ss -H` needs iproute2 4.13 or newer. A socket bound to one device prints as
`*%eth0` and is reported as a specific interface.

### Windows

Discovery runs `netstat -a -n -o -p <TCP|TCPv6|UDP|UDPv6>` and reads the process
name and command line through PowerShell (`Get-CimInstance Win32_Process`).
The adapter's live test runs on the Windows CI job (first green run 2026-10-05);
it has not yet been exercised by a person on a Windows desktop. Known limits:
other users' command lines need an Administrator shell (the executable path is
shown instead, or the field is unavailable); user and working directory are
always unavailable; `--stop` is not supported, the stop line is
`taskkill /PID <pid>`; `netstat` translates state words on non-English Windows,
and translated rows are dropped rather than guessed, so a localized system may
report no owner for a port that is in use.

## Limitations

- macOS and Linux are checked by hand on real sockets; Windows only by its CI live test.
- On macOS without `sudo`, a root-owned port exits 1, like an unused port. Check
  the hint or `"complete": false`, or run with `sudo`.
- On macOS, `ps` renders newlines in a command as `\012`.
- Exposure comes from the bound address only (`127.0.0.1` and `::1` are
  loopback, `*` is all interfaces). It says nothing about firewalls.
- A dual-stack `::` listener appears once, as IPv6 `*`.
- Link-local IPv6 prints as `[fe80::1%lo0]`; `lsof` shows `[fe80:1::1]`.
- Text output shortens commands to one line, including `--detail`; the full
  command line is only in `--json`.
- A process can exit between lookups; its unreadable fields show as unavailable.

## Design and boundaries

Go, standard library plus `golang.org/x/term` and Charm's Lip Gloss (text
styling) and Bubble Tea (the `tui` table). CLI → shared model → OS adapter; OS
commands stay inside the adapter and parsers are tested against recorded
`lsof` and `ss` output. No telemetry, no network service. See
[ARCHITECTURE.md](../ARCHITECTURE.md).

The 1.0 target is a tested one-port CLI for macOS, Linux and Windows; 1.1 adds
a local-port overview. Remote scanning, Docker management, traffic measurement
and history are out of scope.

Manual checks (macOS 26.6.2, 2026-10-05) with real output and the problems
found are in [docs/validation.md](validation.md).
