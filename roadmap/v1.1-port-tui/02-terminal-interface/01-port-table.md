# Job: searchable port table

## Objective
Let the user see which local ports are occupied and find a relevant service in seconds.

## Tasks
- Add `portpeek tui` using Bubble Tea, with columns for port, protocol, binding, process, PID, and exposure.
- Support keyboard navigation, search by port/process, and sorting by port or process.
- Show counts, last refresh time, loading, empty, and permission-limited states.
- Refresh on demand and at a modest interval, with a way to pause automatic refresh; preserve selection when rows change.
- Keep the table readable in a narrow terminal and without color.

## Expected outcome
The TUI opens to a useful local-port overview, stays responsive during refresh, and makes stale or incomplete data visible.
