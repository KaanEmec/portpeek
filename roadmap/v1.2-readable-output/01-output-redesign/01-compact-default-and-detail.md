# Job: compact default output and a clean --detail view

## Objective
Make `portpeek <port>` readable at a glance: a few short lines that identify the
owner and its exposure. Move everything else behind `--detail`, and make that view
clean too. JSON is unchanged.

## Tasks
- Default (one owner): a headline `3000/tcp  node (PID 48213)`, one indented line per
  binding `127.0.0.1:3000  listening  loopback only`, one line with the command
  shortened to the terminal width (basename of argv[0] plus arguments, cut with `…`),
  and `stop: kill 48213`. Working directory, user, and the long exposure sentence are
  not shown.
- Default (several owners): a headline `5353/udp  3 processes` and one aligned row per
  owner: name, PID, bindings collapsed (`*:5353 (v4+v6)`), exposure word. No commands,
  no stop lines (point to `--pid` only when `--stop` is used).
- `--detail`: the full answer with grouped, aligned sections (Sockets / Process / Stop),
  full command on its own line, working dir and user, exposure explained in a short
  parenthetical, the one-port and `--stop` commands, and the hidden-sockets note.
- Hidden-sockets and unknown-owner hints become one short dim line in both views,
  e.g. `other users' sockets hidden; run with sudo`.
- Subtle styling with Lip Gloss (dim labels, bold names) only when stdout is a
  terminal and NO_COLOR is unset; plain text otherwise. Width from the terminal, 100
  columns when not a terminal.
- The TUI detail pane and `--detail` share one renderer; the TUI table reuses the
  same shortening rules for commands and bindings.
- Update README examples and docs/validation.md with the new output; tests compare
  golden strings for both views at a fixed width with styling off.

## Expected outcome
`portpeek 3000` answers in three to four lines a developer can read in a second;
`portpeek 3000 --detail` shows everything in a tidy, scannable layout; JSON and exit
codes are untouched.
