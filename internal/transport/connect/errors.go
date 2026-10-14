package connect

import (
	"context"
	"errors"
	"fmt"

	"connectrpc.com/connect/v2"

	"github.com/thatsnotmynameio/crew-actions/internal/model"
)

// errServer is what a client gets for an error the engine did not mean to
// show, such as a failure of its store: the server sends its code alone.
var errServer = errors.New("the engine failed the call")

// toConnect returns the error the server sends for err, which the engine
// returned: a sentinel of internal/model becomes its code, with the message
// of an ErrInvalid, and any other error goes as connect-go sends it, as
// Unknown with no message unless connect-go made it itself.
func toConnect(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, model.ErrInvalid):
		return connect.NewError(connect.CodeInvalidArgument, err.Error())
	case errors.Is(err, model.ErrConflict):
		return connect.NewError(connect.CodeAborted, "")
	case errors.Is(err, model.ErrNotFound):
		return connect.NewError(connect.CodeNotFound, "")
	case errors.Is(err, model.ErrUnavailable):
		return connect.NewError(connect.CodeUnavailable, "")
	default:
		return err
	}
}

// fromConnect returns the error a client returns for err, which a call made
// with ctx failed with: the context's error when ctx is done, the sentinel
// of internal/model the server's code stands for, model.ErrUnavailable when
// the server sent no code, such as when it could not be reached or cut the
// stream, and errServer for any other code.
func fromConnect(ctx context.Context, err error) error {
	if ctxErr := ctx.Err(); ctxErr != nil {
		return ctxErr //nolint:wrapcheck // the caller compares it with its context's error
	}
	cerr, ok := errors.AsType[*connect.Error](err)
	if !ok || !cerr.IsRemote() {
		return unavailable(err)
	}
	switch cerr.Code() {
	case connect.CodeInvalidArgument, connect.CodeResourceExhausted:
		return model.Invalid(cerr.Message()) //nolint:wrapcheck // model.Invalid makes this error
	case connect.CodeAborted:
		return model.ErrConflict
	case connect.CodeNotFound:
		return model.ErrNotFound
	case connect.CodeUnavailable:
		return model.ErrUnavailable
	default:
		return errServer
	}
}

// unavailable returns model.ErrUnavailable with the reason err gives.
func unavailable(err error) error {
	return fmt.Errorf("%w: %s", model.ErrUnavailable, err.Error())
}
