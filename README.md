# Port Peek

Port Peek answers one question: **what is using this port?** Give it a port
number and it names the local process that owns it and how the socket is bound.
Version 0.1 is a macOS command-line tool; Linux, Windows and a terminal
overview come later (see the [roadmap](roadmap/README.md)).

## Install

From source (Go 1.27 or newer), either of:

```
go install github.com/kaanemec/portpeek/cmd/portpeek@latest
go build -o portpeek ./cmd/portpeek        # from a checkout
```

The module path is a placeholder until there is a public remote, so use the
checkout build for now. `lsof` and `ps` ship with macOS and are the only
runtime dependencies.

## Usage

```
Usage: portpeek <port> [--tcp|--udp] [--json]

Show which process is using a local port.

Options:
  --tcp       only look at TCP sockets
  --udp       only look at UDP sockets
  --json      print machine-readable JSON (schema 1)
  --version   print the version and exit
  -h, --help  show this help and exit

Exit codes:
  0  at least one process uses the port
  1  no listening or bound socket on the port
  2  invalid input
  3  inspection failed (tool missing, permission denied, command error)
130  interrupted
```

A process owns a port when it has a TCP socket in LISTEN state, a TCP socket
bound but not listening, or a UDP socket bound to the port. Connections that
merely talk to the port, such as a client connected to a server, are not
reported.

### Example: text

```
$ portpeek 47121
Port 47121/tcp is used by Python (PID 53011)
  Address:      *:47121 (IPv4, LISTEN)
  Exposure:     all interfaces — accepts connections on every network interface (firewall not checked)
  User:         kaanemec
  Command:      .../MacOS/Python listen.py 3 tcp4any:47121
  Working dir:  /private/tmp/claude-502/-Users-kaanemec-Developer-Port-Peek/12550c7c-4473-4f32-9f98-3fb1e1181341/scratchpad
  Stop:         kill 53011

Sockets owned by other users are not visible without elevated privileges (try sudo).
```

(`Command` is shortened with `...`; the rest is real output.) `Stop:` is a hint
for you to run; Port Peek never runs it. A field that could not be read prints
the reason, for example `Command: unavailable (process exited)`.

### Example: JSON

```
$ portpeek 47120 --json
{
  "schema": 1,
  "query": {
    "port": 47120,
    "protocol": ""
  },
  "complete": false,
  "owners": [
    {
      "process": {
        "pid": 52998,
        "name": "Python",
        "user": "kaanemec",
        "command": ".../MacOS/Python listen.py 3 tcp4:47120",
        "working_dir": "/private/tmp/claude-502/-Users-kaanemec-Developer-Port-Peek/12550c7c-4473-4f32-9f98-3fb1e1181341/scratchpad",
        "unavailable": {}
      },
      "sockets": [
        {
          "protocol": "tcp",
          "family": "ipv4",
          "address": "127.0.0.1",
          "port": 47120,
          "state": "LISTEN",
          "exposure": "loopback"
        }
      ]
    }
  ]
}
```

`complete` is `false` without root (other users' sockets may be missing) and
`true` as root. `state` is `LISTEN` for TCP listeners and `BOUND` for UDP
sockets and TCP sockets bound without listening. With no match, `owners` is
`[]` and the exit code is 1; several owners appear as several entries.

## Exit codes

- `0` at least one process uses the port; `1` no listening or bound socket
- `2` invalid input (bad or missing port, `--tcp` with `--udp`, unknown flag)
- `3` inspection failed (`lsof` missing, permission denied, command error)
- `130` interrupted (Ctrl-C)

## Permissions

Without `sudo`, `lsof` only sees your own user's sockets, so a port held by a
root-owned service is reported as no socket (exit 1). To flag this, a non-root
run ends every text result with
`Sockets owned by other users are not visible without elevated privileges (try sudo).`
and sets `"complete": false` in JSON. To see everything:

```
sudo portpeek <port>
```

Port Peek only runs `lsof` and `ps`. It does not write files, send signals,
open network connections, or change anything on the machine.

## Limitations

- macOS only for now; other systems report the platform as unsupported.
- Without `sudo`, a root-owned port still exits 1, like an unused port. Check
  the hint or `"complete": false`, or run with `sudo`.
- `ps` renders newlines in a command as `\012`.
- Exposure comes from the bound address only (`127.0.0.1` and `::1` are
  loopback, `*` is all interfaces). It says nothing about firewalls.
- A dual-stack `::` listener appears once, as IPv6 `*`.
- Link-local IPv6 prints as `[fe80::1%lo0]`; `lsof` shows `[fe80:1::1]`.
- Commands are printed in full, so browser helpers give long output.
- A process can exit between lookups; its unreadable fields show as unavailable.

## Design and boundaries

Go, standard library only. CLI → shared model → OS adapter; OS commands stay
inside the adapter and parsers are tested against recorded `lsof` output. No
telemetry, no network service. See [ARCHITECTURE.md](ARCHITECTURE.md).

The 1.0 target is a tested one-port CLI for macOS, Linux and Windows; 1.1 adds
a local-port overview; a confirmed stop action is planned for 0.2. Remote
scanning, Docker management, traffic measurement and history are out of scope.

Manual checks (macOS 26.6.2, 2026-10-05) with real output and the problems
found are in [docs/validation.md](docs/validation.md).
