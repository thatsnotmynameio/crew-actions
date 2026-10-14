package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
)

func TestAStepsOutputArrivesInOrderBeforeItFinishes(t *testing.T) {
	b := &fakeBackend{steps: map[string]fakeStep{
		"print": func(_ context.Context, out io.Writer) (int, error) {
			_, _ = io.WriteString(out, "first\n")
			// Longer than a flush interval, so the output goes in two logs.
			time.Sleep(400 * time.Millisecond)
			_, _ = io.WriteString(out, "second\n")
			return 0, nil
		},
	}}
	e := newFakeEngine(t, work(1, "print", "quiet"))
	run(t, e, b)
	e.want(t,
		"next", "accept 1",
		"update 1 running", "update 1 finished success 0",
		"update 2 running", "update 2 finished success 0",
		"done success",
		"next",
	)
	logs := e.stepLogs(1)
	if len(logs) < 2 {
		t.Errorf("the step's output came in %d logs, want one per flush", len(logs))
	}
	wantContiguous(t, logs)
	for _, l := range logs {
		// Calls before step 1's finished: next, accept, its running.
		if l.before > 3 {
			t.Errorf("a log came after %d other calls, want it before the step's finished", l.before)
		}
	}
	if got := e.output(1); got != "first\nsecond\n" {
		t.Errorf("the step's log = %q, want what it printed", got)
	}
	if got := e.output(2); got != "" {
		t.Errorf("a quiet step's log = %q, want nothing", got)
	}
}

func TestALargeOutputArrivesWholeWhileLogIsSlow(t *testing.T) {
	const size = 200 << 10
	want := bytes.Repeat([]byte("0123456789abcdef"), size/16)
	e := newFakeEngine(t, work(1, "loud"))
	e.logGate = make(chan struct{})
	b := &fakeBackend{steps: map[string]fakeStep{
		"loud": func(_ context.Context, out io.Writer) (int, error) {
			// Every log waits for the gate, which opens only once the whole
			// output is written: a writer that blocked would hang here.
			for p := want; len(p) > 0; p = p[min(len(p), 4096):] {
				_, _ = out.Write(p[:min(len(p), 4096)])
			}
			close(e.logGate)
			return 0, nil
		},
	}}
	run(t, e, b)
	e.want(t,
		"next", "accept 1",
		"update 1 running", "update 1 finished success 0",
		"done success",
		"next",
	)
	logs := e.stepLogs(1)
	wantContiguous(t, logs)
	for _, l := range logs {
		if n := len(l.req.GetData()); n > 64<<10 {
			t.Errorf("a log carried %d bytes, want at most 64 KiB", n)
		}
	}
	if got := e.output(1); got != string(want) {
		t.Errorf("the step's log has %d bytes, want the %d it printed", len(got), len(want))
	}
}

func TestAStepThatCannotStartLogsWhy(t *testing.T) {
	b := &fakeBackend{steps: map[string]fakeStep{
		"broken": func(context.Context, io.Writer) (int, error) {
			return -1, errors.New("start the step: no shell")
		},
	}}
	e := newFakeEngine(t, work(1, "broken", "after"))
	stderr := run(t, e, b)
	e.want(t,
		"next", "accept 1",
		"update 1 running", "update 1 finished failure none",
		"update 2 skipped",
		"done failure",
		"next",
	)
	got := e.output(1)
	if got != "crew-actions: start the step: no shell\n" {
		t.Errorf("the step's log = %q, want one crew-actions: line with the reason", got)
	}
	if !strings.Contains(stderr, "start the step: no shell") {
		t.Errorf("stderr = %q, want the reason", stderr)
	}
}

func TestAKilledStepsReasonStartsALineOfItsOwn(t *testing.T) {
	b := &fakeBackend{steps: map[string]fakeStep{
		"killed": func(_ context.Context, out io.Writer) (int, error) {
			_, _ = io.WriteString(out, "half a line")
			return -1, errors.New("the step's shell: signal: killed")
		},
	}}
	e := newFakeEngine(t, work(1, "killed"))
	run(t, e, b)
	want := "half a line\ncrew-actions: the step's shell: signal: killed\n"
	if got := e.output(1); got != want {
		t.Errorf("the step's log = %q, want %q", got, want)
	}
	wantContiguous(t, e.stepLogs(1))
}

func TestALogAnswerOfTheWrongSizeCancelsTheAttempt(t *testing.T) {
	stopped := false
	b := &fakeBackend{steps: map[string]fakeStep{
		"long": func(ctx context.Context, out io.Writer) (int, error) {
			_, _ = io.WriteString(out, "hello\n")
			select {
			case <-ctx.Done():
				stopped = true
				return -1, fmt.Errorf("the step was stopped: %w", ctx.Err())
			case <-time.After(10 * time.Second):
				return 0, nil
			}
		},
	}}
	e := newFakeEngine(t, work(1, "long", "after"))
	e.logSize = func(req *protocol.LogRequest) int64 { return req.GetOffset() }
	stderr := run(t, e, b)
	e.want(t, "next", "accept 1", "update 1 running", "next")
	if !stopped {
		t.Error("the step was not stopped")
	}
	if !strings.Contains(stderr, "holds 0 bytes") {
		t.Errorf("stderr = %q, want the size the engine answered", stderr)
	}
}

// wantContiguous fails the test unless logs start at offset 0 and each
// starts where the one before it ended.
func wantContiguous(t *testing.T, logs []logCall) {
	t.Helper()
	var offset int64
	for i, l := range logs {
		if l.req.GetOffset() != offset {
			t.Errorf("log %d starts at offset %d, want %d", i, l.req.GetOffset(), offset)
		}
		offset = l.req.GetOffset() + int64(len(l.req.GetData()))
	}
}
