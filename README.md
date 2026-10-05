<h1 align="center">Port Peek</h1>

<p align="center"><b>What is using this port? The local process and its binding, on macOS, Linux and Windows.</b></p>

<p align="center"><a href="https://github.com/kaanemec/portpeek/actions/workflows/ci.yml"><img src="https://github.com/kaanemec/portpeek/actions/workflows/ci.yml/badge.svg" alt="CI"></a> <a href="https://github.com/kaanemec/portpeek/releases/latest"><img src="https://img.shields.io/github/v/release/kaanemec/portpeek" alt="Release"></a> <a href="LICENSE"><img src="https://img.shields.io/badge/license-MIT-blue.svg" alt="License: MIT"></a></p>

<p align="center"><img src="docs/tui.png" alt="portpeek: every local port with its process, PID and exposure"></p>

- `portpeek 3000` names the owner of one port in four lines.
- `--detail` shows everything known; `--json` is for scripts.
- `portpeek` on its own opens a live, searchable overview of every port.
- `--stop` sends SIGTERM only after confirmation and a fresh identity check.

## Install

### Homebrew (macOS and Linux)

```sh
brew install kaanemec/tap/portpeek
```

### Install script (macOS and Linux)

```sh
curl -fsSL https://raw.githubusercontent.com/kaanemec/portpeek/main/install.sh | sh
```

The script picks the right archive and verifies its SHA-256 checksum. It
installs to `/usr/local/bin`, or `~/.local/bin` if that is not writable, and
never runs `sudo`.

<details><summary>Pin a version or choose the directory</summary>

```sh
curl -fsSL https://raw.githubusercontent.com/kaanemec/portpeek/main/install.sh | PORTPEEK_VERSION=v1.3.0 PORTPEEK_INSTALL_DIR="$HOME/bin" sh
```

</details>

### Go

With Go 1.27 or newer, on any platform:

```sh
go install github.com/kaanemec/portpeek/cmd/portpeek@latest
```

### Windows

Download `portpeek_<version>_windows_amd64.zip` from
[Releases](https://github.com/kaanemec/portpeek/releases/latest). Unpack it and
put `portpeek.exe` on your `PATH`, or use the `go install` line above.

Every release ships `checksums.txt`; see
[verifying a release](docs/reference.md#verifying-a-release).

## Usage

### One port

Give a port to see who owns it:

```
$ portpeek 3000
3000/tcp  Python  (PID 13337)
  127.0.0.1:3000   listening   loopback only
  Python -m http.server 3000 --bind 127.0.0.1
  stop: kill 13337
other users' sockets hidden; run with sudo
```

### Details

`--detail` adds the user, working directory and both stop commands:

```
$ portpeek 3000 --detail
3000/tcp  Python  PID 13337  user kaanemec
  127.0.0.1:3000  v4  listening  loopback only
  cmd   Python -m http.server 3000 --bind 127.0.0.1
  cwd   /Users/kaanemec/Developer/Port-Peek
  stop  kill 13337  ·  portpeek 3000 --stop

other users' sockets hidden; run with sudo
```

On a terminal the output is styled. Several processes on one port get one row
each:

![portpeek 5353 and portpeek 5353 --detail in a terminal](docs/cli.png)

### Overview (TUI)

Run `portpeek` with no arguments to open it (`portpeek tui` also works): a
searchable table of every local port, refreshed every 5 seconds (`--interval`
to change). There is also a [light-terminal variant](docs/tui-light.png).

| Key | Action |
|---|---|
| `↑/↓` | move |
| `/` | search |
| `s` | sort |
| `r` | refresh |
| `p` | pause |
| `Enter` | details |
| `k` | stop (in details) |
| `q` | quit |

![portpeek details pane for port 5353](docs/tui-details.png)

### Scripts (JSON)

`--json` prints a versioned document (schema 1), described in
[docs/json.md](docs/json.md):

```
$ portpeek 3000 --json | grep -E '"(complete|pid|exposure)"'
  "complete": false,
        "pid": 13337,
          "exposure": "loopback"
```

### Stopping a process

`portpeek 3000 --stop` asks for confirmation (`--force` in scripts). It checks
again that the same process still owns the port, then sends SIGTERM. It never
sends SIGKILL; if the process survives 2 seconds, it prints `kill -9 <pid>` for
you to decide.

## What it tells you

- **Owner**: process name, PID and user, for every process that holds the port.
- **Command**: the command line (one line; full in `--json`) and working directory.
- **Binding**: address, protocol and state (`listening` or `bound`). IPv4 and
  IPv6 of the same address show as `(v4+v6)`.
- **Exposure**: `loopback only`, `all interfaces` or `interface <address>`,
  from the bound address. It says nothing about firewalls.
- **Honest gaps**: without root, other users' sockets are hidden (macOS) or
  have no owner (Linux). Port Peek says so in a hint line and in
  `"complete": false`, instead of calling the port free. Unreadable fields are
  marked unavailable with a reason, never guessed.

## Platforms

| OS | Discovery | Without root | Stop |
|---|---|---|---|
| macOS | `lsof` + `ps` | other users' sockets hidden; use `sudo` | `--stop` |
| Linux | `ss` + `/proc` | other users' sockets show an unknown owner | `--stop` |
| Windows | `netstat` + PowerShell | other users' command lines need Administrator | not supported yet |

Windows is verified by the CI live test. Manual checks with real output are in
[docs/validation.md](docs/validation.md).

## Safety

- Read-only by default: it runs the OS tools above and changes nothing.
- Stopping needs `--stop` (or `k` in the TUI) and a confirmation. It rechecks
  the process identity right before signalling and sends SIGTERM only.
- No telemetry and no network access.

## More

- [docs/reference.md](docs/reference.md): options, output, exit codes, permissions, limitations
- [docs/json.md](docs/json.md): JSON contract and compatibility policy
- [CHANGELOG.md](CHANGELOG.md): release notes
- [ARCHITECTURE.md](ARCHITECTURE.md): design and decisions
- [roadmap/](roadmap/README.md): what was planned and built, version by version

## License

[MIT](LICENSE)
