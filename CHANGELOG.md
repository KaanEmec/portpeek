# Changelog

All notable changes to Port Peek are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and versions follow
[Semantic Versioning](https://semver.org/). The JSON output has its own
compatibility policy, described in [docs/json.md](docs/json.md).

## [Unreleased]

Nothing yet.

## [1.2.0] - 2026-10-05

First public release: everything below from 1.0.0 to 1.2.0 ships together.

### Added (1.2.0)

- Compact default text output: a headline per port, one line per binding
  (IPv4 and IPv6 of the same address collapse to `(v4+v6)`), the command
  shortened to the terminal width and a `stop:` line; several processes on a
  port print one aligned row each. Hints shrink to one short line such as
  `other users' sockets hidden; run with sudo`.
- `--detail`: every socket, user, full command, working directory and stop
  commands in aligned Sockets / Process / Stop sections. Ignored with
  `--json`. The `tui` details pane shows the same view, and its table uses
  the same binding and exposure wording (`*:5000 (v4+v6)`, `loopback only`).
- Bold and dim text on terminals only (never when piped, with `NO_COLOR` or
  `TERM=dumb`); lines are cut to the terminal width, 100 columns when piped.
  JSON output and exit codes are unchanged.

### Added (1.1.0)

- `portpeek tui`: searchable, auto-refreshing table of every local listening
  TCP and bound UDP socket, with a details pane that reuses the one-port
  answer and a confirmed stop action sharing the CLI's identity recheck.
- `Lister` on every adapter: one cheap inventory call without per-process
  enrichment.
- Dependencies: `charm.land/bubbletea/v2`, `bubbles/v2`, `lipgloss/v2`.

### Added (1.0.0)

- Windows adapter (`netstat` + PowerShell `Win32_Process`): fixture tests plus
  a live test that passes on the Windows CI runner.
- Platform-specific stop hint (`taskkill /PID` on Windows); `complete` no longer
  depends on a Unix uid on Windows.

### Release (1.0.0)
- JSON output schema 1 frozen and documented in [docs/json.md](docs/json.md).
- Prebuilt binaries for macOS (amd64, arm64), Linux (amd64, arm64) and Windows
  (amd64) with SHA-256 checksums, published by GoReleaser from a version tag.
- Licence (MIT) and release notes.

## [0.2.0] - 2026-10-05

### Added

- `--stop`: send SIGTERM to the process using the port, after confirmation on a
  terminal (or with `--force` when stdin is not a terminal). `--pid` chooses the
  process when several use the port. The port is inspected again right before
  signalling and nothing is stopped if the PID or its name changed. SIGKILL is
  never sent. Exit code `4` covers every case where nothing was stopped.
- Linux support through an `ss` and `/proc` adapter.
- Unknown owner handling: when a socket is visible but its process cannot be
  read (for example Linux without root), the result is reported as an unknown
  process with `"pid": 0`, no stop hint is printed, `--stop` refuses it, and the
  answer is marked incomplete.

## [0.1.0] - 2026-10-05

### Added

- macOS answer to "what is using this port?" through `lsof` and `ps`: process
  name, PID, user, command, working directory, bound address, state and
  exposure.
- Text output and versioned machine-readable JSON output (`--json`, schema 1).
- Exit codes: `0` at least one owner, `1` no matching socket, `2` invalid
  input, `3` inspection failed.

[Unreleased]: https://github.com/kaanemec/portpeek/compare/v1.2.0...HEAD
[1.2.0]: https://github.com/kaanemec/portpeek/releases/tag/v1.2.0
[0.2.0]: https://github.com/kaanemec/portpeek/compare/v0.1.0...v0.2.0
[0.1.0]: https://github.com/kaanemec/portpeek/releases/tag/v0.1.0
