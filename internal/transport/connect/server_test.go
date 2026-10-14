package connect

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/thatsnotmynameio/crew-actions/internal/api"
	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
)

// fake is the engine behind the handler, as both services: it records each
// request it gets and answers with the responses below.
type fake struct {
	mu   sync.Mutex
	reqs []proto.Message
	// hook runs first in every call, when set, and its error is the call's.
	hook func(ctx context.Context) error
	// follow is Follow's body after the hook, when set; otherwise Follow
	// sends events.
	follow func(ctx context.Context, send func(*api.FollowResponse) error) error
}

var (
	nextRes = &protocol.NextResponse{
		WorkflowId: 3,
		Version:    4,
		Workflow: &protocol.Workflow{
			Name:  "build",
			Steps: []*protocol.Step{{Name: "test", Commands: []string{"go test ./...", "echo done"}}},
		},
	}
	acceptRes = &protocol.AcceptResponse{AttemptId: 9}
	logRes    = &protocol.LogResponse{Size: 1234}
	submitRes = &api.SubmitResponse{BuildId: 42}
	events    = []*api.FollowResponse{
		{Event: &api.FollowResponse_Workflow{Workflow: &api.WorkflowEvent{
			Name: "build", State: api.WorkflowState_WORKFLOW_STATE_RUNNING,
		}}},
		{Event: &api.FollowResponse_Step{Step: &api.StepEvent{
			Attempt: 1, Position: 2, Name: "test", State: api.StepState_STEP_STATE_RUNNING,
		}}},
		{Event: &api.FollowResponse_Output{Output: &api.OutputEvent{
			Attempt: 1, Position: 2, Offset: 10, Data: []byte("ok\n"),
		}}},
		{Event: &api.FollowResponse_Step{Step: &api.StepEvent{
			Attempt: 1, Position: 2, Name: "test", State: api.StepState_STEP_STATE_FINISHED,
			Outcome: api.Outcome_OUTCOME_SUCCESS, ExitStatus: proto.Int32(0),
		}}},
		{Event: &api.FollowResponse_End{End: &api.BuildEnd{Outcome: api.Outcome_OUTCOME_SUCCESS}}},
	}
)

func (f *fake) Next(ctx context.Context, req *protocol.NextRequest) (*protocol.NextResponse, error) {
	if err := f.record(ctx, req); err != nil {
		return nil, err
	}
	return nextRes, nil
}

func (f *fake) Accept(ctx context.Context, req *protocol.AcceptRequest) (*protocol.AcceptResponse, error) {
	if err := f.record(ctx, req); err != nil {
		return nil, err
	}
	return acceptRes, nil
}

func (f *fake) Update(ctx context.Context, req *protocol.UpdateRequest) (*protocol.UpdateResponse, error) {
	if err := f.record(ctx, req); err != nil {
		return nil, err
	}
	return &protocol.UpdateResponse{}, nil
}

func (f *fake) Log(ctx context.Context, req *protocol.LogRequest) (*protocol.LogResponse, error) {
	if err := f.record(ctx, req); err != nil {
		return nil, err
	}
	return logRes, nil
}

func (f *fake) Done(ctx context.Context, req *protocol.DoneRequest) (*protocol.DoneResponse, error) {
	if err := f.record(ctx, req); err != nil {
		return nil, err
	}
	return &protocol.DoneResponse{}, nil
}

func (f *fake) Submit(ctx context.Context, req *api.SubmitRequest) (*api.SubmitResponse, error) {
	if err := f.record(ctx, req); err != nil {
		return nil, err
	}
	return submitRes, nil
}

func (f *fake) Follow(ctx context.Context, req *api.FollowRequest, send func(*api.FollowResponse) error) error {
	if err := f.record(ctx, req); err != nil {
		return err
	}
	if f.follow != nil {
		return f.follow(ctx, send)
	}
	for _, e := range events {
		if err := send(e); err != nil {
			return err
		}
	}
	return nil
}

func (f *fake) record(ctx context.Context, req proto.Message) error {
	f.mu.Lock()
	f.reqs = append(f.reqs, req)
	f.mu.Unlock()
	if f.hook != nil {
		return f.hook(ctx)
	}
	return nil
}

// requests returns the requests the fake got, in order.
func (f *fake) requests() []proto.Message {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]proto.Message(nil), f.reqs...)
}

// rpc returns a Connect unary request for procedure, with an empty body,
// which every RunnerService and BuildService request accepts.
func rpc(method, procedure, contentType string) *http.Request {
	r := httptest.NewRequestWithContext(context.Background(), method, procedure, bytes.NewReader(nil))
	r.Header.Set("Content-Type", contentType)
	return r
}

const nextProcedure = "/crewactions.protocol.v1.RunnerService/Next"

func TestHostCheck(t *testing.T) {
	cases := []struct {
		host string
		want int
	}{
		{"evil.example", http.StatusForbidden},
		{"localhost.evil.example", http.StatusForbidden},
		{"127.0.0.1.nip.io", http.StatusForbidden},
		{"0.0.0.0:7700", http.StatusForbidden},
		{"", http.StatusForbidden},
		{"localhost:7700", http.StatusOK},
		{"LOCALHOST:7700", http.StatusOK},
		{"127.0.0.1:7700", http.StatusOK},
		{"[::1]:7700", http.StatusOK},
	}
	for _, tc := range cases {
		f := &fake{}
		r := rpc(http.MethodPost, nextProcedure, "application/proto")
		r.Host = tc.host
		w := httptest.NewRecorder()
		NewHandler(f, f).ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("Host %q: status %d, want %d", tc.host, w.Code, tc.want)
		}
		if got, want := len(f.requests()), map[bool]int{true: 1}[tc.want == http.StatusOK]; got != want {
			t.Errorf("Host %q: the engine got %d requests, want %d", tc.host, got, want)
		}
	}
}

func TestCrossOriginRequestIsRefused(t *testing.T) {
	f := &fake{}
	r := rpc(http.MethodPost, nextProcedure, "application/proto")
	r.Host = "127.0.0.1:7700"
	r.Header.Set("Origin", "https://evil.example")
	w := httptest.NewRecorder()
	NewHandler(f, f).ServeHTTP(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("status %d, want %d", w.Code, http.StatusForbidden)
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("the engine got %d requests, want none", n)
	}
}

func TestRequestsThatAreNotConnectCallsAreRefused(t *testing.T) {
	cases := []struct {
		method, procedure, contentType string
		want                           int
	}{
		{http.MethodPost, "/crewactions.api.v1.BuildService/Submit", "text/plain", http.StatusUnsupportedMediaType},
		{http.MethodGet, "/crewactions.api.v1.BuildService/Submit", "application/proto", http.StatusMethodNotAllowed},
		{http.MethodGet, "/crewactions.api.v1.BuildService/Follow", "application/connect+proto", http.StatusMethodNotAllowed},
		{http.MethodGet, nextProcedure, "application/proto", http.StatusMethodNotAllowed},
	}
	for _, tc := range cases {
		f := &fake{}
		r := rpc(tc.method, tc.procedure, tc.contentType)
		r.Host = "localhost:7700"
		w := httptest.NewRecorder()
		NewHandler(f, f).ServeHTTP(w, r)
		if w.Code != tc.want {
			t.Errorf("%s %s as %s: status %d, want %d", tc.method, tc.procedure, tc.contentType, w.Code, tc.want)
		}
		if n := len(f.requests()); n != 0 {
			t.Errorf("%s %s as %s: the engine got %d requests, want none", tc.method, tc.procedure, tc.contentType, n)
		}
	}
}
