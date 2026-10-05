# v0.1 macOS validation record

- Date: 2026-10-05 (re-run against the final binary after the CLI changes)
- macOS: 26.6.2 (build 25G83), arm64
- lsof: revision 4.91 (Apple)
- Go: go1.27.1; `go test ./...` passes
- Build: `go build -o portpeek ./cmd/portpeek`; `portpeek --version` prints `portpeek dev`
- Run as a normal user; `sudo` was not exercised (see "Not verified").

Listeners were throwaway Python sockets on ports 47101-47199. Commands and
paths are trimmed with `...`; the rest is copied from real runs. Exit code is
in `[ ]`. As a non-root user every text result ends with the line
`Sockets owned by other users are not visible without elevated privileges (try sudo).`
(written `<sudo hint>` below).

## Results

| # | Check | Result |
|---|-------|--------|
| 1-3 | localhost, wildcard, `[::1]` TCP listeners | pass |
| 4 | UDP bound socket, `--tcp` filter | pass |
| 5 | TCP and UDP from one process | pass |
| 6 | two processes on one port (SO_REUSEPORT) | pass |
| 7 | unused port | pass |
| 8 | invalid input (7 cases) | pass |
| 9 | `--json` match, no match, TCP+UDP | pass |
| 10 | process exits right after bind | pass |
| 11 | outbound-only connection is not an owner | pass |
| 12 | dual-stack `::` listener | pass |
| 13 | TCP bound but not listening | pass |
| 14 | link-local IPv6 UDP | pass |
| 15 | root-owned port without sudo | pass (limitation, see Findings) |
| 16 | Ctrl-C exit code | pass (SIGINT sent with `kill -INT`) |
| 17 | read-only audit | pass |

No check failed.

## 1-3. Localhost, wildcard, IPv6 loopback

```
$ portpeek 47101   [0]
Port 47101/tcp is used by Python (PID 52437)
  Address:      127.0.0.1:47101 (IPv4, LISTEN)
  Exposure:     loopback only — accepts connections from this machine only
  Command:      .../Python listen.py 3 tcp4:47101
  Working dir:  .../scratchpad
  Stop:         kill 52437
<sudo hint>   (after a blank line)
$ portpeek 47102   [0]   Address: *:47102 (IPv4, LISTEN)
  Exposure:     all interfaces — accepts connections on every network interface (firewall not checked)
$ portpeek 47103   [0]   Address: [::1]:47103 (IPv6, LISTEN)   Exposure: loopback only
```

## 4-5. UDP, and TCP plus UDP from one process

```
$ portpeek 47104   [0]   Port 47104/udp ... Address: 127.0.0.1:47104 (IPv4, bound)
$ portpeek 47104 --tcp   [1]   No listening or bound socket on port 47104 (tcp).
$ portpeek 47105   [0]
Port 47105/tcp+udp is used by Python (PID 52486)
  Address:      127.0.0.1:47105 (tcp, IPv4, LISTEN)
  Address:      127.0.0.1:47105 (udp, IPv4, bound)
$ portpeek 47105 --tcp   [0]   one owner, only the LISTEN address
```

## 6. Two processes on one port (SO_REUSEPORT)

```
$ portpeek 47106   [0]
2 processes use port 47106:
Port 47106/tcp is used by Python (PID 52504)   Address: *:47106 (IPv4, LISTEN)
Port 47106/tcp is used by Python (PID 52505)   Address: *:47106 (IPv4, LISTEN)
<sudo hint>   (once, after the last owner)
```

## 7. Unused port

```
$ portpeek 47199   [1]
No listening or bound socket on port 47199 (tcp or udp).
<sudo hint>
```

## 8. Invalid input

All exit 2, write to stderr, and end with `Try 'portpeek --help' for usage.`

```
abc          invalid port "abc": must be a number between 1 and 65535
0 / 70000    invalid port 0: must be between 1 and 65535 (same form for 70000)
(no args)    missing port argument
47101 --tcp --udp   --tcp and --udp cannot be used together
47101 --bogus       flag provided but not defined: -bogus
abc --json   same message as `abc`; nothing on stdout
```

## 9. `--json`

Top-level keys: `schema`, `query`, `complete`, `owners`. `complete` is `false`
for a normal user. TCP+UDP process (socket list only), exit 0:

```
"complete": false,
"sockets": [
  { "protocol": "tcp", "family": "ipv4", "address": "127.0.0.1", "port": 47109,
    "state": "LISTEN", "exposure": "loopback" },
  { "protocol": "udp", "family": "ipv4", "address": "127.0.0.1", "port": 47109,
    "state": "BOUND", "exposure": "loopback" } ]
```

No match (`portpeek 47199 --json`, exit 1): same envelope with `"owners": []`.

## 10. Process exits right after bind

Listener sleeps 0.3 s; portpeek started 0.2 s after bind. Alive for the `lsof`
step, gone by the `ps` step:

```
$ portpeek 47111   [0]
Port 47111/tcp is used by Python (PID 52572)
  Address:      127.0.0.1:47111 (IPv4, LISTEN)
  Command:      unavailable (process exited)
$ portpeek 47111   [1]   (after exit)   No listening or bound socket on port 47111 (tcp or udp).
```

No crash, no exit 3.

## 11. Outbound-only connection

Listener on 47112 (PID 52590); a client in another process connected from port
63180 (`lsof`: `127.0.0.1:63180->127.0.0.1:47112 (ESTABLISHED)`).
`portpeek 47112` [0] lists only the listener; `portpeek 63180` [1] says
`No listening or bound socket on port 63180 (tcp or udp).`

## 12. Dual-stack `::` listener

One AF_INET6 socket on `::` (V6ONLY off). lsof shows one row
(`TCP *:47113 (LISTEN)`) and so does portpeek: `Address: *:47113 (IPv6, LISTEN)`.

## 13. TCP bound but not listening

`s=socket.socket(); s.bind(('127.0.0.1',47115)); time.sleep(5)`, no `listen()`:

```
$ portpeek 47115   [0]
Port 47115/tcp is used by Python (PID 52637)
  Address:      127.0.0.1:47115 (IPv4, bound, not listening)
  Exposure:     loopback only — accepts connections from this machine only
$ portpeek 47115 --json   [0]   "state": "BOUND"  (protocol "tcp")
```

Note the exposure line says "accepts connections" although nothing is
listening; see Findings.

## 14. Link-local IPv6

UDP socket bound to `fe80::1%lo0` (via `getaddrinfo`, scope id 1):

```
$ portpeek 47116   [0]
  Address:      [fe80::1%lo0]:47116 (IPv6, bound)
  Exposure:     specific interface fe80::1%lo0 — accepts connections on that address only (firewall not checked)
$ lsof -nP -i UDP:47116   ->   UDP [fe80:1::1]:47116
```

lsof prints the kernel's embedded-scope form; portpeek rewrites it. JSON:
`"address": "fe80::1%lo0"`, `"exposure": "interface"`.

## 15. Root-owned port without sudo

`netbiosd` (root) holds UDP 137/138 (`netstat -anv -p udp`). An unprivileged
lsof cannot see it:

```
$ portpeek 137   [1]
No listening or bound socket on port 137 (tcp or udp).
<sudo hint>
$ portpeek 137 --json   [1]   "complete": false, "owners": []
```

Exit code 1 is the same as for an unused port, but the text hint and
`"complete": false` now say the answer may be incomplete.

## 16. Ctrl-C

`portpeek 5353 &` then `kill -INT` after 20-70 ms, six times: each exited 130
with `portpeek: interrupted` on stderr. A real terminal Ctrl-C was not tried.

## 17. Read-only audit

`internal/inspect/lsof/lsof.go` runs exactly three commands via
`exec.CommandContext`: `lsof -nP -F pcnLTtfP0 -i <proto>:<port>`,
`lsof -nP -F n -d cwd -a -p <pid>`, and `ps -o command= -p <pid>`.
`grep -rnE 'os/exec|os.Remove|syscall\.|os.Create|WriteFile|OpenFile|\.Kill\('`
over non-test Go files matches only the `os/exec` import and its use in
`execRunner` (re-checked on the final source). The `kill <pid>` line in text
output is a hint only.

## Findings

Resolved since the first pass: a root-owned port used to look exactly like an
unused one (text hint only on no-match, nothing in JSON). Now the hint prints
after every result when not root, and JSON carries `"complete": false`.

Remaining, none blocking:

1. A hidden root-owned port still exits 1; scripts must read `complete`.
2. The sudo hint appears on every result when not root, so it says little per run.
3. Exposure text for a non-listening TCP socket (check 13) says "accepts
   connections", which is not accurate without `listen()`.
4. JSON `query.protocol` is `""` when neither flag is given.
5. Stale PID in the stop hint (check 10): `kill 52572` for an exited process.
6. Long commands are printed in full (UDP 5353 Chrome/Codex helpers: about
   1.2 KB per owner, including metrics ids).
7. `ps` renders newlines as `\012`; `python3 -c` scripts show
   `-c \012import socket,time\012...` (JSON-escaped as `\\012`).

## Not verified

- `sudo portpeek <port>` (no interactive sudo here); expected `"complete": true`
  and no hint, not observed.
- Other macOS versions, Intel hardware, a real LAN-address listener.
