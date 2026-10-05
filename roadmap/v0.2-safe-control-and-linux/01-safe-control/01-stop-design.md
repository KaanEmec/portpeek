# Job: design a safe stop flow

## Objective
Offer process stopping only when the user can identify exactly what will receive the signal.

## Tasks
- Display a copyable manual stop command in the ordinary result.
- Add an explicit stop option that rechecks the PID and process identity immediately before acting.
- Show the target and request confirmation in an interactive terminal; require an explicit force flag for noninteractive use.
- Prefer graceful termination, report failure clearly, and never silently escalate to a force kill.

## Expected outcome
A stale PID cannot cause an unreviewed stop, and a normal inspection still changes nothing.
