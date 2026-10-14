package main

import (
	"bytes"
	"context"
	"errors"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/api"
	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres/postgrestest"
	"github.com/thatsnotmynameio/crew-actions/internal/transport/connect"
)

// waitFor is how long a test waits for a command to print or to end.
const waitFor = 10 * time.Second

// unreachableDatabase is a database URL nothing answers at, with a password
// that must never be printed.
//
//nolint:gosec // a made-up credential, which the test checks is never printed
const unreachableDatabase = "postgres://crew:s3cret-pass@127.0.0.1:1/crew?sslmode=disable&connect_timeout=5"

// servingLine is the engine's notice once it serves, with its URL.
var servingLine = regexp.MustCompile(`serving on (http://\S+) without authentication`)

// background is a command running in another goroutine.
type background struct {
	stdout, stderr *syncBuffer
	cancel         context.CancelFunc
	done           chan int

	once sync.Once
	code int
}

// start runs crew-actions with args in the background until the test ends
// or stop is called.
func start(t *testing.T, args ...string) *background {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	b := &background{stdout: &syncBuffer{}, stderr: &syncBuffer{}, cancel: cancel, done: make(chan int, 1)}
	go func() { b.done <- run(ctx, args, b.stdout, b.stderr) }()
	t.Cleanup(func() { b.stop(t) })
	return b
}

// stop cancels the command's context and returns its exit code.
func (b *background) stop(t *testing.T) int {
	t.Helper()
	b.cancel()
	return b.wait(t)
}

// wait returns the command's exit code once it ends.
func (b *background) wait(t *testing.T) int {
	t.Helper()
	b.once.Do(func() {
		select {
		case b.code = <-b.done:
		case <-time.After(3 * waitFor):
			t.Fatalf("the command did not end; stdout %q, stderr %q", b.stdout, b.stderr)
		}
	})
	return b.code
}

// waitOutput waits until the command's stdout matches re, and returns the
// match.
func (b *background) waitOutput(t *testing.T, re *regexp.Regexp) []string {
	t.Helper()
	deadline := time.After(waitFor)
	for {
		if m := re.FindStringSubmatch(b.stdout.String()); m != nil {
			return m
		}
		select {
		case code := <-b.done:
			b.done <- code
			t.Fatalf("the command exited %d before printing %q; stdout %q, stderr %q",
				code, re, b.stdout, b.stderr)
		case <-deadline:
			t.Fatalf("stdout %q does not match %q; stderr %q", b.stdout, re, b.stderr)
		case <-time.After(10 * time.Millisecond):
		}
	}
}

// engineProc is an engine command running in the background.
type engineProc struct {
	*background

	// url is where it serves, and addr its address.
	url, addr string
}

// startEngine starts an engine on addr, with the database the environment
// names and its logs in logDir, and waits until it serves.
func startEngine(t *testing.T, addr, logDir string) *engineProc {
	t.Helper()
	b := start(t, "engine", "--addr", addr, "--log-dir", logDir)
	url := b.waitOutput(t, servingLine)[1]
	return &engineProc{background: b, url: url, addr: strings.TrimPrefix(url, "http://")}
}

func TestEngineRefusesAnAddressThatIsNotLoopback(t *testing.T) {
	// The database would be opened, and fail with exit 1, past the check.
	t.Setenv(databaseURLEnv, unreachableDatabase)
	for _, addr := range []string{"0.0.0.0:0", ":0", "[::]:0"} {
		t.Run(addr, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			got := run(t.Context(), []string{"engine", "--addr", addr, "--log-dir", t.TempDir()}, &stdout, &stderr)
			if got != exitUsage {
				t.Fatalf("engine --addr %s = %d, want %d; stderr %q", addr, got, exitUsage, stderr.String())
			}
			if !strings.Contains(stderr.String(), addr) || !strings.Contains(stderr.String(), "loopback") {
				t.Errorf("stderr = %q, want it to name %s as not loopback", stderr.String(), addr)
			}
		})
	}
}

func TestEngineNeedsTheDatabaseURL(t *testing.T) {
	t.Setenv(databaseURLEnv, "")
	var stdout, stderr bytes.Buffer
	got := run(t.Context(), []string{"engine", "--addr", "127.0.0.1:0", "--log-dir", t.TempDir()}, &stdout, &stderr)
	if got != exitUsage {
		t.Fatalf("engine without %s = %d, want %d", databaseURLEnv, got, exitUsage)
	}
	if !strings.Contains(stderr.String(), databaseURLEnv) {
		t.Errorf("stderr = %q, want it to name %s", stderr.String(), databaseURLEnv)
	}
}

func TestEngineWithAnUnreachableDatabase(t *testing.T) {
	t.Setenv(databaseURLEnv, unreachableDatabase)
	var stdout, stderr bytes.Buffer
	got := run(t.Context(), []string{"engine", "--addr", "127.0.0.1:0", "--log-dir", t.TempDir()}, &stdout, &stderr)
	if got != exitFailure {
		t.Fatalf("engine with an unreachable database = %d, want %d; stderr %q", got, exitFailure, stderr.String())
	}
	if stderr.Len() == 0 {
		t.Error("stderr is empty, want the reason")
	}
	if strings.Contains(stdout.String()+stderr.String(), "s3cret-pass") {
		t.Errorf("the output holds the database's password: stdout %q, stderr %q", stdout.String(), stderr.String())
	}
}

func TestEngineUsage(t *testing.T) {
	for _, args := range [][]string{
		{"engine", "extra"},
		{"engine", "--unknown"},
		{"runner", "extra"},
		{"runner", "--engine", "127.0.0.1:7700"},
	} {
		var stderr bytes.Buffer
		if got := run(t.Context(), args, &bytes.Buffer{}, &stderr); got != exitUsage {
			t.Errorf("run(%q) = %d, want %d; stderr %q", args, got, exitUsage, stderr.String())
		}
	}
}

func TestDefaultLogDir(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/state")
	if got, err := defaultLogDir(); err != nil || got != filepath.Join("/state", "crew-actions", "logs") {
		t.Errorf("defaultLogDir() = %q, %v, want /state/crew-actions/logs", got, err)
	}
	t.Setenv("XDG_STATE_HOME", "")
	t.Setenv("HOME", "/home/someone")
	want := filepath.Join("/home/someone", ".local", "state", "crew-actions", "logs")
	if got, err := defaultLogDir(); err != nil || got != want {
		t.Errorf("defaultLogDir() = %q, %v, want %q", got, err, want)
	}
}

// nextResult is what a Next returned.
type nextResult struct {
	resp *protocol.NextResponse
	err  error
}

// openNext calls Next in the background.
func openNext(ctx context.Context, runners protocol.Engine) <-chan nextResult {
	next := make(chan nextResult, 1)
	go func() {
		resp, err := runners.Next(ctx, &protocol.NextRequest{})
		next <- nextResult{resp, err}
	}()
	return next
}

// openFollow follows the build id in the background, and returns once the
// stream sent its first event.
func openFollow(ctx context.Context, t *testing.T, builds api.Builds, id int64) <-chan error {
	t.Helper()
	followed, ended := make(chan struct{}, 1), make(chan error, 1)
	go func() {
		ended <- builds.Follow(ctx, &api.FollowRequest{BuildId: id}, func(*api.FollowResponse) error {
			select {
			case followed <- struct{}{}:
			default:
			}
			return nil
		})
	}()
	select {
	case <-followed:
	case <-time.After(waitFor):
		t.Fatal("Follow() sent nothing")
	}
	return ended
}

func TestEngineStopsWaitingWorkFirst(t *testing.T) {
	t.Setenv(databaseURLEnv, postgrestest.New(t))
	e := startEngine(t, "127.0.0.1:0", t.TempDir())
	if !strings.Contains(e.stdout.String(), "any local user") {
		t.Errorf("stdout = %q, want it to say any local user can reach the engine", e.stdout)
	}
	runners, builds := connect.NewEngineClient(e.url), connect.NewBuildsClient(e.url)
	ctx := t.Context()
	submitted, err := builds.Submit(ctx, &api.SubmitRequest{
		Host: "github.com", Organization: "org", Repository: "repo",
		FileName: "test.yaml", Content: []byte("steps:\n  - name: one\n    commands: [\"true\"]\n"),
	})
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	// The first next takes the build's workflow, so the second has none.
	if got, err := runners.Next(ctx, &protocol.NextRequest{}); err != nil || got.GetWorkflow() == nil {
		t.Fatalf("Next() = %v, %v, want the workflow", got, err)
	}
	next := openNext(ctx, runners)
	followEnded := openFollow(ctx, t, builds, submitted.GetBuildId())
	// Let the second next reach the engine.
	time.Sleep(300 * time.Millisecond)

	if got := e.stop(t); got != exitOK {
		t.Fatalf("the stopped engine exited %d, want %d; stderr %q", got, exitOK, e.stderr)
	}
	select {
	case got := <-next:
		if got.err != nil || got.resp.GetWorkflow() != nil {
			t.Errorf("the open Next() = %v, %v, want no workflow and no error", got.resp, got.err)
		}
	case <-time.After(waitFor):
		t.Error("the open Next() did not end")
	}
	select {
	case err := <-followEnded:
		if !errors.Is(err, model.ErrUnavailable) {
			t.Errorf("the open Follow() = %v, want %v", err, model.ErrUnavailable)
		}
	case <-time.After(waitFor):
		t.Error("the open Follow() did not end")
	}
}
