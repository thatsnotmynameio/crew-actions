package local

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/proc"
)

func TestSetupGivesANewEmptyDirectory(t *testing.T) {
	b := newBackend(t)
	first, second := setup(t, b), setup(t, b)
	if first.dir == second.dir {
		t.Fatalf("two setups gave the same directory %s", first.dir)
	}
	for _, ws := range []*workspace{first, second} {
		entries, err := os.ReadDir(ws.dir)
		if err != nil {
			t.Fatalf("ReadDir: %v", err)
		}
		if len(entries) != 0 {
			t.Errorf("%s holds %d entries, want none", ws.dir, len(entries))
		}
		if base := filepath.Base(ws.dir); !strings.HasPrefix(base, "crew-actions-") {
			t.Errorf("directory %s does not start with crew-actions-", base)
		}
	}
}

func TestSetupUnderAMissingBaseDirectoryFails(t *testing.T) {
	b := &Backend{Group: new(proc.Group), BaseDir: filepath.Join(t.TempDir(), "missing")}
	if _, err := b.Setup(); err == nil {
		t.Error("Setup = nil error, want one")
	}
}

func TestStepRunsAtTheWorkspaceRoot(t *testing.T) {
	ws := setup(t, newBackend(t))
	out, status := run(t, ws, "pwd")
	if status != 0 {
		t.Fatalf("status = %d, want 0", status)
	}
	if got, want := strings.TrimSpace(out), realPath(t, ws.dir); got != want {
		t.Errorf("pwd = %q, want %q", got, want)
	}
}

func TestStepRuns(t *testing.T) {
	tests := []struct {
		name     string
		commands []string
		want     string
		status   int
	}{
		{"commands in order", []string{"echo one", "echo two"}, "one\ntwo\n", 0},
		{"stdout then stderr", []string{"echo out; echo err >&2"}, "out\nerr\n", 0},
		{"export carries", []string{"export X=1", `echo "$X"`}, "1\n", 0},
		{"a failure stops", []string{"false", "echo after"}, "", 1},
		{"an exit status is the step's", []string{"true", "exit 3", "echo after"}, "", 3},
		{"a continuation does not swallow the check", []string{`echo a \`, "false"}, "a\n", 1},
		{"set -e in a command", []string{"set -e; false; echo after"}, "", 1},
		{"a failing && list", []string{"true && false"}, "", 1},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			out, status := run(t, setup(t, newBackend(t)), tc.commands...)
			if status != tc.status {
				t.Errorf("status = %d, want %d", status, tc.status)
			}
			if out != tc.want {
				t.Errorf("output = %q, want %q", out, tc.want)
			}
		})
	}
}

func TestStepWithASyntaxErrorFails(t *testing.T) {
	_, status := run(t, setup(t, newBackend(t)), "echo first", `echo "unbalanced`)
	if status == 0 {
		t.Error("status = 0, want non-zero")
	}
}

// An unterminated heredoc swallows the commands after it: the step exits 0
// and false never runs. This pins the script's shape, so changing it is
// deliberate.
func TestStepWithAnUnterminatedHeredocSwallowsTheRest(t *testing.T) {
	_, status := run(t, setup(t, newBackend(t)), "cat <<EOF", "false")
	if status != 0 {
		t.Errorf("status = %d, want 0", status)
	}
}

func TestStepsStartAtTheRootWithAFreshShell(t *testing.T) {
	ws := setup(t, newBackend(t))
	out, status := run(t, ws, "mkdir sub", "cd sub", "export X=1", "pwd")
	if status != 0 {
		t.Fatalf("first step: status = %d, want 0", status)
	}
	root := realPath(t, ws.dir)
	if got, want := strings.TrimSpace(out), filepath.Join(root, "sub"); got != want {
		t.Errorf("first step: pwd = %q, want %q", got, want)
	}

	out, status = run(t, ws, "pwd", `echo "${X:-unset}"`)
	if status != 0 {
		t.Fatalf("second step: status = %d, want 0", status)
	}
	if want := root + "\nunset\n"; out != want {
		t.Errorf("second step: output = %q, want %q", out, want)
	}
}

func TestCancelledStepIsStopped(t *testing.T) {
	ws := setup(t, newBackend(t))
	start := time.Now()
	pgid, err := runCancelled(t, ws, "echo $$", "sleep 30")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want the context's cancellation", err)
	}
	if elapsed := time.Since(start); elapsed >= defaultGrace {
		t.Errorf("the step stopped after %s, not within the grace period", elapsed)
	}
	waitGone(t, pgid)
}

func TestCancelledStepIgnoringSIGTERMIsKilled(t *testing.T) {
	b := newBackend(t)
	b.Grace = 100 * time.Millisecond
	ws := setup(t, b)
	start := time.Now()
	pgid, err := runCancelled(t, ws, "trap '' TERM; echo $$; sleep 30")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Run = %v, want the context's cancellation", err)
	}
	if elapsed := time.Since(start); elapsed < b.Grace || elapsed > 10*time.Second {
		t.Errorf("the step stopped after %s, want just past the grace period %s", elapsed, b.Grace)
	}
	waitGone(t, pgid)
}

func TestStepWhoseShellIsKilledHasNoStatus(t *testing.T) {
	ws := setup(t, newBackend(t))
	_, err := ws.Run(context.Background(), []string{"kill -KILL $$"}, nil)
	if _, ok := errors.AsType[*exec.ExitError](err); !ok {
		t.Errorf("Run = %v, want an error wrapping the shell's exit", err)
	}
}

func TestStepInARemovedWorkspaceFailsToStart(t *testing.T) {
	ws := setup(t, newBackend(t))
	if err := os.RemoveAll(ws.dir); err != nil {
		t.Fatalf("RemoveAll: %v", err)
	}
	_, err := ws.Run(context.Background(), []string{"true"}, nil)
	if err == nil {
		t.Fatal("Run = nil error, want one")
	}
	if _, ok := errors.AsType[*exec.ExitError](err); ok {
		t.Errorf("Run = %v, want no exit status", err)
	}
}

func TestDestroyRemovesTheWorkspace(t *testing.T) {
	tests := []struct {
		name     string
		commands []string
	}{
		{"what a step wrote", []string{"mkdir -p a/b && echo x > a/b/f && touch g"}},
		{"a read-only directory", []string{"mkdir d && touch d/f && chmod 555 d"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			ws := setup(t, newBackend(t))
			if _, status := run(t, ws, tc.commands...); status != 0 {
				t.Fatalf("status = %d, want 0", status)
			}
			if err := ws.Destroy(); err != nil {
				t.Fatalf("Destroy: %v", err)
			}
			if _, err := os.Stat(ws.dir); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("stat %s = %v, want it gone", ws.dir, err)
			}
		})
	}
}

func TestDestroyOfARemovedWorkspaceSucceeds(t *testing.T) {
	ws := setup(t, newBackend(t))
	if err := ws.Destroy(); err != nil {
		t.Fatalf("first Destroy: %v", err)
	}
	if err := ws.Destroy(); err != nil {
		t.Errorf("second Destroy = %v, want nil", err)
	}
}

// newBackend returns a backend whose workspaces live under a temporary
// directory of the test.
func newBackend(t *testing.T) *Backend {
	t.Helper()
	return &Backend{Group: new(proc.Group), BaseDir: t.TempDir()}
}

// setup sets up a workspace of b and destroys it when the test ends.
func setup(t *testing.T, b *Backend) *workspace {
	t.Helper()
	ws, err := b.Setup()
	if err != nil {
		t.Fatalf("Setup: %v", err)
	}
	t.Cleanup(func() { _ = ws.Destroy() })
	local, ok := ws.(*workspace)
	if !ok {
		t.Fatalf("Setup = %T, want *workspace", ws)
	}
	return local
}

// run runs a step of commands in ws and returns its output and status.
func run(t *testing.T, ws *workspace, commands ...string) (string, int) {
	t.Helper()
	var out bytes.Buffer
	status, err := ws.Run(context.Background(), commands, &out)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return out.String(), status
}

// runCancelled runs a step of commands in ws, whose first line of output is
// the shell's pid, cancels it once that line is printed, and returns the pid,
// which is also the step's process group, and Run's error.
func runCancelled(t *testing.T, ws *workspace, commands ...string) (int, error) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	out := &lineWriter{line: make(chan struct{})}
	go func() {
		<-out.line
		cancel()
	}()
	_, err := ws.Run(ctx, commands, out)
	pid, perr := strconv.Atoi(strings.TrimSpace(out.String()))
	if perr != nil {
		t.Fatalf("the step's pid: %v", perr)
	}
	return pid, err
}

// waitGone fails the test unless process group pgid is gone within a few
// seconds: the step's children are reaped by init once its shell is gone.
func waitGone(t *testing.T, pgid int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(-pgid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("process group %d is still there: kill = %v", pgid, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// realPath returns path with its symlinks resolved, as pwd prints it: the
// temporary directory of macOS sits under a symlink.
func realPath(t *testing.T, path string) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		t.Fatalf("EvalSymlinks: %v", err)
	}
	return resolved
}

// lineWriter collects what it is written and closes line at the first
// newline. It is safe for concurrent use.
type lineWriter struct {
	mu   sync.Mutex
	buf  bytes.Buffer
	line chan struct{}
	once sync.Once
}

func (w *lineWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	_, _ = w.buf.Write(p) // a bytes.Buffer's Write returns no error
	if bytes.Contains(w.buf.Bytes(), []byte("\n")) {
		w.once.Do(func() { close(w.line) })
	}
	return len(p), nil
}

func (w *lineWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buf.String()
}
