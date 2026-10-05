# Job: verify the first release

## Objective
Prove the macOS diagnostic works in ordinary development situations and is ready for a small public preview.

## Tasks
- Exercise localhost and wildcard listeners, IPv4/IPv6, TCP/UDP, an unused port, an invalid port, and a short-lived process.
- Add focused automated tests for parser and output behavior; record manual checks that require real sockets.
- Write installation, example output, permissions, and limitation notes.
- Confirm the command is read-only and produces no confusing success result when ownership is unknown.

## Expected outcome
A user can install the preview, inspect a real port, understand the result, and reproduce the key checks from the repository.
