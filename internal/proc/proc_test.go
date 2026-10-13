// Copied from crew's internal/proc/proc_test.go
// (github.com/thatsnotmynameio/crew) at commit
// 8b0b3b56209b9b4eb10511cfa3061389058d13f1, under crew's MIT license, which
// LICENSE in this directory holds. Only this header was added and the import
// of proc changed to this module's path. In the comments below, "crew" means
// the program that uses this package.

package proc_test

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
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

// output is a writer tests can read while a child still writes to it.
type output struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (o *output) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n, _ := o.buf.Write(p) // a bytes.Buffer's Write always returns a nil error
	return n, nil
}

func (o *output) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.buf.String()
}

// pid waits for the child to print "<name>=<pid>" and returns the pid, so a
// test acts only once the child has set up its traps and grandchildren.
func (o *output) pid(t *testing.T, name string) int {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		for line := range strings.Lines(o.String()) {
			if v, ok := strings.CutPrefix(strings.TrimSpace(line), name+"="); ok {
				pid, err := strconv.Atoi(v)
				if err != nil {
					t.Fatalf("bad pid line %q", line)
				}
				return pid
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("the child never printed %s=<pid>; output: %q", name, o.String())
	return 0
}

func sh(script string) proc.Command {
	return proc.Command{Name: "sh", Args: []string{"-c", script}}
}

func alive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}

// gone waits for pid to disappear, giving init a moment to reap an orphan.
func gone(t *testing.T, pid int, what string) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for alive(pid) {
		if time.Now().After(deadline) {
			_ = syscall.Kill(pid, syscall.SIGKILL)
			t.Fatalf("the %s (pid %d) is still running", what, pid)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitErr waits for p to end, failing the test after five seconds.
func waitErr(t *testing.T, p *proc.Process) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- p.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the child did not end")
		return nil
	}
}

func TestStartRunsTheChildInDirWithItsEnvAndWriters(t *testing.T) {
	var g proc.Group
	dir := t.TempDir()
	var stdout, stderr output
	c := sh(`pwd -P; echo "$CREW_TEST $GIT_TERMINAL_PROMPT $GH_PROMPT_DISABLED"; echo oops >&2; read line || echo eof`)
	c.Dir = dir
	c.Env = []string{"CREW_TEST=added", "GIT_TERMINAL_PROMPT=1"}

	p, err := g.Start(c, &stdout, &stderr)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := waitErr(t, p); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatal(err)
	}
	if want := resolved + "\nadded 0 1\neof\n"; stdout.String() != want {
		t.Errorf("stdout = %q, want %q", stdout.String(), want)
	}
	if stderr.String() != "oops\n" {
		t.Errorf("stderr = %q, want %q", stderr.String(), "oops\n")
	}
}

// run runs c to its end and returns its stdout.
func run(t *testing.T, c proc.Command) string {
	t.Helper()
	var g proc.Group
	out, err := g.Run(context.Background(), c)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return string(out.Stdout)
}

func TestStartRemovesTheInheritedVariablesInUnsetAndAddsEnv(t *testing.T) {
	t.Setenv("GH_TOKEN", "your-token")
	t.Setenv("GITHUB_TOKEN", "your-token")
	t.Setenv("CREW_KEPT", "kept")
	c := sh(`echo "${GH_TOKEN-unset}|${GITHUB_TOKEN-unset}|$GH_CONFIG_DIR|$CREW_KEPT"`)
	c.Env = []string{"GH_CONFIG_DIR=/run/crew/developer"}
	c.Unset = []string{"GH_TOKEN", "GITHUB_TOKEN"}

	if got, want := run(t, c), "unset|unset|/run/crew/developer|kept\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestUnsetCannotRemoveThePromptSettings(t *testing.T) {
	c := sh(`echo "$GIT_TERMINAL_PROMPT $GH_PROMPT_DISABLED"`)
	c.Unset = []string{"GIT_TERMINAL_PROMPT", "GH_PROMPT_DISABLED"}

	if got, want := run(t, c), "0 1\n"; got != want {
		t.Errorf("stdout = %q, want %q", got, want)
	}
}

func TestWaitReportsTheExitStatus(t *testing.T) {
	var g proc.Group
	p, err := g.Start(sh("exit 3"), nil, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	var exit *exec.ExitError
	if err := waitErr(t, p); !errors.As(err, &exit) || exit.ExitCode() != 3 {
		t.Errorf("Wait = %v, want exit status 3", err)
	}
}

func TestStartOfAMissingCommandFails(t *testing.T) {
	var g proc.Group
	if _, err := g.Start(proc.Command{Name: "crew-no-such-command"}, nil, nil); err == nil {
		t.Error("Start succeeded, want an error")
	}
}

func TestStopTerminatesTheChild(t *testing.T) {
	var g proc.Group
	var out output
	p, err := g.Start(sh(`echo child=$$; sleep 30`), &out, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := out.pid(t, "child")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	began := time.Now()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(began); elapsed > 2*time.Second {
		t.Errorf("Stop took %v, want the child to end on the terminate signal", elapsed)
	}
	if alive(pid) {
		t.Error("the child is still running after Stop returned")
	}
}

func TestStopKillsAChildThatTrapsTerminateOnceTheDeadlinePasses(t *testing.T) {
	var g proc.Group
	var out output
	p, err := g.Start(sh(`trap '' TERM; echo child=$$; while :; do sleep 1; done`), &out, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pid := out.pid(t, "child")

	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	began := time.Now()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if elapsed := time.Since(began); elapsed < 300*time.Millisecond {
		t.Errorf("Stop returned after %v, before the deadline", elapsed)
	}
	if alive(pid) {
		t.Error("the child is still running after Stop returned")
	}
	if err := waitErr(t, p); err == nil {
		t.Error("Wait = nil, want the kill reported")
	}
}

func TestStopEndsTheChildAndItsGrandchild(t *testing.T) {
	var g proc.Group
	var out output
	p, err := g.Start(sh(`sleep 30 & echo grandchild=$!; echo child=$$; wait`), &out, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	grandchild, child := out.pid(t, "grandchild"), out.pid(t, "child")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	gone(t, child, "child")
	gone(t, grandchild, "grandchild")
}

func TestWaitEndsWhatTheChildLeftBehind(t *testing.T) {
	var g proc.Group
	var out output
	// The grandchild keeps stdout open, so Wait must not wait for it.
	p, err := g.Start(sh(`sleep 30 & echo grandchild=$!`), &out, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	grandchild := out.pid(t, "grandchild")

	if err := waitErr(t, p); err != nil {
		t.Errorf("Wait = %v, want the child's clean exit", err)
	}
	gone(t, grandchild, "grandchild")
}

func TestKillAllEndsEveryStartedChild(t *testing.T) {
	var g proc.Group
	var plain, trapping output
	p1, err := g.Start(sh(`echo child=$$; sleep 30`), &plain, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	p2, err := g.Start(sh(`trap '' TERM; sleep 30 & echo grandchild=$!; echo child=$$; wait`), &trapping, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	pids := map[string]int{
		"child":               plain.pid(t, "child"),
		"trapping child":      trapping.pid(t, "child"),
		"trapping grandchild": trapping.pid(t, "grandchild"),
	}

	g.KillAll()
	for what, pid := range pids {
		gone(t, pid, what)
	}
	for _, p := range []*proc.Process{p1, p2} {
		if err := waitErr(t, p); err == nil {
			t.Error("Wait = nil, want the kill reported")
		}
	}
}

// TestInterruptOfCrewsProcessGroupDoesNotReachTheChild plays crew in a helper
// process with its own process group, as a terminal's foreground job, and
// sends that group the SIGINT a terminal's Ctrl-C sends.
func TestInterruptOfCrewsProcessGroupDoesNotReachTheChild(t *testing.T) {
	//nolint:gosec // os.Args[0] is this test binary, re-run as the helper process
	helper := exec.CommandContext(t.Context(), os.Args[0], "-test.run=^TestHelperCrew$")
	helper.Env = append(os.Environ(), "CREW_PROC_HELPER=1")
	helper.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	stdout, err := helper.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := helper.Start(); err != nil {
		t.Fatal(err)
	}
	child := 0
	scanner := bufio.NewScanner(stdout)
	for child == 0 && scanner.Scan() {
		if v, ok := strings.CutPrefix(scanner.Text(), "child="); ok {
			child, _ = strconv.Atoi(v)
		}
	}
	if child == 0 {
		_ = helper.Process.Kill()
		t.Fatalf("the helper never reported its child: %v", scanner.Err())
	}
	t.Cleanup(func() { _ = syscall.Kill(-child, syscall.SIGKILL) })

	if err := syscall.Kill(-helper.Process.Pid, syscall.SIGINT); err != nil {
		t.Fatal(err)
	}
	if err := helper.Wait(); err == nil {
		t.Fatal("the helper survived SIGINT; the test proves nothing")
	}
	time.Sleep(200 * time.Millisecond) // time for an interrupted child to end
	if !alive(child) {
		t.Error("the child ended when crew's process group was interrupted")
	}
}

// TestHelperCrew is the helper process of
// TestInterruptOfCrewsProcessGroupDoesNotReachTheChild; it does nothing in a
// normal test run.
func TestHelperCrew(_ *testing.T) {
	if os.Getenv("CREW_PROC_HELPER") != "1" {
		return
	}
	var g proc.Group
	p, err := g.Start(sh(`echo child=$$; exec sleep 30`), os.Stdout, os.Stderr)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	_ = p.Wait()
	os.Exit(0)
}

func TestRunCapturesTheOutput(t *testing.T) {
	var g proc.Group
	out, err := g.Run(context.Background(), sh(`echo out; echo err >&2`))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if string(out.Stdout) != "out\n" || string(out.Stderr) != "err\n" {
		t.Errorf("Run = %q, %q; want out and err", out.Stdout, out.Stderr)
	}
}

func TestRunOfAFailingCommandReportsItsStatusAndStderr(t *testing.T) {
	var g proc.Group
	out, err := g.Run(context.Background(), sh(`echo "not found" >&2; exit 4`))
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 4 {
		t.Fatalf("Run = %v, want exit status 4", err)
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("error %q does not carry stderr", err)
	}
	if string(out.Stderr) != "not found\n" {
		t.Errorf("Stderr = %q, want it kept", out.Stderr)
	}
}

func TestRunPastItsDeadlineKillsTheCommand(t *testing.T) {
	var g proc.Group
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	began := time.Now()
	_, err := g.Run(ctx, sh(`trap '' TERM; sleep 30`))
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Run = %v, want the deadline exceeded", err)
	}
	if elapsed := time.Since(began); elapsed > 3*time.Second {
		t.Errorf("Run took %v, want it to end at the deadline", elapsed)
	}
}

// pids waits for the file at path to hold "<child> <grandchild>", as the
// detached tests' command writes it, and returns both pids.
func pids(t *testing.T, path string) (int, int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path)
		if f := strings.Fields(string(data)); err == nil && len(f) == 2 {
			child, cerr := strconv.Atoi(f[0])
			grandchild, gerr := strconv.Atoi(f[1])
			if cerr != nil || gerr != nil {
				t.Fatalf("bad pids %q", data)
			}
			return child, grandchild
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("the detached command never wrote its pids")
	return 0, 0
}

func TestStartDetachedLeavesWhatTheCommandStartedRunning(t *testing.T) {
	var g proc.Group
	path := filepath.Join(t.TempDir(), "pids")
	// The command starts a grandchild and exits at once, as xdg-open does
	// once it has started the browser.
	c := sh(`sleep 30 & echo "$$ $!" > "$1.tmp" && mv "$1.tmp" "$1"`)
	c.Args = append(c.Args, "sh", path)
	if err := proc.StartDetached(c); err != nil {
		t.Fatalf("StartDetached: %v", err)
	}
	child, grandchild := pids(t, path)
	t.Cleanup(func() { _ = syscall.Kill(grandchild, syscall.SIGKILL) })

	gone(t, child, "detached command") // reaped, so no zombie stays
	g.KillAll()                        // the Group never recorded it
	if !alive(grandchild) {
		t.Fatal("the detached command's grandchild ended with it")
	}
	// setsid also makes the command the leader of a new process group.
	pgid, err := syscall.Getpgid(grandchild)
	if err != nil {
		t.Fatal(err)
	}
	if pgid != child || pgid == syscall.Getpgrp() {
		t.Errorf("the grandchild's process group is %d, want the detached command's own %d", pgid, child)
	}
}

func TestStartDetachedOfAMissingCommandFails(t *testing.T) {
	if err := proc.StartDetached(proc.Command{Name: "crew-no-such-command"}); err == nil {
		t.Error("StartDetached succeeded, want an error")
	}
}
