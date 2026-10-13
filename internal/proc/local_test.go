// These tests are crew-actions' own, not crew's: proc.go and proc_test.go
// stay verbatim copies. In crew, its callers' tests reach the paths below;
// here nothing calls proc yet, so these tests hold the coverage floor.

package proc_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/proc"
)

func TestStopOfAProcessThatAlreadyEndedDoesNothing(t *testing.T) {
	var g proc.Group
	p, err := g.Start(sh("exit 0"), nil, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := waitErr(t, p); err != nil {
		t.Fatalf("Wait: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := p.Stop(ctx); err != nil {
		t.Errorf("Stop = %v, want nil", err)
	}
}

func TestStartWithOneWriterForStdoutAndStderrKeepsTheirOrder(t *testing.T) {
	var g proc.Group
	var out output
	p, err := g.Start(sh(`echo one; echo two >&2; echo three`), &out, &out)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := waitErr(t, p); err != nil {
		t.Fatalf("Wait: %v", err)
	}
	if want := "one\ntwo\nthree\n"; out.String() != want {
		t.Errorf("output = %q, want %q", out.String(), want)
	}
}

func TestRunWithAContextAlreadyDoneStartsNothing(t *testing.T) {
	var g proc.Group
	path := filepath.Join(t.TempDir(), "ran")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	c := sh(`touch "$1"`)
	c.Args = append(c.Args, "sh", path)
	if _, err := g.Run(ctx, c); !errors.Is(err, context.Canceled) {
		t.Errorf("Run = %v, want the context's cancellation", err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the command ran: stat %s = %v", path, err)
	}
}

func TestRunOfAMissingCommandFails(t *testing.T) {
	var g proc.Group
	_, err := g.Run(context.Background(), proc.Command{Name: "crew-no-such-command"})
	if !errors.Is(err, exec.ErrNotFound) {
		t.Errorf("Run = %v, want the command not found", err)
	}
}

func TestRunOfAFailingCommandWithoutStderrReportsItsStatus(t *testing.T) {
	var g proc.Group
	_, err := g.Run(context.Background(), sh("exit 5"))
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 5 {
		t.Fatalf("Run = %v, want exit status 5", err)
	}
	if want := "sh: exit status 5"; err.Error() != want {
		t.Errorf("error = %q, want %q", err, want)
	}
}
