# Job: validate Linux behavior

## Objective
Catch platform-specific errors before claiming Linux support.

## Tasks
- Test representative listeners and bound UDP sockets on a Linux machine or CI runner.
- Check permission-limited processes, multiple owners, and missing discovery tools.
- Add parser fixtures and platform-specific usage notes.

## Expected outcome
The documented Linux examples match observed behavior, and failures direct the user to a useful next step.
