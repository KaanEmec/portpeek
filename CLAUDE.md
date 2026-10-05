# Port Peek — working notes for agents

Read ARCHITECTURE.md first; it is the source of truth for layout, model, and decisions.

- Go, standard library only outside `internal/tui`; the one exception is `golang.org/x/term`. No telemetry, no network.
- OS commands stay inside `internal/inspect/<adapter>`; parsers are tested with
  fixtures in `testdata/`. Live-socket tests skip under `go test -short`.
- Inspection is read-only. Any process-changing action is opt-in and confirmed.
- Unknown data is labelled unavailable with a reason; never guessed.
- Before finishing: `gofmt -l .`, `go vet ./...`, `go test ./...`, `golangci-lint run`.
- Roadmap jobs live in `roadmap/`; update the status table in ARCHITECTURE.md when a
  version's epics complete.
