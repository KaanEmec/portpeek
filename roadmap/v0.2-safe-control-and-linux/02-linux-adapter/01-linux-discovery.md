# Job: implement Linux discovery

## Objective
Provide the same one-port answer on Linux while respecting Linux-specific socket and permission behavior.

## Tasks
- Choose and document a reliable `ss`/`procfs` discovery path and a fallback for unavailable process details.
- Map IPv4, IPv6, TCP listeners, and bound UDP sockets into the shared model.
- Resolve command and working directory only where readable.
- Keep command invocation and parsing behind the platform adapter.

## Expected outcome
Linux returns the same result fields and error categories as macOS, with unavailable details labeled honestly.
