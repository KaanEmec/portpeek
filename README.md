# Port Peek

Port Peek answers one question: **what is using this port?** Give it a port
number and it names the local process that owns it and how the socket is bound.
It is a command-line tool for macOS and Linux that can also stop the owner after
confirmation. A Windows adapter is implemented but not yet verified on a real
Windows machine; a terminal overview comes later (see the
[roadmap](roadmap/README.md)).

## Install

From source (Go 1.27 or newer), either of:

```
go install github.com/kaanemec/portpeek/cmd/portpeek@latest
go build -o portpeek ./cmd/portpeek        # from a checkout
```

The module path is a placeholder until there is a public remote, so use the
checkout build for now. Runtime dependencies: `lsof` and `ps` on macOS (both
ship with it), `ss` from iproute2 plus `/proc` on Linux.

### Releases

Prebuilt archives for macOS (amd64, arm64), Linux (amd64, arm64) and Windows
(amd64) are attached to each GitHub Release once the project has a public
remote (none yet, so there are no downloads today). Download the archive for
your OS and architecture together with `checksums.txt`, then verify and unpack:

```
shasum -a 256 -c checksums.txt --ignore-missing
tar -xzf portpeek_<version>_<os>_<arch>.tar.gz     # .zip on Windows
```

(`--ignore-missing` is a GNU/BSD option that skips archives you did not
download; plain `-c` complains about them.) The binary is a single file with no
runtime dependencies beyond the OS tools listed above; put it anywhere on your
`PATH`.

## Usage

```
Usage: portpeek <port> [--tcp|--udp] [--json]
       portpeek <port> --stop [--pid <pid>] [--force] [--tcp|--udp]

Options:
  --tcp        only look at TCP sockets
  --udp        only look at UDP sockets
  --json       print machine-readable JSON (schema 1)
  --stop       send SIGTERM to the process using the port, after confirmation
  --pid <pid>  with --stop, the process to stop when several use the port
  --force      with --stop, skip confirmation (required when stdin is not a terminal)
  --version    print the version and exit
  -h, --help   show this help and exit
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
for you to run; Port Peek does not run it (see `--stop` below). A field that
could not be read prints the reason, for example
`Command: unavailable (process exited)`.

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

`complete` is `false` without root (other users' sockets may be missing) or
when any owner is unknown, and `true` otherwise. `"pid": 0` means the owner is
unknown: the socket was seen but its process could not be read (see Linux
below). `state` is `LISTEN` for TCP listeners and `BOUND` for UDP sockets and
TCP sockets bound without listening. With no match, `owners` is `[]` and the
exit code is 1; several owners appear as several entries.

The full field reference, the error object and the compatibility policy are in
[docs/json.md](docs/json.md).

## Stopping a process

`portpeek <port> --stop` prints the normal result, then sends SIGTERM to the
owner. When several processes use the port, choose one with `--pid <pid>`;
Port Peek never picks for you. On a terminal it asks
`Send SIGTERM to node (PID 48213)? [y/N]` and only `y` or `yes` proceeds.
Without a terminal (scripts, pipes) it refuses unless `--force` is given.

Right before signalling it inspects the port again and stops nothing if the
PID no longer owns the port or its name changed. It sends SIGTERM only, waits
up to 2 seconds, and reports whether the process exited; if not, it prints
`kill -9 <pid>` for you to run and never sends SIGKILL itself. An unknown
owner (`"pid": 0`) is never signalled. `--stop` cannot be combined with
`--json`. Every case where nothing was signalled exits 4.

## Exit codes

- `0` at least one process uses the port; `1` no listening or bound socket
- `2` invalid input (bad or missing port, `--tcp` with `--udp`, unknown flag)
- `3` inspection failed (`lsof` or `ss` missing, permission denied, command error)
- `4` `--stop` stopped nothing (declined, no `--force` without a terminal,
  several owners and no `--pid`, unknown owner, process changed, signal failed)
- `130` interrupted (Ctrl-C)

## Permissions

On macOS without `sudo`, `lsof` only sees your own user's sockets, so a port
held by a root-owned service is reported as no socket (exit 1). To flag this, a
non-root run ends every text result with
`Sockets owned by other users are not visible without elevated privileges (try sudo).`
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
`used by an unknown process` with `Owner: unavailable (not readable without
elevated privileges)`, no `Stop:` hint, `"pid": 0` and `"complete": false`;
the text then ends with
`Owner details for some sockets are not readable without elevated privileges (try sudo).`
Root inside a container may still lack `CAP_SYS_PTRACE` and see unknown owners.
`ss -H` needs iproute2 4.13 or newer. A socket bound to one device prints as
`*%eth0` and is reported as a specific interface.

### Windows (unverified)

Discovery runs `netstat -a -n -o -p <TCP|TCPv6|UDP|UDPv6>` and reads the process
name and command line through PowerShell (`Get-CimInstance Win32_Process`).
This adapter is tested against fixtures and compiles for Windows, but it has
not yet run on a real Windows machine; the Windows CI job is the first check.
Known limits: other users' command lines need an Administrator shell (the
executable path is shown instead, or the field is unavailable); user and
working directory are always unavailable; `--stop` is not supported, the
`Stop:` hint is `taskkill /PID <pid>`; `netstat` translates state words on
non-English Windows, and translated rows are dropped rather than guessed, so a
localized system may report no owner for a port that is in use.

## Limitations

- macOS and Linux are tested on real sockets; Windows is unverified (see above).
- On macOS without `sudo`, a root-owned port exits 1, like an unused port. Check
  the hint or `"complete": false`, or run with `sudo`.
- On macOS, `ps` renders newlines in a command as `\012`.
- Exposure comes from the bound address only (`127.0.0.1` and `::1` are
  loopback, `*` is all interfaces). It says nothing about firewalls.
- A dual-stack `::` listener appears once, as IPv6 `*`.
- Link-local IPv6 prints as `[fe80::1%lo0]`; `lsof` shows `[fe80:1::1]`.
- Commands are printed in full, so browser helpers give long output.
- A process can exit between lookups; its unreadable fields show as unavailable.

## Design and boundaries

Go, standard library only. CLI → shared model → OS adapter; OS commands stay
inside the adapter and parsers are tested against recorded `lsof` and `ss`
output. No telemetry, no network service. See [ARCHITECTURE.md](ARCHITECTURE.md).

The 1.0 target is a tested one-port CLI for macOS, Linux and Windows; 1.1 adds
a local-port overview. Remote scanning, Docker management, traffic measurement
and history are out of scope.

Manual checks (macOS 26.6.2, 2026-10-05) with real output and the problems
found are in [docs/validation.md](docs/validation.md).

License: MIT
