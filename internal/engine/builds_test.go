package engine

import (
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/thatsnotmynameio/crew-actions/internal/api"
	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
)

// testFile is a workflow file with two steps.
const testFile = `steps:
  - name: checkout
    commands:
      - git status
  - name: build
    commands:
      - go build ./...
      - go vet ./...
`

// submitRequest returns a request to submit content as test.yaml, for repo.
func submitRequest(content string) *api.SubmitRequest {
	return &api.SubmitRequest{
		Host: repo.Host, Organization: repo.Organization, Repository: repo.Name,
		FileName: "test.yaml", Content: []byte(content),
	}
}

// checkNoBuild fails the test if any workflow is waiting to be handed out.
func (f fixture) checkNoBuild(t *testing.T) {
	t.Helper()
	pending, err := f.store.PendingWorkflows(t.Context(), 1)
	if err != nil {
		t.Fatalf("PendingWorkflows() error = %v", err)
	}
	if len(pending) != 0 {
		t.Errorf("pending workflows = %v, want none", pending)
	}
}

func TestSubmitCreatesABuild(t *testing.T) {
	f := newFixture(t)
	resp, err := f.engine.Clients().Submit(t.Context(), submitRequest(testFile))
	if err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	b := f.build(t, model.BuildID(resp.GetBuildId()))
	if b.Repository != repo || b.State != model.BuildRunning {
		t.Errorf("build = %s, %s; want %s, running", b.Repository, b.State, repo)
	}
	if len(b.Workflows) != 1 {
		t.Fatalf("workflows = %d, want 1", len(b.Workflows))
	}
	w := b.Workflows[0]
	if w.Name != "test" || w.State != model.WorkflowPending {
		t.Errorf("workflow = %q, %s; want \"test\", pending", w.Name, w.State)
	}
	got, err := decode(w.Definition)
	if err != nil {
		t.Fatal(err)
	}
	want := &protocol.Workflow{
		Name: "test",
		Steps: []*protocol.Step{
			{Name: "checkout", Commands: []string{"git status"}},
			{Name: "build", Commands: []string{"go build ./...", "go vet ./..."}},
		},
	}
	if !proto.Equal(got, want) {
		t.Errorf("definition = %v, want %v", got, want)
	}
}

// Covers AE6.
func TestSubmitRefusesAFileItDoesNotRun(t *testing.T) {
	tests := []struct {
		name    string
		content string
		// want are the words the error's message must hold.
		want []string
	}{
		{
			name:    "an unsupported key",
			content: "depends_on: [build]\n" + testFile,
			want:    []string{"depends_on", "not supported yet"},
		},
		{
			name:    "a misspelled key",
			content: "steps:\n  - name: build\n    comands:\n      - make\n",
			want:    []string{"comands", "unknown key"},
		},
		{
			name:    "invalid YAML",
			content: "steps: [\n",
			want:    []string{"not valid YAML"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			_, err := f.engine.Clients().Submit(t.Context(), submitRequest(tt.content))
			if !errors.Is(err, model.ErrInvalid) {
				t.Fatalf("Submit() error = %v, want %v", err, model.ErrInvalid)
			}
			for _, word := range tt.want {
				if !strings.Contains(err.Error(), word) {
					t.Errorf("Submit() error = %q, want it to name %q", err, word)
				}
			}
			f.checkNoBuild(t)
		})
	}
}

func TestSubmitRefusesAnIncompleteRequest(t *testing.T) {
	tests := []struct {
		name   string
		change func(*api.SubmitRequest)
	}{
		{"no host", func(r *api.SubmitRequest) { r.Host = "" }},
		{"no organization", func(r *api.SubmitRequest) { r.Organization = "" }},
		{"no repository", func(r *api.SubmitRequest) { r.Repository = "" }},
		{"a file name without .yaml or .yml", func(r *api.SubmitRequest) { r.FileName = "test.json" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			req := submitRequest(testFile)
			tt.change(req)
			if _, err := f.engine.Clients().Submit(t.Context(), req); !errors.Is(err, model.ErrInvalid) {
				t.Errorf("Submit() error = %v, want %v", err, model.ErrInvalid)
			}
			f.checkNoBuild(t)
		})
	}
}

func TestSubmitWakesAWaitingNext(t *testing.T) {
	f := newFixture(t)
	// With a long tick, only the submit's notification can wake Next soon.
	f.engine.window, f.engine.tick = time.Minute, time.Minute
	done := make(chan *protocol.NextResponse, 1)
	go func() {
		resp, err := f.engine.Runners().Next(t.Context(), &protocol.NextRequest{})
		if err != nil {
			t.Errorf("Next() error = %v", err)
		}
		done <- resp
	}()
	time.Sleep(100 * time.Millisecond)
	if _, err := f.engine.Clients().Submit(t.Context(), submitRequest(testFile)); err != nil {
		t.Fatalf("Submit() error = %v", err)
	}
	select {
	case resp := <-done:
		if resp.GetWorkflow().GetName() != "test" {
			t.Errorf("Next() = %v, want the submitted workflow", resp)
		}
	case <-time.After(waitFor):
		t.Fatal("Next() did not return the submitted build")
	}
}
