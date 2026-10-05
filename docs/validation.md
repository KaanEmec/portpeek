# v0.1 macOS validation record

- Date: 2026-10-05
- macOS: 26.6.2 (build 25G83), arm64
- lsof: revision 4.91 (Apple)
- Go: go1.27.1; `go test ./...` passes
- Build: `go build -o portpeek ./cmd/portpeek`; version string `portpeek dev`
- Run as a normal user; `sudo` was not exercised (see "Not verified").

Listeners were throwaway Python sockets on ports 47101-47199. Paths and long
commands are trimmed with `...`; the rest is copied from real runs. Exit code
is in `[ ]`.

## Results

| # | Check | Result |
|---|-------|--------|
| 1 | localhost TCP listener | pass |
| 2 | wildcard TCP listener | pass |
| 3 | IPv6 `[::1]` listener | pass |
| 4 | UDP bound socket, `--tcp` / `--udp` filters | pass |
| 5 | TCP and UDP from one process | pass |
| 6 | two processes on one port (SO_REUSEPORT) | pass |
| 7 | unused port | pass |
| 8 | invalid input (7 cases) | pass |
| 9 | `--json` match / no match / multi-socket | pass (see note on `state`) |
| 10 | process exits right after bind | pass |
| 11 | outbound-only connection not reported as owner | pass |
| 12 | dual-stack `::` listener | pass |
| 13 | read-only audit | pass |
| 14 | root-owned port without sudo | partial: documented limitation, see Findings |

## 1. localhost TCP  (pass)

```
$ portpeek 47101                                   [0]
Port 47101/tcp is used by Python (PID 46279)
  Address:      127.0.0.1:47101 (IPv4, LISTEN)
  Exposure:     loopback only — reachable from this machine only
  User:         kaanemec
  Command:      .../Python listen.py 3 tcp4:47101
  ...
```

## 2-3. Wildcard IPv4 and IPv6 loopback  (pass)

```
$ portpeek 47102   [0]   Address: *:47102 (IPv4, LISTEN)
                         Exposure: all interfaces — reachable from other machines on the network
$ portpeek 47103   [0]   Address: [::1]:47103 (IPv6, LISTEN)
                         Exposure: loopback only — reachable from this machine only
```

## 4. UDP bound socket  (pass)

```
$ portpeek 47104                                   [0]
Port 47104/udp is used by Python (PID 46325)
  Address:      127.0.0.1:47104 (IPv4, bound)
$ portpeek 47104 --udp                             [0]   (same as above)
$ portpeek 47104 --tcp                             [1]
No process is using port 47104 (tcp).
Sockets owned by other users are not visible without elevated privileges (try sudo).
```

## 5. TCP + UDP from the same process  (pass)

One owner, two sockets; `--tcp` narrows to one.

```
$ portpeek 47105                                   [0]
Port 47105/tcp+udp is used by Python (PID 46343)
  Address:      127.0.0.1:47105 (tcp, IPv4, LISTEN)
  Address:      127.0.0.1:47105 (udp, IPv4, bound)
$ portpeek 47105 --tcp                             [0]
Port 47105/tcp is used by Python (PID 46343)
  Address:      127.0.0.1:47105 (IPv4, LISTEN)
```

## 6. Two processes, one port (SO_REUSEPORT)  (pass)

```
$ portpeek 47106                                   [0]
2 processes use port 47106:
Port 47106/tcp is used by Python (PID 46359)   Address: *:47106 (IPv4, LISTEN)
Port 47106/tcp is used by Python (PID 46360)   Address: *:47106 (IPv4, LISTEN)
```
(each owner block also has Exposure/User/Command/Working dir/Stop lines)

## 7. Unused port  (pass)

```
$ portpeek 47199                                   [1]
No process is using port 47199 (tcp or udp).
Sockets owned by other users are not visible without elevated privileges (try sudo).
```

## 8. Invalid input  (pass)

All exit 2, print to stderr, and end with `Try 'portpeek --help' for usage.`

```
$ portpeek abc            portpeek: invalid port "abc": must be a number between 1 and 65535   [2]
$ portpeek 0              portpeek: invalid port 0: must be between 1 and 65535                [2]
$ portpeek 70000          portpeek: invalid port 70000: must be between 1 and 65535            [2]
$ portpeek                portpeek: missing port argument                                      [2]
$ portpeek 47101 --tcp --udp   portpeek: --tcp and --udp cannot be used together               [2]
$ portpeek -5             portpeek: flag provided but not defined: -5                          [2]
$ portpeek 47101 --bogus  portpeek: flag provided but not defined: -bogus                      [2]
$ portpeek abc --json     (stdout empty, error on stderr)                                      [2]
```

## 9. `--json`  (pass)

Match (trimmed), exit 0:

```
$ portpeek 47109 --json
{ "schema": 1,
  "query": { "port": 47109, "protocol": "" },
  "owners": [ { "process": { "pid": 46397, "name": "Python", "user": "kaanemec",
        "command": ".../Python listen.py 3 tcp4:47109", "working_dir": ".../scratchpad",
        "unavailable": {} },
      "sockets": [ { "protocol": "tcp", "family": "ipv4", "address": "127.0.0.1",
        "port": 47109, "state": "LISTEN", "exposure": "loopback" } ] } ] }
```

No match, exit 1: `{"schema": 1, "query": {...}, "owners": []}`.

TCP+UDP process (47110): two sockets under one owner; the UDP socket has
`"state": ""` (text output calls it `bound`).

## 10. Process exits right after binding  (pass)

Listener sleeps 0.3 s; portpeek started 0.2 s after bind. The process was
alive when `lsof` ran and gone by the time `ps` ran:

```
$ portpeek 47111                                   [0]
Port 47111/tcp is used by Python (PID 46444)
  Address:      127.0.0.1:47111 (IPv4, LISTEN)
  Command:      unavailable (process exited)
  Working dir:  .../scratchpad
  Stop:         kill 46444
```

Afterwards (process gone):

```
$ portpeek 47111                                   [1]
No process is using port 47111 (tcp or udp).
```

Both runs returned cleanly; no crash, no exit 3.

## 11. Outbound-only connection  (pass)

Listener on 47112 (PID 46455); a client in another process connected from
port 62328 (`lsof` shows `127.0.0.1:62328->127.0.0.1:47112 (ESTABLISHED)`).

```
$ portpeek 47112                                   [0]   one owner: the listener, PID 46455
$ portpeek 62328                                   [1]
No process is using port 62328 (tcp or udp).
```

The client is not reported as an owner of either port.

## 12. Dual-stack `::` listener  (pass)

One AF_INET6 socket on `::` (V6ONLY off), reported once, matching lsof's single row:

```
$ portpeek 47113                                   [0]
  Address:      *:47113 (IPv6, LISTEN)
```

## 13. Read-only audit  (pass)

`internal/inspect/lsof/lsof.go` runs exactly three commands through
`exec.CommandContext`: `lsof -nP -F pcnLTtfPR0 -i <proto>:<port>`,
`lsof -nP -F n -d cwd -a -p <pid>`, and `ps -o command= -p <pid>`.

`grep -rnE 'os/exec|os.Remove|syscall\.|os.Create|WriteFile|OpenFile|\.Kill\('`
over all non-test Go files matches only the `os/exec` import in `lsof.go`. No
file writes, no signals, no network. The `kill <pid>` line in text output is a
printed hint only.

## 14. Root-owned port without sudo  (partial)

`netbiosd` (root) holds UDP 137/138 (`netstat -anv -p udp`). Not visible to
the unprivileged lsof:

```
$ portpeek 137                                     [1]
No process is using port 137 (tcp or udp).
Sockets owned by other users are not visible without elevated privileges (try sudo).
$ portpeek 137 --json                              [1]
{ "schema": 1, "query": { "port": 137, "protocol": "" }, "owners": [] }
```

Also seen on UDP 5353: root's mDNSResponder is absent; only user-owned
Chrome/Codex processes were listed.

## Findings

1. **Exit 1 and JSON cannot tell "unused" from "hidden by permissions"** (check
   14). The text hint is printed for every no-match when not root, including a
   truly unused port, so it is not specific. `--json` carries no hint, so a
   script gets `owners: []` and exit 1 for a port that is in use.
2. **JSON UDP `state` is `""`** while text shows `bound`.
3. **JSON `query.protocol` is `""`** when neither flag was given.
4. **Stale PID in the stop hint** (check 10): `Stop: kill 46444` is shown for a
   process that has already exited.
5. **Very long commands are printed in full** (Chromium helpers on UDP 5353
   print roughly 1.2 KB per owner, including metrics ids). Readable but noisy.
6. `ps` renders newlines in a `-c` script as `\012`
   (`... -c import socket,time\012s=socket.socket(); ...`).

No Go files were changed.

## Not verified

- `sudo portpeek <port>` (no interactive sudo available in this run).
  Expected from lsof behaviour, not observed.
- Other macOS versions, Intel hardware, and specific-interface addresses
  (a LAN IP, `fe80::1`) live; only fixtures cover them.
