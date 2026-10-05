# Job: list local ports

## Objective
Give the TUI a complete snapshot of locally bound ports without running a separate expensive inspection for every row.

## Tasks
- Extend the OS adapters to enumerate listening TCP and bound UDP sockets on each supported platform.
- Reuse the shared process, address, and exposure model from the CLI; group duplicate observations without hiding distinct owners or bindings.
- Exclude outbound-only connections and remote hosts; label process details that permissions prevent reading.
- Add fixtures for IPv4/IPv6, TCP/UDP, shared ports, and a process exiting during collection.

## Expected outcome
One inventory request returns a truthful, deduplicated snapshot of local ports and their known owners on macOS, Linux, and Windows.
