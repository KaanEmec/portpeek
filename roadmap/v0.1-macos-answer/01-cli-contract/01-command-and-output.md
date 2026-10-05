# Job: command and output contract

## Objective
Define a single-port command that gives a useful answer without requiring the user to interpret raw socket tables.

## Tasks
- Accept one valid TCP/UDP port number (1–65535), with a documented option to narrow the protocol when both appear.
- Define shared result fields: protocol, local address, process name, PID, command, working directory, and exposure classification.
- Render readable text with clear labels; mark unavailable fields instead of guessing.
- Set exit behavior and help text for invalid input, no match, partial information, and inspection failure.

## Expected outcome
`portpeek 3000` has a stable, documented text shape and distinct, understandable outcomes for valid, empty, and invalid queries.
