# Job: verify stop behavior

## Objective
Make the optional stop action predictable when processes exit, refuse signals, or share a port.

## Tasks
- Test confirmation, cancellation, noninteractive behavior, permission errors, and a process that exits before confirmation.
- Define what happens when several PIDs match: require an explicit choice or decline the action.
- Confirm the result reports whether the signal was sent and whether the process actually exited.
- Document the signal used and its limitations.

## Expected outcome
The stop flow is explicit, recoverable when it fails, and never chooses among multiple owners on the user's behalf.
