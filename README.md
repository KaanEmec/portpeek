# Port Peek

Port Peek answers one question: **what is using this port?** Give it a port
number and it names the local process that owns it and says how the socket is
bound. Version 0.1 is a macOS command-line tool. Linux and Windows adapters and
a terminal overview come later (see the [roadmap](roadmap/README.md)).

## Install

From source (Go 1.27 or newer):

```
go install github.com/kaanemec/portpeek/cmd/portpeek@latest
```

or from a checkout:

```
go build -o portpeek ./cmd/portpeek
```

The module path is a placeholder until the repository has a public remote, so
until then use the checkout build. `lsof` and `ps` ship with macOS and are the
only runtime dependencies.

## Usage

```
Usage: portpeek <port> [--tcp|--udp] [--json]

Show which process is using a local port.

Options:
  --tcp       only look at TCP listeners
  --udp       only look at UDP sockets
  --json      print machine-readable JSON (schema 1)
  --version   print the version and exit
  -h, --help  show this help and exit

Exit codes:
  0  at least one process uses the port
  1  no process uses the port
  2  invalid input
  3  inspection failed (tool missing, permission denied, command error)
```

A process owns a port when it has a TCP socket in LISTEN state or a UDP socket
bound to it. Connections that merely talk to the port, such as a client
connected to a server, are not reported.

### Example: text

```
$ portpeek 47102
Port 47102/tcp is used by Python (PID 46292)
  Address:      *:47102 (IPv4, LISTEN)
  Exposure:     all interfaces — reachable from other machines on the network
  User:         kaanemec
  Command:      /Applications/Xcode.app/.../MacOS/Python listen.py 3 tcp4any:47102
  Working dir:  /Users/kaanemec/scratch
  Stop:         kill 46292
```

The `Stop:` line is a hint for you to run yourself. Port Peek never runs it.
Fields that could not be read print the reason instead, for example
`Command: unavailable (process exited)`.

### Example: JSON

```
$ portpeek 47109 --json
{
  "schema": 1,
  "query": { "port": 47109, "protocol": "" },
  "owners": [
    {
      "process": {
        "pid": 46397,
        "name": "Python",
        "user": "kaanemec",
        "command": "/Applications/Xcode.app/.../MacOS/Python listen.py 3 tcp4:47109",
        "working_dir": "/Users/kaanemec/scratch",
        "unavailable": {}
      },
      "sockets": [
        { "protocol": "tcp", "family": "ipv4", "address": "127.0.0.1",
          "port": 47109, "state": "LISTEN", "exposure": "loopback" }
      ]
    }
  ]
}
```

Paths are shortened and the `sockets` entry is put on fewer lines. When
nothing matches, `owners` is an empty list and the exit code is 1. Several
owners (for example two processes sharing a port with `SO_REUSEPORT`) appear as
several entries.

## Exit codes

- `0` at least one process uses the port
- `1` no matching socket
- `2` invalid input (bad or missing port, `--tcp` with `--udp`, unknown flag)
- `3` inspection failed (`lsof` missing, permission denied, command error)

## Permissions

Without `sudo`, `lsof` only sees sockets owned by your own user. A port held by
a root-owned service is reported as "no process", followed by a hint to try
`sudo`. To see everything:

```
sudo portpeek <port>
```

Port Peek only runs `lsof` and `ps`. It does not write files, send signals,
open network connections, or change anything on the machine.

## Limitations

- macOS only for now. On other systems the binary reports that the platform is
  unsupported.
- Without `sudo`, a root-owned port looks the same as an unused port: exit
  code 1, and in `--json` an empty `owners` list with no hint. Use `sudo` when
  a "no process" answer surprises you.
- `ps` renders newlines in a command as `\012`.
- Exposure comes from the bound address only (`127.0.0.1` and `::1` are
  loopback, `*` is all interfaces). It says nothing about firewalls or
  network configuration.
- A dual-stack `::` listener appears once, as IPv6 `*`.
- Commands are printed in full, so browser helper processes give long output.
- A process can exit between lookups; the unreadable fields are then marked
  unavailable.

## Design

Go, standard library only. CLI → shared socket/process model → OS adapter,
with OS commands kept inside the adapter so parsers are tested against recorded
`lsof` output. On macOS, `lsof` supplies ownership and working directory and
`ps` the full command line. No telemetry, no network service.

See [ARCHITECTURE.md](ARCHITECTURE.md) for the detailed map.

## Boundaries and release target

The 1.0 target is a documented, tested one-port CLI for macOS, Linux and
Windows. Version 1.1 adds a compact local-port overview on the same discovery
core. Remote scanning, Docker management, traffic measurement and historical
monitoring are out of scope. Platform support is earned through real adapter
tests, not inferred from compilation. A stop action is planned for 0.2 and will
require confirmation.

## Validation

[docs/validation.md](docs/validation.md) records the manual checks run on
macOS 26.6.2 (2026-10-05), with the real output and the problems found.
