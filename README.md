# Port Peek

Port Peek answers one question quickly: **what is using this port?** Given a port number, it identifies the local process and explains how the socket is exposed. The first release is a macOS command-line tool; Linux and Windows adapters follow after the core answer is reliable. A later terminal interface provides an overview of local ports.

## Product contract

`portpeek 3000` reports the protocol, local address, process name, PID, launch command and working directory when available, plus whether the address is loopback-only or reachable from other interfaces. It distinguishes “no matching socket,” “information unavailable,” and “permission denied.” A later explicit action may stop the process after confirmation.

From v1.1, `portpeek tui` shows local listening TCP and bound UDP ports, their owning processes, and the same exposure information in a searchable list. Selecting a row opens the detailed one-port answer. Here, “usage” means which process owns a port and how it is bound; the TUI does not measure bandwidth or traffic volume.

## Technical shape

- **Language/interface:** Go, one small CLI binary using the standard library where practical; Bubble Tea, Bubbles, and Lip Gloss for the optional TUI.
- **Architecture:** CLI and TUI presentation → shared socket/process model → OS-specific discovery adapter. Keep OS commands behind the adapter so their output can be tested without an actual listener.
- **macOS source:** start with `lsof` for socket ownership and process details; use a direct OS API only if it materially improves reliability.
- **Other platforms:** Linux via `ss`/`procfs` as appropriate; Windows via system APIs or PowerShell. State unsupported fields honestly.
- **Output:** readable text by default and stable JSON for scripts by 1.0. No telemetry or network service.
- **Safety:** inspecting a port makes no changes; any stop action is opt-in, shows the exact PID, and requires confirmation.

## Boundaries and release target

The 1.0 target is a documented, tested one-port CLI for macOS, Linux, and Windows. Version 1.1 adds a compact local-port overview using the same discovery core. Remote scanning, Docker management, traffic measurement, and historical monitoring remain outside scope. Platform support is earned through real adapter tests, not inferred from compilation.

See [the roadmap](roadmap/README.md) for version goals and individual jobs. Each job has an objective, tasks, and an expected outcome; finish and verify a version before starting the next one.
