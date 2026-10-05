# JSON output (schema 1)

`portpeek <port> --json` prints one JSON document to standard output. This page
is the contract for scripts. The document is indented with two spaces and always
ends with a newline.

Port Peek is a read-only inspection tool. `--json` cannot be combined with
`--stop` (that is a usage error, exit code 2).

## Result document

```json
{
  "schema": 1,
  "query": { "port": 47120, "protocol": "" },
  "complete": false,
  "owners": [ { "process": { ... }, "sockets": [ ... ] } ]
}
```

| Field | Type | Meaning |
|---|---|---|
| `schema` | integer | Version of this contract. Always `1` for the 1.x releases. |
| `query` | object | The question that was asked. |
| `query.port` | integer | Port number, 1 to 65535. |
| `query.protocol` | string | `"tcp"`, `"udp"`, or `""` when no `--tcp` or `--udp` was given, meaning both protocols were checked. |
| `complete` | boolean | See [`complete`](#complete). |
| `owners` | array of owner | Every process that owns the port. Always an array, never `null`; `[]` when no socket matched. |

A port with no matching socket is a valid answer, not an error: the document has
`"owners": []` and the exit code is 1. Because of `complete`, an empty `owners`
array never means the port is free.

### Owner

One entry per process. A process that holds several sockets on the port (IPv4
and IPv6, TCP and UDP) appears once, with several sockets. Several processes
can share a port, which gives several owners.

| Field | Type | Meaning |
|---|---|---|
| `process` | object | The owning process. |
| `sockets` | array of socket | The sockets this process holds on the port. Always an array. |

### Process

All fields are always present. A field that could not be read is an empty string
(`""`, or `0` for `pid`) and has an entry in `unavailable` that says why.

| Field | Type | Meaning |
|---|---|---|
| `pid` | integer | Process ID. `0` means the owner is unknown: the socket was seen but its process could not be determined (for example another user's socket on Linux without root). A `pid` of `0` is never a real process and must not be passed to `kill`. |
| `name` | string | Process name. |
| `user` | string | Owning user. |
| `command` | string | Full command line as the OS reports it. |
| `working_dir` | string | Current working directory of the process. |
| `unavailable` | object | Always an object, never `null`; `{}` when every field was read. Keys are the field names `name`, `user`, `command` and `working_dir`; each value is a short human-readable reason such as `process exited`. The reason text is for people, not for matching. |

For an unknown owner (`pid` 0), `name`, `user`, `command` and `working_dir` are
all empty and all four appear in `unavailable`.

### Socket

| Field | Type | Meaning |
|---|---|---|
| `protocol` | string | `"tcp"` or `"udp"`. |
| `family` | string | `"ipv4"` or `"ipv6"`. |
| `address` | string | Bound local address as the OS reports it, without port or brackets: `127.0.0.1`, `::1`, `fe80::1%lo0`. A wildcard bind is `*`; a wildcard bound to one device is `*%eth0` on Linux. |
| `port` | integer | Local port; equals `query.port`. |
| `state` | string | `"LISTEN"` for a TCP listener. `"BOUND"` for a UDP socket and for a TCP socket bound to the port without listening. |
| `exposure` | string | Where the bound address can be reached from; see below. |

`exposure` is derived from `address` only. It says nothing about firewalls.

| Value | Meaning |
|---|---|
| `loopback` | Only this machine can connect (`127.0.0.1`, `::1`). |
| `all-interfaces` | Wildcard address; reachable on every local interface. |
| `interface` | Bound to one specific non-loopback address or device. |
| `unknown` | The address could not be classified. |

Only sockets that own the port are listed: a TCP listener, a TCP socket bound
without listening, or a UDP socket bound to the port. Established connections
and connected UDP sockets are not reported.

### `complete`

`complete` tells a script whether the answer can be trusted as the full picture.

- `true`: every socket on the port could be listed and every listed socket has
  a known owner. On macOS and Linux that requires root; on Windows `netstat`
  lists every socket regardless of privilege, so only unknown owners make it false.
- `false`: either Port Peek was not run as root on macOS/Linux, so other users'
  sockets may be missing (on macOS `lsof` omits them silently, so a "no match"
  may be a root service), or at least one owner is unknown (`"pid": 0`).

`"complete": false` is not a failure. Treat `"owners": []` with
`"complete": false` as "nothing visible", and re-run with `sudo` to be sure.
Fields that merely need elevated rights to read (for example another user's
command line) are reported in `unavailable` and do not affect `complete`.

## Error document

When inspection itself fails and `--json` was given, the document on standard
output is an error object and the exit code is 3. Nothing is written to standard
error in this case.

```json
{
  "schema": 1,
  "error": {
    "kind": "tool-missing",
    "message": "required tool \"lsof\" is not installed, so port 47120 cannot be inspected."
  }
}
```

| Field | Type | Meaning |
|---|---|---|
| `schema` | integer | Same schema version as above. |
| `error.kind` | string | Machine-readable category; see below. |
| `error.message` | string | One line for people. Wording may change at any time; match on `kind`, not on `message`. |

| `kind` | Meaning |
|---|---|
| `tool-missing` | A required system command (`lsof`, `ps`, `ss`) is not installed. |
| `permission-denied` | The OS refused access to socket or process information. Re-run with `sudo`. |
| `command-failed` | A system command ran but failed unexpectedly. |
| `unsupported-platform` | There is no adapter for this operating system yet. |
| `unknown` | Any other failure. |

A script should treat an unrecognised `kind` as a generic failure.

Errors that happen before inspection are not JSON. Invalid input (exit code 2),
an interrupt (130) and a failure to write the output print plain text to
standard error, even with `--json`.

## Exit codes

The exit code is the primary signal; the JSON adds detail.

| Code | Meaning | Standard output with `--json` |
|---|---|---|
| `0` | At least one process uses the port. | Result document with one or more owners. |
| `1` | No listening or bound socket matched. | Result document with `"owners": []`. |
| `2` | Invalid input or usage (bad or missing port, `--tcp` with `--udp`, `--stop` with `--json`, unknown flag). | Nothing; message on standard error. |
| `3` | Inspection failed. | Error document. |
| `4` | `--stop` stopped nothing. Not reachable with `--json`. | Not applicable. |
| `130` | Interrupted. | Nothing; message on standard error. |

## Compatibility policy

The JSON output is stable from 1.0.

- Within `"schema": 1`, fields are only ever added. A field is never renamed,
  removed, or changed in type, and the meaning of an existing value does not
  change. New values may appear in enumerations (`exposure`, `error.kind`,
  and the keys of `unavailable`).
- A breaking change increments `schema`. It will be announced in the changelog
  and needs a new major version of Port Peek.
- Scripts should ignore fields they do not know, check `schema`, and treat
  unknown enumeration values as "unknown" instead of failing.
- Not covered by the contract: the text output, the wording of `message` and of
  the `unavailable` reasons, the order of owners and of sockets, and the exact
  whitespace of the JSON.
- The exit codes above are part of the 1.x contract in the same way.

## Example

From a macOS run without root (so `complete` is `false`):

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

(`command` is shortened with `...`; the rest is real output.)

Example with jq, which acts on the exit code and `complete`:

```
portpeek 3000 --json > out.json
case $? in
  0) jq -r '.owners[].process.pid' out.json ;;
  1) jq -e '.complete' out.json >/dev/null || echo "nothing visible; try sudo" ;;
  3) jq -r '.error.kind' out.json ;;
esac
```
