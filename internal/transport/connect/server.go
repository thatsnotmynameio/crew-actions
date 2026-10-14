// Package connect serves the runner protocol and the client API over HTTP,
// with connect-go, and calls them as their clients. It is the one package
// that imports connect-go: the engine, the runner and run see only
// protocol.Engine, api.Builds and internal/model's sentinels.
//
// The engine listens on loopback only, and the protocol and the API have no
// authentication yet, so the handler refuses whatever a browser or another
// host could send it.
package connect

import (
	"context"
	"net"
	"net/http"
	"net/netip"
	"strings"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/thatsnotmynameio/crew-actions/internal/api"
	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
	"github.com/thatsnotmynameio/crew-actions/internal/transport/connect/api/apiconnect"
	"github.com/thatsnotmynameio/crew-actions/internal/transport/connect/protocol/protocolconnect"
)

// maxRequestBytes is the largest request the handler reads.
const maxRequestBytes = 1 << 20

// NewHandler returns the HTTP handler that serves engine as RunnerService
// and builds as BuildService. It reads requests of at most 1 MiB, refuses
// a cross-origin request from a browser, and answers 403 to a request whose
// Host is not localhost or a loopback IP, which keeps out DNS rebinding.
func NewHandler(engine protocol.Engine, builds api.Builds) http.Handler {
	server := connect.NewServer()
	protocolconnect.RegisterRunnerServiceHandler(server, runnerHandler{engine: engine})
	apiconnect.RegisterBuildServiceHandler(server, buildsHandler{builds: builds})
	mux := http.NewServeMux()
	connecthttp.Mount(mux, server, connecthttp.WithReadMaxBytes(maxRequestBytes))
	return checkHost(http.NewCrossOriginProtection().Handler(mux))
}

// checkHost answers 403 to a request whose Host is not loopback, and passes
// the others to next.
func checkHost(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !loopbackHost(r.Host) {
			http.Error(w, "the Host is not loopback", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// loopbackHost reports whether hostport, with or without its port, is
// localhost in any case or a loopback IP.
func loopbackHost(hostport string) bool {
	host, _, err := net.SplitHostPort(hostport)
	if err != nil {
		host = strings.TrimSuffix(strings.TrimPrefix(hostport, "["), "]")
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	addr, err := netip.ParseAddr(host)
	return err == nil && addr.IsLoopback()
}

// runnerHandler serves an engine as RunnerService.
type runnerHandler struct {
	engine protocol.Engine
}

func (h runnerHandler) Next(ctx context.Context, req *protocol.NextRequest) (*protocol.NextResponse, error) {
	res, err := h.engine.Next(ctx, req)
	return res, toConnect(err)
}

func (h runnerHandler) Accept(ctx context.Context, req *protocol.AcceptRequest) (*protocol.AcceptResponse, error) {
	res, err := h.engine.Accept(ctx, req)
	return res, toConnect(err)
}

func (h runnerHandler) Update(ctx context.Context, req *protocol.UpdateRequest) (*protocol.UpdateResponse, error) {
	res, err := h.engine.Update(ctx, req)
	return res, toConnect(err)
}

func (h runnerHandler) Log(ctx context.Context, req *protocol.LogRequest) (*protocol.LogResponse, error) {
	res, err := h.engine.Log(ctx, req)
	return res, toConnect(err)
}

func (h runnerHandler) Done(ctx context.Context, req *protocol.DoneRequest) (*protocol.DoneResponse, error) {
	res, err := h.engine.Done(ctx, req)
	return res, toConnect(err)
}

// buildsHandler serves an engine's builds as BuildService.
type buildsHandler struct {
	builds api.Builds
}

func (h buildsHandler) Submit(ctx context.Context, req *api.SubmitRequest) (*api.SubmitResponse, error) {
	res, err := h.builds.Submit(ctx, req)
	return res, toConnect(err)
}

func (h buildsHandler) Follow(
	ctx context.Context, req *api.FollowRequest, stream apiconnect.BuildServiceFollowServerStream,
) error {
	return toConnect(h.builds.Follow(ctx, req, stream.Send))
}
