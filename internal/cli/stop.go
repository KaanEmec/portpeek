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
	if proceed, code := s.recheck(ctx, target); !proceed {
		return code
	}
	return s.terminate(target)
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

// recheck re-inspects the port right before signalling and requires that p
// still owns it under the same name, so a PID that exited or was reused since
// the first inspection never receives a signal.
func (s stopRun) recheck(ctx context.Context, p inspect.Process) (proceed bool, code int) {
	res, err := s.ins.Inspect(ctx, s.query)
	if errors.Is(err, context.Canceled) {
		return false, reportInterrupted(s.out.stderr)
	}
	if err != nil {
		s.errorf("could not recheck port %d: %v; nothing was stopped", s.query.Port, err)
		return false, exitNotStopped
	}

	i := slices.IndexFunc(res.Owners, func(o inspect.Owner) bool { return o.Process.PID == p.PID })
	if i < 0 || res.Owners[i].Process.Name != p.Name {
		s.errorf("process changed since inspection; nothing was stopped")
		return false, exitNotStopped
	}
	return true, exitOK
}

// terminate sends SIGTERM to p and reports whether it exited within
// stopWait. It never escalates to SIGKILL; it prints the command instead.
func (s stopRun) terminate(p inspect.Process) int {
	if err := s.sys.terminate(p.PID); err != nil {
		s.errorf("could not signal PID %d: %v", p.PID, err)
		return exitNotStopped
	}

	name := headlineProcess(p)
	if s.waitExit(p.PID) {
		_, _ = fmt.Fprintf(s.out.stdout, "Sent SIGTERM to %s; process exited.\n", name)
		return exitOK
	}
	_, _ = fmt.Fprintf(
		s.out.stdout,
		"Sent SIGTERM to %s; still running after %s. To force: kill -9 %d\n",
		name,
		stopWait,
		p.PID,
	)
	return exitOK
}

// waitExit polls until pid is gone or stopWait has passed.
func (s stopRun) waitExit(pid int) bool {
	for range int(stopWait / stopPollInterval) {
		if !s.sys.alive(pid) {
			return true
		}
		s.sys.sleep(stopPollInterval)
	}
	return !s.sys.alive(pid)
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
