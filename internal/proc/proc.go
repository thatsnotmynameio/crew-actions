// Copied from crew's internal/proc/proc.go (github.com/thatsnotmynameio/crew)
// at commit 8b0b3b56209b9b4eb10511cfa3061389058d13f1, under crew's MIT
// license, which LICENSE in this directory holds. Only this header was added.
// In the comments below, "crew" means the program that uses this package.

// Package proc is the one way crew starts a child process, such as claude,
// gh or git. Each child runs in its own process group, so a terminal's Ctrl-C
// reaches crew alone and crew decides when its children stop; and every
// signal goes to the child's whole group, so the tools and servers a child
// spawned stop with it. A Group records the children it started, so a forced
// exit can kill every one of them.
//
// The one exception is StartDetached, for a program that hands work to the
// desktop, such as the browser opener: it runs in a session of its own, and
// no Group records, waits for or kills it.
//
// It works on Unix systems (darwin and linux), where process groups exist.
package proc

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"sync"
	"syscall"
	"time"
)

// drainTimeout bounds how long a process waits, once its group has ended,
// for its output pipes to close. Only a descendant that left the group, such
// as a daemon calling setsid, can hold them open past that.
const drainTimeout = 2 * time.Second

// Command is a child process to run.
type Command struct {
	// Name is the program, looked up in PATH when it holds no slash.
	Name string
	// Args are the arguments after the program name.
	Args []string
	// Dir is the working directory; empty means crew's.
	Dir string
	// Env holds KEY=value entries added to crew's environment, winning over
	// crew's own. GIT_TERMINAL_PROMPT=0 and GH_PROMPT_DISABLED=1 are always
	// set and win over Env: a child in a background process group that read
	// the terminal would stop forever.
	Env []string
	// Unset names variables of crew's environment the child must not
	// inherit, such as a token that would act as someone else. It removes
	// only what crew has: Env and the prompt settings are still added.
	Unset []string
}

// Output is what a command run to completion printed.
type Output struct {
	Stdout, Stderr []byte
}

// Group starts child processes and records the live ones. Its zero value is
// ready to use, and it is safe for concurrent use; it must not be copied
// after first use. Every child gets its own process group.
type Group struct {
	mu   sync.Mutex
	live map[*Process]struct{}
}

// Process is a started child and its process group.
type Process struct {
	pgid   int
	err    error         // set before exited is closed
	exited chan struct{} // closed once the child itself has exited
	done   chan struct{} // closed once its group is gone and its output copied
}

// Start starts c in a new process group, with stdin reading nothing and its
// stdout and stderr copied to the given writers; a nil writer discards. When
// stdout and stderr are the same writer, the child gets one pipe for both, so
// the writer is never written from two goroutines.
//
// When the child exits, whatever it left running in its group is killed: a
// command's helpers end with it.
func (g *Group) Start(c Command, stdout, stderr io.Writer) (*Process, error) {
	// proc is the one way crew starts a child: the program and its arguments
	// come from crew's adapters, never from a shell.
	//nolint:noctx // Stop and KillAll end the child's whole group; a context would kill its pid alone
	cmd := exec.Command(c.Name, c.Args...) //nolint:gosec // running the program its callers name is proc's job
	cmd.Dir = c.Dir
	cmd.Env = environ(c)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	pipes, err := attachPipes(cmd, stdout, stderr)
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", c.Name, err)
	}
	proc, err := g.launch(cmd, c.Name)
	if err != nil {
		closePipes(pipes)
		return nil, err
	}

	var copies sync.WaitGroup
	for _, p := range pipes {
		_ = p.w.Close() // the child holds its own copy
		copies.Go(func() { _, _ = io.Copy(p.dst, p.r) })
	}
	go g.reap(proc, cmd, pipes, &copies)
	return proc, nil
}

// Wait waits for the process to end and returns how the child exited: nil
// for status 0, or an error wrapping *exec.ExitError. It may be called more
// than once, and from any goroutine.
func (p *Process) Wait() error {
	<-p.done
	return p.err
}

// Stop sends SIGTERM to the child's process group and, if the child has not
// exited when ctx is done, SIGKILL. It returns once the child has exited and
// its group has been killed; an error means the group could not be
// signalled. Stopping a process that already ended does nothing.
func (p *Process) Stop(ctx context.Context) error {
	select {
	case <-p.exited:
		<-p.done
		return nil
	default:
	}
	if err := signal(p.pgid, syscall.SIGTERM); err != nil {
		return err
	}
	select {
	case <-p.exited:
	case <-ctx.Done():
		if err := signal(p.pgid, syscall.SIGKILL); err != nil {
			return err
		}
	}
	<-p.done
	return nil
}

// KillAll sends SIGKILL to the process group of every live child and returns
// once they have all ended. It is for crew's forced exit, and for any exit
// that skips the engine's stop sequence.
func (g *Group) KillAll() {
	g.mu.Lock()
	live := make([]*Process, 0, len(g.live))
	for p := range g.live {
		live = append(live, p)
	}
	g.mu.Unlock()
	for _, p := range live {
		_ = signal(p.pgid, syscall.SIGKILL)
	}
	for _, p := range live {
		<-p.done
	}
}

// StartDetached starts c in a session of its own, with stdin, stdout and
// stderr on the null device, and returns once it has started. No Group
// records it: crew neither waits for it nor kills it, so what it starts,
// such as the browser xdg-open opens, outlives it, and a command that stays
// in the foreground, as xdg-open can until the browser closes, holds nothing
// up. Only a failure to start is an error. A goroutine reaps the command
// once it exits, so it leaves no zombie.
func StartDetached(c Command) error {
	//nolint:noctx // a detached command is never killed, so no context may end it
	cmd := exec.Command(c.Name, c.Args...) //nolint:gosec // running the program its callers name is proc's job
	cmd.Dir = c.Dir
	cmd.Env = environ(c)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start %s: %w", c.Name, err)
	}
	go func() { _ = cmd.Wait() }()
	return nil
}

// Runner is the shape of Group.Run, so a caller can take a Group's Run and
// tests can inject a scripted one.
type Runner func(ctx context.Context, c Command) (Output, error)

// Run runs c to completion and returns what it printed. A non-zero exit is
// an error wrapping *exec.ExitError, with the command's stderr in its
// message. When ctx is done first, Run kills the command's process group and
// returns an error wrapping ctx.Err(), so a timeout reads as
// context.DeadlineExceeded.
func (g *Group) Run(ctx context.Context, c Command) (Output, error) {
	if err := ctx.Err(); err != nil {
		return Output{}, fmt.Errorf("%s: %w", c.Name, err)
	}
	var stdout, stderr bytes.Buffer
	p, err := g.Start(c, &stdout, &stderr)
	if err != nil {
		return Output{}, err
	}
	var cancelled error
	select {
	case <-p.done:
	case <-ctx.Done():
		cancelled = ctx.Err()
		_ = signal(p.pgid, syscall.SIGKILL)
		<-p.done
	}
	out := Output{Stdout: stdout.Bytes(), Stderr: stderr.Bytes()}
	switch {
	case cancelled != nil:
		return out, fmt.Errorf("%s: %w", c.Name, cancelled)
	case p.err != nil:
		if msg := strings.TrimSpace(string(out.Stderr)); msg != "" {
			return out, fmt.Errorf("%s: %w: %s", c.Name, p.err, msg)
		}
		return out, fmt.Errorf("%s: %w", c.Name, p.err)
	}
	return out, nil
}

// launch starts cmd, the command named name, and records it as a live child.
func (g *Group) launch(cmd *exec.Cmd, name string) (*Process, error) {
	proc := &Process{exited: make(chan struct{}), done: make(chan struct{})}
	// Holding the lock across the start means KillAll never misses a child
	// that started before it.
	g.mu.Lock()
	defer g.mu.Unlock()
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", name, err)
	}
	proc.pgid = cmd.Process.Pid
	if g.live == nil {
		g.live = make(map[*Process]struct{})
	}
	g.live[proc] = struct{}{}
	return proc, nil
}

// reap waits for the child, kills what it left in its group, lets the output
// drain, and then marks the process done.
func (g *Group) reap(p *Process, cmd *exec.Cmd, pipes []*pipe, copies *sync.WaitGroup) {
	p.err = cmd.Wait()
	close(p.exited)
	// The child is reaped, so the group outlives it only through its
	// descendants; until they are gone, its id cannot be reused.
	_ = signal(p.pgid, syscall.SIGKILL)

	copied := make(chan struct{})
	go func() {
		copies.Wait()
		close(copied)
	}()
	select {
	case <-copied:
	case <-time.After(drainTimeout):
		for _, pp := range pipes {
			_ = pp.r.Close() // unblocks the copy
		}
		<-copied
	}
	for _, pp := range pipes {
		_ = pp.r.Close()
	}

	g.mu.Lock()
	delete(g.live, p)
	g.mu.Unlock()
	close(p.done)
}

// environ returns c's environment: crew's without the variables c.Unset
// names, then c.Env, then the settings that keep git and gh from prompting.
func environ(c Command) []string {
	env := slices.DeleteFunc(os.Environ(), func(kv string) bool {
		name, _, _ := strings.Cut(kv, "=")
		return slices.Contains(c.Unset, name)
	})
	return append(append(env, c.Env...), "GIT_TERMINAL_PROMPT=0", "GH_PROMPT_DISABLED=1")
}

// signal sends sig to process group pgid. A group that is already gone is
// not an error.
func signal(pgid int, sig syscall.Signal) error {
	err := syscall.Kill(-pgid, sig)
	if err == nil || errors.Is(err, syscall.ESRCH) {
		return nil
	}
	return fmt.Errorf("signal process group %d: %w", pgid, err)
}

// pipe carries one output stream of a child to its writer.
type pipe struct {
	r, w *os.File
	dst  io.Writer
}

func newPipe(dst io.Writer) (*pipe, error) {
	r, w, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("output pipe: %w", err)
	}
	return &pipe{r: r, w: w, dst: dst}, nil
}

// attachPipes gives cmd a pipe to each writer that is not nil, one pipe for
// both when stdout and stderr are the same writer, and returns the pipes.
func attachPipes(cmd *exec.Cmd, stdout, stderr io.Writer) ([]*pipe, error) {
	var pipes []*pipe
	if stdout != nil {
		p, err := newPipe(stdout)
		if err != nil {
			return nil, err
		}
		pipes = append(pipes, p)
		cmd.Stdout = p.w
	}
	switch {
	case stderr == nil:
	case sameWriter(stdout, stderr):
		cmd.Stderr = cmd.Stdout
	default:
		p, err := newPipe(stderr)
		if err != nil {
			closePipes(pipes)
			return nil, err
		}
		pipes = append(pipes, p)
		cmd.Stderr = p.w
	}
	return pipes, nil
}

// closePipes closes both ends of every pipe.
func closePipes(pipes []*pipe) {
	for _, p := range pipes {
		_ = p.r.Close()
		_ = p.w.Close()
	}
}

// sameWriter reports whether a and b are the same writer, without panicking
// on writers whose dynamic type cannot be compared.
func sameWriter(a, b io.Writer) bool {
	return a != nil && reflect.TypeOf(a) == reflect.TypeOf(b) && reflect.TypeOf(a).Comparable() && a == b
}
