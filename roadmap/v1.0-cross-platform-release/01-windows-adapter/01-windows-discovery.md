# Job: implement and verify Windows discovery

## Objective
Bring the same one-port diagnostic to Windows without weakening the shared answer.

## Tasks
- Select a Windows system API or structured PowerShell source for socket ownership and process details.
- Map TCP and UDP bindings, IPv4/IPv6 addresses, and permission-limited fields into the shared model.
- Test real listeners, multiple matches, an unused port, and process exit during inspection on Windows.
- Document any Windows-only limitations and use the same explicit stop safeguards if stop is supported.

## Expected outcome
Windows users receive a correct process/PID answer with understandable limitations and no false ownership claims.
