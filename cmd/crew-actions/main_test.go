package main

import (
	"bytes"
	"errors"
	"strings"
	"sync"
	"testing"
)

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

// syncBuffer is a bytes.Buffer that a command running in another goroutine
// can write while the test reads it.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p) //nolint:wrapcheck // a writer returns its buffer's error as it is
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestRun(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		version string
		want    int
		out     string
	}{
		{name: "dev build", args: []string{"--version"}, want: exitOK, out: "crew-actions dev\n"},
		{name: "release build", args: []string{"--version"}, version: "v0.1.0", want: exitOK, out: "crew-actions v0.1.0\n"},
		{name: "no argument", want: exitUsage},
		{name: "unknown subcommand", args: []string{"build"}, want: exitUsage},
		{name: "version with an argument", args: []string{"--version", "now"}, want: exitUsage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			version = tc.version
			t.Cleanup(func() { version = "" })
			var stdout, stderr bytes.Buffer
			if got := run(t.Context(), tc.args, &stdout, &stderr); got != tc.want {
				t.Fatalf("run(%q) = %d, want %d", tc.args, got, tc.want)
			}
			if stdout.String() != tc.out {
				t.Errorf("stdout = %q, want %q", stdout.String(), tc.out)
			}
			if tc.want == exitUsage && !strings.Contains(stderr.String(), "usage:") {
				t.Errorf("stderr = %q, want the usage", stderr.String())
			}
		})
	}
}

func TestRunUsageListsTheSubcommandsAndExitCodes(t *testing.T) {
	var stderr bytes.Buffer
	run(t.Context(), nil, &bytes.Buffer{}, &stderr)
	for _, want := range []string{"crew-actions engine", "crew-actions runner", "crew-actions run ", "130"} {
		if !strings.Contains(stderr.String(), want) {
			t.Errorf("usage = %q, want it to mention %q", stderr.String(), want)
		}
	}
}

func TestRunFailedOutput(t *testing.T) {
	if got := run(t.Context(), []string{"--version"}, failingWriter{}, &bytes.Buffer{}); got != exitFailure {
		t.Fatalf("run with a failing stdout = %d, want %d", got, exitFailure)
	}
}
