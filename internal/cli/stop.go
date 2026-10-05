package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"golang.org/x/term"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// After SIGTERM, liveness is checked every stopPollInterval for up to stopWait.
const (
	stopWait         = 2 * time.Second
	stopPollInterval = 100 * time.Millisecond
)

// stopper holds every side effect of the --stop flow. Run uses
// systemStopper; tests replace the functions with fakes.
type stopper struct {
	// stdin is where the confirmation answer is read from.
	stdin io.Reader
	// isTerminal reports whether stdin is an interactive terminal.
	isTerminal func() bool
	// terminate sends SIGTERM to pid.
	terminate func(pid int) error
	// alive reports whether pid still exists.
	alive func(pid int) bool
	// sleep pauses between liveness checks.
	sleep func(time.Duration)
}

// systemStopper returns the stopper that acts on real processes.
func systemStopper() stopper {
	return stopper{
		stdin:      os.Stdin,
		isTerminal: stdinIsTerminal,
		terminate:  terminate,
		alive:      alive,
		sleep:      time.Sleep,
	}
}

// stdinIsTerminal reports whether stdin is an interactive terminal. A mode
// check is not enough: /dev/null is also a character device.
func stdinIsTerminal() bool {
	return term.IsTerminal(int(os.Stdin.Fd()))
}

// stopRun is one execution of the --stop flow for a query whose owners have
// already been printed.
type stopRun struct {
	sys   stopper
	ins   inspect.Inspector
	out   stdio
	query inspect.Query
}

// stop chooses the target, confirms, rechecks its identity and sends SIGTERM.
// owners must not be empty.
func (s stopRun) stop(ctx context.Context, owners []inspect.Owner, opts options) int {
	target, ok := s.choose(owners, opts.pid)
	if !ok {
		return exitNotStopped
	}
	if !opts.force {
		if proceed, code := s.confirm(ctx, target); !proceed {
			return code
		}
	}

	res := stopVerified(ctx, s.ins, s.query, target, s.sys)
	switch {
	case res.Interrupted:
		return reportInterrupted(s.out.stderr)
	case !res.Sent:
		s.errorf("%s", res.Message)
		return exitNotStopped
	default:
		_, _ = fmt.Fprintln(s.out.stdout, res.Message)
		return exitOK
	}
}

// choose picks the process to stop and refuses an unknown owner, which has no
// PID that could safely be signalled.
func (s stopRun) choose(owners []inspect.Owner, pid int) (inspect.Process, bool) {
	target, ok := s.pick(owners, pid)
	if !ok {
		return inspect.Process{}, false
	}
	if isUnknownOwner(target) {
		s.errorf("owner of port %d is unknown; nothing was stopped", s.query.Port)
		return inspect.Process{}, false
	}
	return target, true
}

// pick selects the owner named by --pid, or the only owner. Several owners
// require an explicit --pid; the tool never chooses among them. When every
// owner is unknown there is nothing to choose, so the first one is returned
// for choose to refuse.
func (s stopRun) pick(owners []inspect.Owner, pid int) (inspect.Process, bool) {
	if pid != 0 {
		i := slices.IndexFunc(owners, func(o inspect.Owner) bool { return o.Process.PID == pid })
		if i < 0 {
			s.errorf("PID %d does not use port %d; nothing was stopped", pid, s.query.Port)
			return inspect.Process{}, false
		}
		return owners[i].Process, true
	}

	allUnknown := !slices.ContainsFunc(owners, func(o inspect.Owner) bool { return !isUnknownOwner(o.Process) })
	if len(owners) == 1 || allUnknown {
		return owners[0].Process, true
	}

	var b strings.Builder
	fmt.Fprintf(&b, "portpeek: port %d is used by several processes; choose one with --pid <pid>:\n", s.query.Port)
	for _, o := range owners {
		p := o.Process
		pidText := strconv.Itoa(p.PID)
		if isUnknownOwner(p) {
			pidText = "unknown"
		}
		fmt.Fprintf(&b, "  %-8s %s\n", pidText, processField(p, inspect.FieldName, p.Name))
	}
	_, _ = io.WriteString(s.out.stderr, b.String())
	return inspect.Process{}, false
}

// confirm asks on the terminal before stopping p. Without a terminal it
// refuses, because --force is the only way to consent non-interactively.
func (s stopRun) confirm(ctx context.Context, p inspect.Process) (proceed bool, code int) {
	if !s.sys.isTerminal() {
		s.errorf("refusing to stop without confirmation; re-run with --force in non-interactive use")
		return false, exitNotStopped
	}

	_, _ = fmt.Fprintf(s.out.stderr, "Send SIGTERM to %s? [y/N] ", headlineProcess(p))
	answer, err := readLine(ctx, s.sys.stdin)
	if err != nil {
		_, _ = fmt.Fprintln(s.out.stderr)
		return false, reportInterrupted(s.out.stderr)
	}
	switch strings.ToLower(strings.TrimSpace(answer)) {
	case "y", "yes":
		return true, exitOK
	default:
		_, _ = fmt.Fprintln(s.out.stderr, "Not stopped.")
		return false, exitNotStopped
	}
}

// StopResult is the outcome of StopVerified.
type StopResult struct {
	// Sent reports whether SIGTERM was delivered.
	Sent bool
	// Exited reports whether the process was gone within the wait after
	// SIGTERM. It is false when nothing was sent.
	Exited bool
	// Interrupted reports that ctx ended during the recheck; nothing was
	// sent and Message is empty.
	Interrupted bool
	// Message is the one-line report in the CLI's wording, without the
	// "portpeek: " prefix or a trailing newline: the outcome when Sent, or
	// why nothing was stopped.
	Message string
}

// StopVerified stops target, a process the caller showed as owning q's port
// and the user confirmed. It re-inspects the port right before signalling and
// requires that target's PID still owns it under the same name, so a PID that
// exited or was reused since it was shown never receives a signal. It then
// sends SIGTERM, waits up to 2s for the process to exit, and never escalates
// to SIGKILL. An unknown owner (PID 0) is always refused.
//
// Choosing among several owners and asking for confirmation stay with the
// caller; StopVerified is the part the CLI's --stop and the terminal
// interface share.
func StopVerified(ctx context.Context, ins inspect.Inspector, q inspect.Query, target inspect.Process) StopResult {
	return stopVerified(ctx, ins, q, target, systemStopper())
}

// stopVerified is StopVerified with the side effects injected.
func stopVerified(
	ctx context.Context,
	ins inspect.Inspector,
	q inspect.Query,
	target inspect.Process,
	sys stopper,
) StopResult {
	if isUnknownOwner(target) || target.PID < 0 {
		return StopResult{Message: fmt.Sprintf("owner of port %d is unknown; nothing was stopped", q.Port)}
	}

	res, err := ins.Inspect(ctx, q)
	if errors.Is(err, context.Canceled) {
		return StopResult{Interrupted: true}
	}
	if err != nil {
		return StopResult{Message: fmt.Sprintf("could not recheck port %d: %v; nothing was stopped", q.Port, err)}
	}
	i := slices.IndexFunc(res.Owners, func(o inspect.Owner) bool { return o.Process.PID == target.PID })
	if i < 0 || res.Owners[i].Process.Name != target.Name {
		return StopResult{Message: "process changed since inspection; nothing was stopped"}
	}

	if err := sys.terminate(target.PID); err != nil {
		return StopResult{Message: fmt.Sprintf("could not signal PID %d: %v", target.PID, err)}
	}

	name := headlineProcess(target)
	if waitExit(sys, target.PID) {
		return StopResult{
			Sent:    true,
			Exited:  true,
			Message: fmt.Sprintf("Sent SIGTERM to %s; process exited.", name),
		}
	}
	return StopResult{
		Sent: true,
		Message: fmt.Sprintf(
			"Sent SIGTERM to %s; still running after %s. To force: kill -9 %d",
			name,
			stopWait,
			target.PID,
		),
	}
}

// waitExit polls until pid is gone or stopWait has passed.
func waitExit(sys stopper, pid int) bool {
	for range int(stopWait / stopPollInterval) {
		if !sys.alive(pid) {
			return true
		}
		sys.sleep(stopPollInterval)
	}
	return !sys.alive(pid)
}

// errorf writes a "portpeek: " prefixed line to stderr.
func (s stopRun) errorf(format string, args ...any) {
	_, _ = fmt.Fprintf(s.out.stderr, "portpeek: "+format+"\n", args...)
}

// readLine reads one line from r, giving up when ctx ends. The reading
// goroutine is then left blocked on r; that is acceptable because the process
// exits right after an interrupt. A read error or EOF yields whatever was
// read, which an empty or partial answer treats as "no".
func readLine(ctx context.Context, r io.Reader) (string, error) {
	lines := make(chan string, 1)
	go func() {
		line, _ := bufio.NewReader(r).ReadString('\n')
		lines <- line
	}()

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case line := <-lines:
		return line, nil
	}
}
