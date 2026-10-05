# Job: safe row actions

## Objective
Make common follow-up actions convenient without turning a table selection into an accidental process change.

## Tasks
- Provide copyable port, PID, and one-port CLI command for the selected row.
- If the v0.2 stop action is present, expose it only from details with the same identity recheck and explicit confirmation.
- Require a deliberate owner choice when several processes match a port.
- Return to a refreshed table after an action and show its result or failure.

## Expected outcome
Users can carry a port result into another tool or deliberately stop a verified process; browsing the table makes no changes.
