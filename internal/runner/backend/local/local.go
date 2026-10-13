// Package local is the backend that runs steps on the runner's machine. Each
// attempt gets a new, empty directory under the system's temporary
// directory, named crew-actions- and a random suffix, which is removed when
// the attempt ends.
//
// A step's commands run in one POSIX shell, /bin/sh -c, started at the
// workspace's root through proc, so each step has a process group of its
// own. After each command the shell exits with that command's status when it
// is not zero. The step inherits the runner's environment. Nothing isolates
// it: it can leave the workspace and write anywhere the runner can.
package local

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/proc"
	"github.com/thatsnotmynameio/crew-actions/internal/runner/backend"
)

// Compile-time guards.
var (
	_ backend.Backend   = (*Backend)(nil)
	_ backend.Workspace = (*workspace)(nil)
)

// defaultGrace is how long a step whose context ended gets to stop after
// SIGTERM before it is killed.
const defaultGrace = 10 * time.Second

// ownerWrite is the permission bit that lets a directory's owner remove its
// entries.
const ownerWrite = 0o200

// check follows each command of a step's script: the blank line before it
// ends a command whose last line is a \ continuation, and it exits with the
// command's status when that is not zero.
const check = "\ncrew_status=$?; [ \"$crew_status\" -eq 0 ] || exit \"$crew_status\"\n"

// Backend sets up workspaces in temporary directories. Group must be set;
// the other fields have defaults.
type Backend struct {
	// Group starts the steps' processes, so its owner can stop them all.
	Group *proc.Group
	// BaseDir is where workspaces are created; empty means os.TempDir().
	BaseDir string
	// Grace is how long a stopped step gets between SIGTERM and SIGKILL;
	// zero means 10 seconds.
	Grace time.Duration
}

// workspace is one attempt's directory.
type workspace struct {
	backend *Backend
	dir     string
}

// Setup implements backend.Backend: it creates a new, empty directory.
func (b *Backend) Setup() (backend.Workspace, error) {
	dir, err := os.MkdirTemp(b.BaseDir, "crew-actions-")
	if err != nil {
		return nil, fmt.Errorf("create the workspace: %w", err)
	}
	return &workspace{backend: b, dir: dir}, nil
}

// Run implements backend.Workspace. When ctx ends, the step's process group
// gets SIGTERM, then SIGKILL once the grace period has passed.
func (w *workspace) Run(ctx context.Context, commands []string, out io.Writer) (int, error) {
	p, err := w.backend.Group.Start(proc.Command{
		Name: "/bin/sh", Args: []string{"-c", script(commands)}, Dir: w.dir,
	}, out, out)
	if err != nil {
		return -1, fmt.Errorf("start the step: %w", err)
	}
	exited := make(chan error, 1)
	go func() { exited <- p.Wait() }()
	select {
	case err := <-exited:
		return status(err)
	case <-ctx.Done():
		// A step that exited before the cancellation was seen keeps its result.
		select {
		case err := <-exited:
			return status(err)
		default:
		}
		grace := w.backend.Grace
		if grace == 0 {
			grace = defaultGrace
		}
		stop, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
		defer cancel()
		// Stop kills the group at its deadline, so its error adds nothing.
		_ = p.Stop(stop)
		<-exited
		return -1, fmt.Errorf("the step was stopped: %w", ctx.Err())
	}
}

// Destroy implements backend.Workspace. When the removal fails, as on a
// directory a step made read-only, it gives the tree's directories owner
// write permission and tries once more.
func (w *workspace) Destroy() error {
	if err := os.RemoveAll(w.dir); err == nil {
		return nil
	}
	// The walk stays inside the workspace, whatever symlinks a step left. A
	// directory that cannot be read is skipped: the second removal reports
	// what is left.
	if root, err := os.OpenRoot(w.dir); err == nil {
		_ = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr == nil && d.IsDir() {
				if info, err := d.Info(); err == nil {
					_ = root.Chmod(path, info.Mode().Perm()|ownerWrite)
				}
			}
			return nil
		})
		_ = root.Close()
	}
	if err := os.RemoveAll(w.dir); err != nil {
		return fmt.Errorf("remove the workspace: %w", err)
	}
	return nil
}

// script is the shell script of a step's commands: each command followed by
// check.
func script(commands []string) string {
	var b strings.Builder
	for _, c := range commands {
		b.WriteString(c)
		b.WriteString("\n")
		b.WriteString(check)
	}
	return b.String()
}

// status is the exit status of a step whose wait returned err. A shell killed
// by a signal has none.
func status(err error) (int, error) {
	if err == nil {
		return 0, nil
	}
	if exit, ok := errors.AsType[*exec.ExitError](err); ok && exit.Exited() {
		return exit.ExitCode(), nil
	}
	return -1, fmt.Errorf("the step's shell: %w", err)
}
