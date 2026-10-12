package main

import (
	"bytes"
	"errors"
	"testing"
)

// failingWriter fails every write.
type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) { return 0, errors.New("closed") }

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
		{name: "unknown argument", args: []string{"run"}, want: exitUsage},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			version = tc.version
			t.Cleanup(func() { version = "" })
			var stdout, stderr bytes.Buffer
			if got := run(tc.args, &stdout, &stderr); got != tc.want {
				t.Fatalf("run(%q) = %d, want %d", tc.args, got, tc.want)
			}
			if stdout.String() != tc.out {
				t.Errorf("stdout = %q, want %q", stdout.String(), tc.out)
			}
			if tc.want == exitUsage && stderr.Len() == 0 {
				t.Error("stderr is empty, want the usage line")
			}
		})
	}
}

func TestRunFailedOutput(t *testing.T) {
	if got := run([]string{"--version"}, failingWriter{}, &bytes.Buffer{}); got != exitFailure {
		t.Fatalf("run with a failing stdout = %d, want %d", got, exitFailure)
	}
}
