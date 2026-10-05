//go:build darwin

package cli

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/kaanemec/portpeek/internal/inspect/lsof"
)

const stopHelperEnv = "PORTPEEK_STOP_HELPER"

// TestStopHelperListener is not a real test: TestRun_StopLive re-executes the
// test binary to run it as a child process that holds a TCP listener, prints
// its port, and waits to be stopped.
func TestStopHelperListener(t *testing.T) {
	if os.Getenv(stopHelperEnv) != "1" {
		t.Skip("helper process for TestRun_StopLive")
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		os.Exit(2)
	}
	_, _ = os.Stdout.WriteString(strconv.Itoa(ln.Addr().(*net.TCPAddr).Port) + "\n")
	time.Sleep(30 * time.Second)
	os.Exit(0)
}

// TestRun_StopLive stops a real child process through the real lsof
// inspector and real signals. It is skipped with -short.
func TestRun_StopLive(t *testing.T) {
	if testing.Short() {
		t.Skip("live stop test skipped in -short mode")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
	t.Cleanup(cancel)

	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestStopHelperListener$")
	cmd.Env = append(os.Environ(), stopHelperEnv+"=1")
	childOut, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatalf("stdout pipe: %v", err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	// Reap the child as soon as it exits so the liveness check does not see
	// a zombie.
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	t.Cleanup(func() {
		_ = cmd.Process.Kill()
		<-done
	})

	line, err := bufio.NewReader(childOut).ReadString('\n')
	if err != nil {
		t.Fatalf("reading helper port: %v", err)
	}
	port := strings.TrimSpace(line)

	var stdout, stderr bytes.Buffer
	code := Run(ctx, []string{port, "--tcp", "--stop", "--force"}, &stdout, &stderr, Deps{Inspector: lsof.New()})

	if code != exitOK {
		t.Fatalf("exit code = %d, want %d\nstdout:\n%s\nstderr:\n%s", code, exitOK, stdout.String(), stderr.String())
	}
	if !strings.Contains(stdout.String(), "; process exited.\n") {
		t.Errorf("stdout does not report the exit:\n%s", stdout.String())
	}
	t.Logf("stdout:\n%s", stdout.String())
	select {
	case err := <-done:
		done <- nil // let the cleanup's receive succeed
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) || exitErr.Sys().(syscall.WaitStatus).Signal() != syscall.SIGTERM {
			t.Errorf("helper exit = %v, want termination by SIGTERM", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("helper still running after --stop")
	}
}
