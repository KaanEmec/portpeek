# Job: verify and document the TUI

## Objective
Ship a simple, stable overview that works on every platform where the TUI is advertised.

## Tasks
- Test the inventory and screen with empty, large, changing, and permission-limited socket sets.
- Check search, sorting, refresh, disappearing rows, narrow terminals, no-color mode, and safe actions on supported operating systems.
- Document `portpeek tui`, shortcuts, refresh behavior, and the meaning of “usage” and exposure labels.
- Build and package the TUI in the existing Port Peek release pipeline.

## Expected outcome
The 1.1 release has a usable TUI on its claimed platforms and leaves the one-port CLI behavior intact.
