package main

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/store/logs"
	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres"
	"github.com/thatsnotmynameio/crew-actions/internal/store/postgres/postgrestest"
)

// threeSteps is a workflow file whose second step fails after printing.
const threeSteps = `steps:
  - name: one
    commands: ["echo first"]
  - name: two
    commands: ["echo out; exit 1"]
  - name: three
    commands: ["echo never"]
`

// buildLine is run's line with the build's id.
var buildLine = regexp.MustCompile(`build (\d+) of `)

// pendingLine is run's line once the build's workflow waits for a runner.
var pendingLine = regexp.MustCompile(`workflow test: pending, waiting for a runner`)

// world is an engine on a database of its own, with its logs in a
// temporary directory, as a person starts it by hand.
type world struct {
	engine *engineProc
	store  *postgres.Store
	logDir string
}

// newWorld starts an engine on a new database. It skips the test without
// CREW_ACTIONS_TEST_DATABASE_URL.
func newWorld(t *testing.T) *world {
	t.Helper()
	databaseURL := postgrestest.New(t)
	t.Setenv(databaseURLEnv, databaseURL)
	store, err := postgres.Open(t.Context(), databaseURL)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(store.Close)
	logDir := t.TempDir()
	return &world{engine: startEngine(t, "127.0.0.1:0", logDir), store: store, logDir: logDir}
}

// restartEngine stops the engine and starts another on the same address,
// database and logs.
func (w *world) restartEngine(t *testing.T) {
	t.Helper()
	if got := w.engine.stop(t); got != exitOK {
		t.Fatalf("the stopped engine exited %d, want %d; stderr %q", got, exitOK, w.engine.stderr)
	}
	w.engine = startEngine(t, w.engine.addr, w.logDir)
}

// startRunner starts a runner of the engine.
func (w *world) startRunner(t *testing.T) *background {
	t.Helper()
	return start(t, "runner", "--engine", w.engine.url)
}

// workflowFile writes content as test.yaml in a new directory and returns
// its path.
func workflowFile(t *testing.T, content string) string {
	t.Helper()
	file := filepath.Join(t.TempDir(), "test.yaml")
	if err := os.WriteFile(file, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	return file
}

// run runs `crew-actions run` against the engine with args and returns its
// exit code and output.
func (w *world) run(t *testing.T, args ...string) (int, string, string) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := run(t.Context(), append([]string{"run", "--engine", w.engine.url}, args...), &stdout, &stderr)
	return code, stdout.String(), stderr.String()
}

// build returns the snapshot of the build whose id run printed in stdout.
func (w *world) build(t *testing.T, stdout string) model.Build {
	t.Helper()
	m := buildLine.FindStringSubmatch(stdout)
	if m == nil {
		t.Fatalf("stdout %q has no build id", stdout)
	}
	id, err := strconv.ParseInt(m[1], 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	b, err := w.store.GetBuild(t.Context(), model.BuildID(id))
	if err != nil {
		t.Fatalf("GetBuild(%d) error = %v", id, err)
	}
	return b
}

// logOf returns what the log of the step at position of the attempt holds.
func (w *world) logOf(t *testing.T, attempt model.AttemptID, position int) string {
	t.Helper()
	id, err := w.store.DatabaseID(t.Context())
	if err != nil {
		t.Fatalf("DatabaseID() error = %v", err)
	}
	data, err := logs.New(w.logDir, id).Read(attempt, position, 0, 1<<20)
	if err != nil {
		t.Fatalf("read the log of step %d: %v", position, err)
	}
	return string(data)
}

// noBuild fails the test if the database holds a build.
func (w *world) noBuild(t *testing.T) {
	t.Helper()
	// A new database numbers its first build 1.
	if b, err := w.store.GetBuild(t.Context(), 1); !errors.Is(err, model.ErrNotFound) {
		t.Errorf("GetBuild(1) = %+v, %v, want no build", b, err)
	}
}

// outcomeOf returns the outcome o points at, or "" for none.
func outcomeOf(o *model.Outcome) model.Outcome {
	if o == nil {
		return ""
	}
	return *o
}

func TestRunAFailingBuild(t *testing.T) {
	w := newWorld(t)
	w.startRunner(t)
	code, stdout, stderr := w.run(t, "--repo", "gitlab.com/group/repo", workflowFile(t, threeSteps))
	if code != exitFailure {
		t.Fatalf("run = %d, want %d; stdout %q, stderr %q", code, exitFailure, stdout, stderr)
	}
	out := strings.Index(stdout, "two | out\n")
	failed := strings.Index(stdout, "two: finished with failure, exit status 1\n")
	if out < 0 || failed < 0 || out > failed {
		t.Errorf("stdout = %q, want two's output, then its failure", stdout)
	}
	for _, want := range []string{"one | first\n", "three: skipped\n", "finished with failure\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}

	b := w.build(t, stdout)
	if b.State != model.BuildFinished || outcomeOf(b.Outcome) != model.OutcomeFailure {
		t.Errorf("the build is %s with %q, want finished with failure", b.State, outcomeOf(b.Outcome))
	}
	a := b.Workflows[0].Attempts[0]
	want := []struct {
		state   model.StepState
		outcome model.Outcome
		log     string
	}{
		{model.StepFinished, model.OutcomeSuccess, "first\n"},
		{model.StepFinished, model.OutcomeFailure, "out\n"},
		{model.StepSkipped, "", ""},
	}
	for i, s := range a.Steps {
		if s.State != want[i].state || outcomeOf(s.Outcome) != want[i].outcome {
			t.Errorf("step %s is %s with %q, want %s with %q",
				s.Name, s.State, outcomeOf(s.Outcome), want[i].state, want[i].outcome)
		}
		if got := w.logOf(t, a.ID, s.Position); got != want[i].log {
			t.Errorf("the log of step %s holds %q, want %q", s.Name, got, want[i].log)
		}
	}
}

func TestRunASucceedingBuild(t *testing.T) {
	w := newWorld(t)
	w.startRunner(t)
	code, stdout, stderr := w.run(t, "--repo", "gitlab.com/group/repo", workflowFile(t, twoSteps))
	if code != exitOK {
		t.Fatalf("run = %d, want %d; stdout %q, stderr %q", code, exitOK, stdout, stderr)
	}
	for _, want := range []string{"build | built\n", "test | tested\n", "finished with success\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}
}

func TestRunRefusesAKeyItDoesNotRun(t *testing.T) {
	w := newWorld(t)
	for content, want := range map[string]string{
		"depends_on: [build]\n" + twoSteps:                  "depends_on (line 1): not supported yet",
		strings.Replace(twoSteps, "commands", "comands", 1): "steps[0].comands (line 3): unknown key",
	} {
		code, stdout, stderr := w.run(t, "--repo", "gitlab.com/group/repo", workflowFile(t, content))
		if code != exitUsage {
			t.Fatalf("run = %d, want %d; stdout %q, stderr %q", code, exitUsage, stdout, stderr)
		}
		if !strings.Contains(stderr, want) {
			t.Errorf("stderr = %q, want %q", stderr, want)
		}
	}
	w.noBuild(t)
}

// noToken fails the test if what holds the credential of tokenOrigin.
func noToken(t *testing.T, where, what string) {
	t.Helper()
	if strings.Contains(what, "token") {
		t.Errorf("%s holds the credential: %q", where, what)
	}
}

// checkNoToken checks that the build and its logs hold the identity of
// tokenOrigin, and never its credential.
func (w *world) checkNoToken(t *testing.T, b model.Build) {
	t.Helper()
	want := model.Repository{Host: "gitlab.com", Organization: "group/sub", Name: "repo"}
	if b.Repository != want {
		t.Errorf("the build's repository is %+v, want %+v", b.Repository, want)
	}
	for i := range b.Workflows {
		noToken(t, "the workflow's definition", string(b.Workflows[i].Definition))
		b.Workflows[i].Definition = nil
	}
	noToken(t, "the build's rows", fmt.Sprintf("%+v", b))
	root, err := os.OpenRoot(w.logDir)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	err = fs.WalkDir(root.FS(), ".", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		noToken(t, "the log directory", path)
		data, err := fs.ReadFile(root.FS(), path)
		noToken(t, path, string(data))
		return err //nolint:wrapcheck // the test fails with it as it is
	})
	if err != nil {
		t.Fatal(err)
	}
	noToken(t, "the engine's output", w.engine.stdout.String()+w.engine.stderr.String())
}

func TestRunKeepsNoCredential(t *testing.T) {
	w := newWorld(t)
	runner := w.startRunner(t)
	code, stdout, stderr := w.run(t, "--repo", tokenOrigin, workflowFile(t, twoSteps))
	if code != exitOK {
		t.Fatalf("run --repo = %d, want %d; stdout %q, stderr %q", code, exitOK, stdout, stderr)
	}
	noToken(t, "run's output", stdout+stderr)
	w.checkNoToken(t, w.build(t, stdout))

	dir := gitDir(t, true, tokenOrigin)
	file := filepath.Join(dir, "test.yaml")
	if err := os.WriteFile(file, []byte(twoSteps), 0o600); err != nil {
		t.Fatal(err)
	}
	code, stdout, stderr = w.run(t, "test.yaml")
	if code != exitOK {
		t.Fatalf("run in a clone = %d, want %d; stdout %q, stderr %q", code, exitOK, stdout, stderr)
	}
	noToken(t, "run's output", stdout+stderr)
	w.checkNoToken(t, w.build(t, stdout))
	noToken(t, "the runner's output", runner.stderr.String())
}

func TestRunFollowsAcrossAnEngineRestart(t *testing.T) {
	w := newWorld(t)
	r := start(t, "run", "--engine", w.engine.url, "--repo", "gitlab.com/group/repo", workflowFile(t, twoSteps))
	r.waitOutput(t, pendingLine)
	w.restartEngine(t)
	w.startRunner(t)
	if code := r.wait(t); code != exitOK {
		t.Fatalf("run = %d, want %d; stdout %q, stderr %q", code, exitOK, r.stdout, r.stderr)
	}
	if !strings.Contains(r.stderr.String(), "unavailable") {
		t.Errorf("stderr = %q, want it to say the engine was unavailable", r.stderr)
	}
	stdout := r.stdout.String()
	seen := map[string]bool{}
	for line := range strings.Lines(stdout) {
		if seen[line] {
			t.Errorf("stdout = %q, which repeats %q", stdout, line)
		}
		seen[line] = true
	}
	for _, want := range []string{"build | built\n", "test | tested\n", "finished with success\n"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout = %q, want %q", stdout, want)
		}
	}
}

func TestRunInterruptedLeavesTheBuildRunning(t *testing.T) {
	w := newWorld(t)
	r := start(t, "run", "--engine", w.engine.url, "--repo", "gitlab.com/group/repo", workflowFile(t, twoSteps))
	r.waitOutput(t, pendingLine)
	if code := r.stop(t); code != exitInterrupted {
		t.Fatalf("an interrupted run = %d, want %d; stderr %q", code, exitInterrupted, r.stderr)
	}
	if !strings.Contains(r.stderr.String(), "continues") {
		t.Errorf("stderr = %q, want it to say the build continues", r.stderr)
	}
	w.startRunner(t)
	deadline := time.After(waitFor)
	for {
		b := w.build(t, r.stdout.String())
		if b.State == model.BuildFinished {
			if outcomeOf(b.Outcome) != model.OutcomeSuccess {
				t.Errorf("the build finished with %q, want success", outcomeOf(b.Outcome))
			}
			return
		}
		select {
		case <-deadline:
			t.Fatalf("the build is %s, want it finished", b.State)
		case <-time.After(50 * time.Millisecond):
		}
	}
}
