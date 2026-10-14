package runner

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
	"github.com/thatsnotmynameio/crew-actions/internal/runner/executor"
)

const (
	// maxChunk is the most output one log carries.
	maxChunk = 64 << 10
	// flushEvery is how often a step's output is shipped while no chunk of
	// it is full.
	flushEvery = 250 * time.Millisecond
	// reasonPrefix starts the line a runner-side reason adds to a step's
	// log.
	reasonPrefix = "crew-actions: "
)

// observer reports an attempt's steps to the engine. It is the one place
// where the executor's 0-based step index becomes the protocol's 1-based
// position, and an exit status of -1 becomes none. Its first failed call
// cancels the attempt, after which it sends nothing.
type observer struct {
	ctx     context.Context //nolint:containedctx // the attempt's context, which a failed call cancels
	cancel  context.CancelFunc
	engine  protocol.Engine
	attempt int64
	reportf func(format string, args ...any)

	// log is the running step's output.
	log *stepLog

	mu  sync.Mutex
	err error
}

// Compile-time guard.
var _ executor.Observer = (*observer)(nil)

// Started implements executor.Observer: it sends the step's running state
// and returns a writer that never blocks.
func (o *observer) Started(i int) io.Writer {
	o.update(&protocol.UpdateRequest{Position: position(i), State: protocol.StepState_STEP_STATE_RUNNING})
	o.log = newStepLog(o, position(i))
	return o.log
}

// Finished implements executor.Observer: it adds a runner-side reason to the
// step's log, waits until the whole log is shipped, then sends the step's
// finished state.
func (o *observer) Finished(i int, r executor.Result) {
	if r.Err != nil && !r.Stopped {
		o.reportf("attempt %d, step %d: %v", o.attempt, position(i), r.Err)
		o.log.reason(r.Err)
	}
	o.log.close()
	req := &protocol.UpdateRequest{
		Position: position(i),
		State:    protocol.StepState_STEP_STATE_FINISHED,
		Outcome:  protocol.OutcomeFromModel(modelOutcome(r.Outcome())),
	}
	if r.ExitStatus >= 0 {
		status := int32(r.ExitStatus) //nolint:gosec // an exit status is at most 255
		req.ExitStatus = &status
	}
	o.update(req)
}

// Skipped implements executor.Observer.
func (o *observer) Skipped(i int) {
	o.update(&protocol.UpdateRequest{Position: position(i), State: protocol.StepState_STEP_STATE_SKIPPED})
}

// update sends req for the attempt, unless the attempt was cancelled.
func (o *observer) update(req *protocol.UpdateRequest) {
	if o.ctx.Err() != nil {
		return
	}
	req.AttemptId = o.attempt
	if _, err := o.engine.Update(o.ctx, req); err != nil {
		o.fail(fmt.Errorf("update step %d: %w", req.GetPosition(), err))
	}
}

// fail keeps err, when it is the first, and cancels the attempt.
func (o *observer) fail(err error) {
	o.mu.Lock()
	if o.err == nil {
		o.err = err
	}
	o.mu.Unlock()
	o.cancel()
}

// failed is the observer's first failed call, or nil.
func (o *observer) failed() error {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.err
}

// position is the protocol's position of the step at index i.
func position(i int) int32 {
	return int32(i + 1) //nolint:gosec // a workflow has far fewer steps than an int32 counts
}

// stepLog is a step's output on its way to the engine. Its writes append to
// a buffer and never block; a sender ships the buffer in chunks of at most
// maxChunk every flushEvery, or sooner when a chunk is full.
type stepLog struct {
	obs      *observer
	position int32
	// wake tells the sender to ship now: a chunk is full or the log closed.
	wake chan struct{}
	// sent is closed once the sender has stopped.
	sent chan struct{}

	mu  sync.Mutex
	buf []byte
	// open is whether the output so far ends inside a line.
	open   bool
	closed bool
}

// newStepLog returns the log of the step at position and starts its sender.
func newStepLog(obs *observer, position int32) *stepLog {
	l := &stepLog{obs: obs, position: position, wake: make(chan struct{}, 1), sent: make(chan struct{})}
	go l.send()
	return l
}

// Write appends p to the log. It never fails.
func (l *stepLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	l.buf = append(l.buf, p...)
	if len(p) > 0 {
		l.open = p[len(p)-1] != '\n'
	}
	full := len(l.buf) >= maxChunk
	l.mu.Unlock()
	if full {
		l.signal()
	}
	return len(p), nil
}

// reason adds err to the log as a line of its own starting with
// reasonPrefix.
func (l *stepLog) reason(err error) {
	l.mu.Lock()
	open := l.open
	l.mu.Unlock()
	line := reasonPrefix + err.Error() + "\n"
	if open {
		line = "\n" + line
	}
	_, _ = l.Write([]byte(line))
}

// close waits until the sender has shipped the whole log, or stopped on a
// failed call or the attempt's end.
func (l *stepLog) close() {
	l.mu.Lock()
	l.closed = true
	l.mu.Unlock()
	l.signal()
	<-l.sent
}

// signal wakes the sender, unless a wake is already pending.
func (l *stepLog) signal() {
	select {
	case l.wake <- struct{}{}:
	default:
	}
}

// send ships the log until it is closed and empty. When the attempt ends,
// it stops at once: nothing more can be sent for it.
func (l *stepLog) send() {
	defer close(l.sent)
	tick := time.NewTicker(flushEvery)
	defer tick.Stop()
	var offset int64
	for {
		select {
		case <-l.wake:
		case <-tick.C:
		case <-l.obs.ctx.Done():
			return
		}
		// Read before shipping: what was written before the close is in the
		// buffer by now.
		l.mu.Lock()
		closed := l.closed
		l.mu.Unlock()
		if !l.ship(&offset) || closed {
			return
		}
	}
}

// ship sends the buffer in chunks, from *offset on, and reports whether
// every log succeeded. A chunk leaves the buffer once the engine holds it.
func (l *stepLog) ship(offset *int64) bool {
	for {
		l.mu.Lock()
		n := min(len(l.buf), maxChunk)
		// Writes append past n, so the chunk stays as it is.
		chunk := l.buf[:n:n]
		l.mu.Unlock()
		if n == 0 {
			return true
		}
		resp, err := l.obs.engine.Log(l.obs.ctx, &protocol.LogRequest{
			AttemptId: l.obs.attempt, Position: l.position, Offset: *offset, Data: chunk,
		})
		if err != nil {
			l.obs.fail(fmt.Errorf("log step %d: %w", l.position, err))
			return false
		}
		if want := *offset + int64(n); resp.GetSize() != want {
			l.obs.fail(fmt.Errorf("log step %d: the engine holds %d bytes, want %d", l.position, resp.GetSize(), want))
			return false
		}
		*offset += int64(n)
		l.mu.Lock()
		l.buf = l.buf[n:]
		l.mu.Unlock()
	}
}
