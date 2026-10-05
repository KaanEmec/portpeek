package ss

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"testing"

	"github.com/kaanemec/portpeek/internal/inspect"
)

// fixtureCwd is the working directory the cwd symlink of PID 1005 points to.
const fixtureCwd = "/srv/sockets"

// procTree copies testdata/proc into a temporary directory and adds the
// cwd symlink of PID 1005, which is not committed because symlinks do not
// survive every checkout. It returns the new procfs root.
func procTree(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("procfs is Linux-only; Windows rewrites symlink targets with backslashes")
	}
	root := filepath.Join(t.TempDir(), "proc")
	if err := os.CopyFS(root, os.DirFS(filepath.Join("testdata", "proc"))); err != nil {
		t.Fatalf("copying proc fixture: %v", err)
	}
	if err := os.Symlink(fixtureCwd, filepath.Join(root, "1005", "cwd")); err != nil {
		t.Fatalf("creating cwd symlink: %v", err)
	}
	return root
}

// writeProcFile writes one file of a fake /proc/PID directory.
func writeProcFile(t *testing.T, root string, pid int, name, content string) {
	t.Helper()
	dir := filepath.Join(root, fmt.Sprint(pid))
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestInspector_enrich(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		pid         int
		ssName      string
		setup       func(t *testing.T, root string)
		want        inspect.Process
		wantReasons map[inspect.Field]string
	}{
		{
			name:   "every field readable",
			pid:    1005,
			ssName: "sockets",
			want: inspect.Process{
				PID:        1005,
				Name:       "sockets",
				User:       "root",
				Command:    "/tmp/sockets eth0",
				WorkingDir: fixtureCwd,
			},
			wantReasons: map[inspect.Field]string{},
		},
		{
			name:   "comm overrides the ss name",
			pid:    1005,
			ssName: "stale",
			want: inspect.Process{
				PID:        1005,
				Name:       "sockets",
				User:       "root",
				Command:    "/tmp/sockets eth0",
				WorkingDir: fixtureCwd,
			},
			wantReasons: map[inspect.Field]string{},
		},
		{
			name:   "process exited keeps the ss name",
			pid:    4242,
			ssName: "node",
			want:   inspect.Process{PID: 4242, Name: "node"},
			wantReasons: map[inspect.Field]string{
				inspect.FieldUser:       reasonExited,
				inspect.FieldCommand:    reasonExited,
				inspect.FieldWorkingDir: reasonExited,
			},
		},
		{
			name: "process exited without an ss name",
			pid:  4242,
			want: inspect.Process{PID: 4242},
			wantReasons: map[inspect.Field]string{
				inspect.FieldName:       reasonExited,
				inspect.FieldUser:       reasonExited,
				inspect.FieldCommand:    reasonExited,
				inspect.FieldWorkingDir: reasonExited,
			},
		},
		{
			name:   "zombie with empty cmdline and no cwd",
			pid:    77,
			ssName: "worker",
			setup: func(t *testing.T, root string) {
				writeProcFile(t, root, 77, "comm", "worker\n")
				writeProcFile(t, root, 77, "cmdline", "")
				writeProcFile(t, root, 77, "status", "Name:\tworker\nUid:\t0\t0\t0\t0\n")
			},
			want: inspect.Process{PID: 77, Name: "worker", User: "root"},
			wantReasons: map[inspect.Field]string{
				inspect.FieldCommand:    reasonUnknown,
				inspect.FieldWorkingDir: reasonUnknown,
			},
		},
		{
			name:   "status without a Uid line",
			pid:    78,
			ssName: "odd",
			setup: func(t *testing.T, root string) {
				writeProcFile(t, root, 78, "cmdline", "odd\x00")
				writeProcFile(t, root, 78, "status", "Name:\todd\n")
			},
			want: inspect.Process{PID: 78, Name: "odd", Command: "odd"},
			wantReasons: map[inspect.Field]string{
				inspect.FieldUser:       reasonUnknown,
				inspect.FieldWorkingDir: reasonUnknown,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := procTree(t)
			if tt.setup != nil {
				tt.setup(t, root)
			}
			i := newTestInspector(fakeRunner{}, root)
			p := proc(tt.pid, tt.ssName)

			i.enrich(&p)

			tt.want.Unavailable = tt.wantReasons
			if !reflect.DeepEqual(p, tt.want) {
				t.Errorf("enrich =\n%+v\nwant\n%+v", p, tt.want)
			}
		})
	}
}

// TestInspector_enrich_UnknownUID is not parallel: it replaces the user
// lookup.
func TestInspector_enrich_UnknownUID(t *testing.T) {
	old := lookupUsername
	lookupUsername = func(uid string) (string, error) {
		return "", fmt.Errorf("user: unknown userid %s", uid)
	}
	t.Cleanup(func() { lookupUsername = old })

	root := t.TempDir()
	writeProcFile(t, root, 90, "status", "Uid:\t100999\t100999\t100999\t100999\n")
	p := proc(90, "app")

	newTestInspector(fakeRunner{}, root).enrich(&p)

	if p.User != "100999" {
		t.Errorf("User = %q, want the numeric uid", p.User)
	}
	if _, ok := p.Unavailable[inspect.FieldUser]; ok {
		t.Errorf("Unavailable[user] = %q, want unset", p.Unavailable[inspect.FieldUser])
	}
}

func TestUnavailableReason(t *testing.T) {
	t.Parallel()

	permission := &fs.PathError{Op: "readlink", Path: "/proc/1/cwd", Err: fs.ErrPermission}
	missing := &fs.PathError{Op: "open", Path: "/proc/9/cmdline", Err: fs.ErrNotExist}
	tests := []struct {
		name   string
		err    error
		exited bool
		want   string
	}{
		{name: "permission denied", err: permission, want: reasonPermission},
		{name: "permission denied for an exited process", err: permission, exited: true, want: reasonExited},
		{name: "missing file of an exited process", err: missing, exited: true, want: reasonExited},
		{name: "missing file of a live process", err: missing, want: reasonUnknown},
		{name: "empty file", err: errEmpty, want: reasonUnknown},
		{name: "other error", err: errors.New("i/o error"), want: reasonUnknown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := unavailableReason(tt.err, tt.exited); got != tt.want {
				t.Errorf("unavailableReason(%v, %t) = %q, want %q", tt.err, tt.exited, got, tt.want)
			}
		})
	}
}

func TestReadUID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		status  string
		want    string
		wantErr bool
	}{
		{name: "real uid first", status: "Name:\tx\nUid:\t1000\t0\t0\t0\nGid:\t1000\t1000\t1000\t1000\n", want: "1000"},
		{name: "no Uid line", status: "Name:\tx\n", wantErr: true},
		{name: "empty Uid line", status: "Uid:\n", wantErr: true},
		{name: "non-numeric uid", status: "Uid:\tabc\n", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "status")
			if err := os.WriteFile(path, []byte(tt.status), 0o644); err != nil {
				t.Fatal(err)
			}
			got, err := readUID(path)
			if got != tt.want || (err != nil) != tt.wantErr {
				t.Errorf("readUID = %q, %v; want %q, error %t", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
