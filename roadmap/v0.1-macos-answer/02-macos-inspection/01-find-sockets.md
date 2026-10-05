# Job: find macOS sockets

## Objective
Identify processes bound to the requested port on macOS without confusing a connection to that port with ownership of it.

## Tasks
- Implement the macOS adapter using `lsof` and parse its machine-readable form where available.
- Cover IPv4 and IPv6, TCP listeners, and bound UDP sockets; label UDP without implying a listening state.
- Return all matching owners when more than one socket matches the query.
- Handle missing `lsof`, access restrictions, and output variations with targeted fixtures.

## Expected outcome
Known local TCP and UDP sockets resolve to the correct PID and address; unrelated outbound connections do not appear as owners.
