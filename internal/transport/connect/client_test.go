package connect

import (
	"context"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/thatsnotmynameio/crew-actions/internal/api"
	"github.com/thatsnotmynameio/crew-actions/internal/model"
	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
)

// serve starts the handler over f on a loopback HTTP server and returns
// clients of both services against it.
func serve(t *testing.T, f *fake) (protocol.Engine, api.Builds, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(NewHandler(f, f))
	t.Cleanup(srv.Close)
	return NewEngineClient(srv.URL), NewBuildsClient(srv.URL), srv
}

// sentinels are the errors a caller branches on.
var sentinels = []error{model.ErrConflict, model.ErrNotFound, model.ErrInvalid, model.ErrUnavailable}

// equal reports whether got and want hold the same fields.
func equal(t *testing.T, what string, got, want proto.Message) {
	t.Helper()
	if !proto.Equal(got, want) {
		t.Errorf("%s = %v, want %v", what, got, want)
	}
}

// collect follows a build with c and returns the events it got.
func collect(ctx context.Context, c api.Builds) ([]*api.FollowResponse, error) {
	var got []*api.FollowResponse
	err := c.Follow(ctx, &api.FollowRequest{BuildId: 42}, func(e *api.FollowResponse) error {
		got = append(got, e)
		return nil
	})
	if err != nil {
		return got, fmt.Errorf("follow: %w", err)
	}
	return got, nil
}

// checkRequests checks that f got want, in order.
func checkRequests(t *testing.T, f *fake, want ...proto.Message) {
	t.Helper()
	got := f.requests()
	if len(got) != len(want) {
		t.Fatalf("the engine got %d requests, want %d", len(got), len(want))
	}
	for i := range want {
		equal(t, "request", got[i], want[i])
	}
}

func TestRunnerCallsRoundTrip(t *testing.T) {
	f := &fake{}
	engine, _, _ := serve(t, f)
	ctx := context.Background()
	nextReq := &protocol.NextRequest{}
	acceptReq := &protocol.AcceptRequest{WorkflowId: 3, Version: 4}
	updateReq := &protocol.UpdateRequest{
		AttemptId: 9, Position: 2, State: protocol.StepState_STEP_STATE_FINISHED,
		Outcome: protocol.Outcome_OUTCOME_FAILURE, ExitStatus: proto.Int32(1),
	}
	logReq := &protocol.LogRequest{AttemptId: 9, Position: 2, Offset: 1200, Data: []byte("line\n")}
	doneReq := &protocol.DoneRequest{AttemptId: 9, Outcome: protocol.Outcome_OUTCOME_FAILURE}

	next, err := engine.Next(ctx, nextReq)
	if err != nil {
		t.Fatalf("Next: %v", err)
	}
	equal(t, "Next", next, nextRes)
	accept, err := engine.Accept(ctx, acceptReq)
	if err != nil {
		t.Fatalf("Accept: %v", err)
	}
	equal(t, "Accept", accept, acceptRes)
	update, err := engine.Update(ctx, updateReq)
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	equal(t, "Update", update, &protocol.UpdateResponse{})
	log, err := engine.Log(ctx, logReq)
	if err != nil {
		t.Fatalf("Log: %v", err)
	}
	equal(t, "Log", log, logRes)
	done, err := engine.Done(ctx, doneReq)
	if err != nil {
		t.Fatalf("Done: %v", err)
	}
	equal(t, "Done", done, &protocol.DoneResponse{})
	checkRequests(t, f, nextReq, acceptReq, updateReq, logReq, doneReq)
}

func TestBuildCallsRoundTrip(t *testing.T) {
	f := &fake{}
	_, builds, _ := serve(t, f)
	ctx := context.Background()
	submitReq := &api.SubmitRequest{
		Host: "github.com", Organization: "acme", Repository: "app",
		FileName: "build", Content: []byte("steps: []\n"),
	}

	submit, err := builds.Submit(ctx, submitReq)
	if err != nil {
		t.Fatalf("Submit: %v", err)
	}
	equal(t, "Submit", submit, submitRes)
	got, err := collect(ctx, builds)
	if err != nil {
		t.Fatalf("Follow: %v", err)
	}
	if len(got) != len(events) {
		t.Fatalf("Follow sent %d events, want %d", len(got), len(events))
	}
	for i := range events {
		equal(t, "event", got[i], events[i])
	}
	checkRequests(t, f, submitReq, &api.FollowRequest{BuildId: 42})
}

func TestEngineErrorsRoundTrip(t *testing.T) {
	cases := []struct {
		err  error
		want error
		msg  string
	}{
		{model.Invalid("line 3: steps must be a list"), model.ErrInvalid, "line 3: steps must be a list"},
		{model.ErrConflict, model.ErrConflict, ""},
		{model.ErrNotFound, model.ErrNotFound, ""},
		{model.ErrUnavailable, model.ErrUnavailable, ""},
	}
	for _, tc := range cases {
		engine, builds, _ := serve(t, &fake{hook: func(context.Context) error { return tc.err }})
		ctx := context.Background()
		_, acceptErr := engine.Accept(ctx, &protocol.AcceptRequest{WorkflowId: 3, Version: 4})
		_, submitErr := builds.Submit(ctx, &api.SubmitRequest{FileName: "build"})
		for _, err := range []error{acceptErr, submitErr} {
			if !errors.Is(err, tc.want) {
				t.Errorf("engine error %v: client error %v, want one matching %v", tc.err, err, tc.want)
			}
			if tc.msg != "" && err.Error() != tc.msg {
				t.Errorf("engine error %v: client message %q, want %q", tc.err, err.Error(), tc.msg)
			}
		}
	}
}

func TestPlainErrorReachesClientWithoutMessage(t *testing.T) {
	engine, _, _ := serve(t, &fake{hook: func(context.Context) error {
		return errors.New("open /var/lib/crew-actions/secret: permission denied")
	}})
	_, err := engine.Next(context.Background(), &protocol.NextRequest{})
	if err == nil {
		t.Fatal("Next: nil error, want one")
	}
	if strings.Contains(err.Error(), "secret") || strings.Contains(err.Error(), "permission") {
		t.Errorf("Next error %q carries the engine's message", err)
	}
	for _, s := range sentinels {
		if errors.Is(err, s) {
			t.Errorf("Next error %v matches %v, want none of the sentinels", err, s)
		}
	}
}

func TestOversizedSubmitIsInvalid(t *testing.T) {
	f := &fake{}
	_, builds, _ := serve(t, f)
	_, err := builds.Submit(context.Background(), &api.SubmitRequest{
		FileName: "build", Content: make([]byte, 2<<20),
	})
	if !errors.Is(err, model.ErrInvalid) {
		t.Errorf("Submit of 2 MiB: error %v, want one matching model.ErrInvalid", err)
	}
	if n := len(f.requests()); n != 0 {
		t.Errorf("the engine got %d requests, want none", n)
	}
}

func TestFollowUnavailableMidStream(t *testing.T) {
	_, builds, _ := serve(t, &fake{follow: func(_ context.Context, send func(*api.FollowResponse) error) error {
		if err := send(events[0]); err != nil {
			return err
		}
		return model.ErrUnavailable
	}})
	got, err := collect(context.Background(), builds)
	if len(got) != 1 {
		t.Errorf("Follow sent %d events, want 1", len(got))
	}
	if !errors.Is(err, model.ErrUnavailable) {
		t.Errorf("Follow error %v, want one matching model.ErrUnavailable", err)
	}
}

func TestFollowEndingWithoutBuildEndIsUnavailable(t *testing.T) {
	_, builds, _ := serve(t, &fake{follow: func(_ context.Context, send func(*api.FollowResponse) error) error {
		return send(events[0])
	}})
	if _, err := collect(context.Background(), builds); !errors.Is(err, model.ErrUnavailable) {
		t.Errorf("Follow error %v, want one matching model.ErrUnavailable", err)
	}
}

func TestFollowReturnsSendError(t *testing.T) {
	_, builds, _ := serve(t, &fake{})
	stop := errors.New("stop")
	err := builds.Follow(context.Background(), &api.FollowRequest{BuildId: 42}, func(*api.FollowResponse) error {
		return stop
	})
	if !errors.Is(err, stop) {
		t.Errorf("Follow error %v, want send's error", err)
	}
}

// blocked returns a hook that signals started and waits for the call's
// context to end.
func blocked(started chan<- struct{}) func(ctx context.Context) error {
	return func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}
}

func TestCancelledCallReturnsContextError(t *testing.T) {
	calls := map[string]func(protocol.Engine, api.Builds, context.Context) error{
		"Next": func(e protocol.Engine, _ api.Builds, ctx context.Context) error {
			if _, err := e.Next(ctx, &protocol.NextRequest{}); err != nil {
				return fmt.Errorf("next: %w", err)
			}
			return nil
		},
		"Follow": func(_ protocol.Engine, b api.Builds, ctx context.Context) error {
			_, err := collect(ctx, b)
			return err
		},
	}
	for name, call := range calls {
		started := make(chan struct{})
		engine, builds, _ := serve(t, &fake{hook: blocked(started)})
		ctx, cancel := context.WithCancel(context.Background())
		go func() {
			<-started
			cancel()
		}()
		err := call(engine, builds, ctx)
		if !errors.Is(err, context.Canceled) || errors.Is(err, model.ErrUnavailable) {
			t.Errorf("%s cancelled mid-call: error %v, want context.Canceled and not model.ErrUnavailable", name, err)
		}
	}
}

func TestClosedServerIsUnavailable(t *testing.T) {
	engine, builds, srv := serve(t, &fake{})
	srv.Close()
	ctx := context.Background()
	if _, err := engine.Next(ctx, &protocol.NextRequest{}); !errors.Is(err, model.ErrUnavailable) {
		t.Errorf("Next against a closed server: error %v, want one matching model.ErrUnavailable", err)
	}
	if _, err := collect(ctx, builds); !errors.Is(err, model.ErrUnavailable) {
		t.Errorf("Follow against a closed server: error %v, want one matching model.ErrUnavailable", err)
	}
}

func TestFollowCutByServerIsUnavailable(t *testing.T) {
	f := &fake{follow: func(ctx context.Context, send func(*api.FollowResponse) error) error {
		if err := send(events[0]); err != nil {
			return err
		}
		<-ctx.Done()
		return ctx.Err()
	}}
	_, builds, srv := serve(t, f)
	err := builds.Follow(context.Background(), &api.FollowRequest{BuildId: 42}, func(*api.FollowResponse) error {
		srv.CloseClientConnections()
		return nil
	})
	if !errors.Is(err, model.ErrUnavailable) {
		t.Errorf("Follow cut by the server: error %v, want one matching model.ErrUnavailable", err)
	}
}
