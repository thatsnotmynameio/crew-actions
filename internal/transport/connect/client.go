package connect

import (
	"context"
	"errors"
	"io"
	"net/http"

	"connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"

	"github.com/thatsnotmynameio/crew-actions/internal/api"
	"github.com/thatsnotmynameio/crew-actions/internal/protocol"
	"github.com/thatsnotmynameio/crew-actions/internal/transport/connect/api/apiconnect"
	"github.com/thatsnotmynameio/crew-actions/internal/transport/connect/protocol/protocolconnect"
)

// errNoEnd is what Follow returns when the stream ends before the build's
// end, which a server that is stopping does.
var errNoEnd = errors.New("the stream ended before the build's end")

// newClient returns a connect-go client of the server at baseURL. Its HTTP
// client has no timeout, which would cut Next's wait and Follow's stream.
func newClient(baseURL string) *connect.Client {
	return connect.NewClient(connecthttp.NewTransport(&http.Client{}, baseURL))
}

// NewEngineClient returns the engine at baseURL, such as
// http://127.0.0.1:7700, as a runner sees it. Its calls fail with the
// sentinels of internal/model, with model.ErrUnavailable when the engine
// cannot be reached, and with the context's error when their context ends.
func NewEngineClient(baseURL string) protocol.Engine {
	return engineClient{client: protocolconnect.NewRunnerServiceClient(newClient(baseURL))}
}

// engineClient calls RunnerService.
type engineClient struct {
	client protocolconnect.RunnerServiceClient
}

func (c engineClient) Next(ctx context.Context, req *protocol.NextRequest) (*protocol.NextResponse, error) {
	res, err := c.client.Next(ctx, req)
	if err != nil {
		return nil, fromConnect(ctx, err)
	}
	return res, nil
}

func (c engineClient) Accept(ctx context.Context, req *protocol.AcceptRequest) (*protocol.AcceptResponse, error) {
	res, err := c.client.Accept(ctx, req)
	if err != nil {
		return nil, fromConnect(ctx, err)
	}
	return res, nil
}

func (c engineClient) Update(ctx context.Context, req *protocol.UpdateRequest) (*protocol.UpdateResponse, error) {
	res, err := c.client.Update(ctx, req)
	if err != nil {
		return nil, fromConnect(ctx, err)
	}
	return res, nil
}

func (c engineClient) Log(ctx context.Context, req *protocol.LogRequest) (*protocol.LogResponse, error) {
	res, err := c.client.Log(ctx, req)
	if err != nil {
		return nil, fromConnect(ctx, err)
	}
	return res, nil
}

func (c engineClient) Done(ctx context.Context, req *protocol.DoneRequest) (*protocol.DoneResponse, error) {
	res, err := c.client.Done(ctx, req)
	if err != nil {
		return nil, fromConnect(ctx, err)
	}
	return res, nil
}

// NewBuildsClient returns the builds of the engine at baseURL as a client
// sees them, failing as NewEngineClient's calls do. Its Follow also fails
// with model.ErrUnavailable when the stream ends before the build's end.
func NewBuildsClient(baseURL string) api.Builds {
	return buildsClient{client: apiconnect.NewBuildServiceClient(newClient(baseURL))}
}

// buildsClient calls BuildService.
type buildsClient struct {
	client apiconnect.BuildServiceClient
}

func (c buildsClient) Submit(ctx context.Context, req *api.SubmitRequest) (*api.SubmitResponse, error) {
	res, err := c.client.Submit(ctx, req)
	if err != nil {
		return nil, fromConnect(ctx, err)
	}
	return res, nil
}

func (c buildsClient) Follow(ctx context.Context, req *api.FollowRequest, send func(*api.FollowResponse) error) error {
	stream, err := c.client.Follow(ctx, req)
	if err != nil {
		return fromConnect(ctx, err)
	}
	defer stream.Close()
	for {
		res, err := stream.Receive()
		if errors.Is(err, io.EOF) {
			return fromConnect(ctx, errNoEnd)
		}
		if err != nil {
			return fromConnect(ctx, err)
		}
		if err := send(res); err != nil {
			return err
		}
		if res.GetEnd() != nil {
			return nil
		}
	}
}
