---
title: A non-remote connect-go error does not mean the engine is unreachable, so it must not all become ErrUnavailable
date: 2026-10-14
last_updated: 2026-10-14
category: best-practices
module: internal/transport/connect
problem_type: best_practice
component: api_layer
severity: medium
root_cause: wrong_api
applies_when:
  - A change touches fromConnect or fromLocal in internal/transport/connect/errors.go, or any client path that returns model.ErrUnavailable
  - Code retries a call, or restarts Follow, when it gets model.ErrUnavailable, such as the runner or run
  - A plan or a doc says a non-remote error is a refused connection or a cut stream
  - Code adds a client read limit, an HTTP middleware that answers with a status, or a new RPC to the connect transport
  - A change upgrades connect-go, whose error messages fromLocal matches by text
  - A test checks that a Follow cancelled as its stream ends returns the context's error
tags: [connect-go, isremote, error-mapping, err-unavailable, retry, follow, context-cancellation, envelope, connection-reset, ktd4, ktd11]
---

# A non-remote connect-go error does not mean the engine is unreachable, so it must not all become ErrUnavailable

## Context

Issue #75 added the connect transport. KTD4 in its plan, copied from the plan for #69, says: "A non-remote error, such as a refused connection or a stream cut by a dying server, also comes back as `ErrUnavailable`" (`docs/plans/2026-10-13-1434-issue-69-plan.md:40`, `docs/plans/2026-10-14-0438-issue-75-plan.md:47`, `docs/plans/2026-10-14-0436-issue-73-plan.md:66`). The client did exactly that: it returned `model.ErrUnavailable` for any error that was not a `*connect.Error` or whose `IsRemote()` was false.

`model.ErrUnavailable` means "the server is stopping or cannot be reached" (`internal/model/errors.go:16`). The runner and `run` retry on it: an idle `next` waits for the engine (`internal/runner/runner.go:86`), and `api.Follow` reconnects (`internal/api/follow.go:33`). So the broad mapping made a wrong base URL look like an engine outage, and retried it forever.

#105 narrowed the mapping (KTD11 in `docs/plans/2026-10-14-1225-issue-105-plan.md`). The plans for #69, #73 and #75 are not updated after they ship, so they still state KTD4's broad sentence. KTD11 replaces it.

The review of #105 then found that the narrowed mapping misses two ways a dying engine shows up. Both were confirmed with throwaway raw servers and left unapplied (see "Known gaps" below).

## Guidance

`IsRemote()` false only means the error did not come from a Connect error body the server sent. connect-go makes such errors in all of these cases. The last column is what the client returns since #105:

| Case | connect-go's error | Code, remote | Client returns |
|---|---|---|---|
| Connection refused | `unavailable: dial tcp ...: connection refused` | `unavailable`, no | `ErrUnavailable` |
| Stream cut between messages (inside the 5-byte prefix) | `protocol error: incomplete envelope: unexpected EOF` | `invalid_argument`, no | `ErrUnavailable` |
| Stream cut after a prefix, body ended cleanly | `protocol error: promised N bytes in enveloped message, got M bytes` | `invalid_argument`, no | `errServer` (gap) |
| Stream cut after a prefix, body read failed | `read enveloped message: unexpected EOF` | `unknown`, no | `errServer` (gap) |
| Connection reset by the engine (RST) | `canceled: ... connection reset by peer` | `canceled`, no | `errServer` (gap) |
| Engine answered 403 (its own Host check, for a base URL of `http://0.0.0.0:<port>` or the machine's hostname) | `permission_denied: 403 Forbidden` | `permission_denied`, no | `errServer` |
| Engine answered 404 (wrong path in the base URL) | `unimplemented: 404 Not Found` | `unimplemented`, no | `errServer` |
| Request fails to marshal on the client (a `Submit` `FileName` with invalid UTF-8) | `internal: marshal message: string field contains invalid UTF-8` | `internal`, no | `errServer` |
| A `Follow` event over the client's read limit (4 MiB by default, connect-go's `connecthttp/protocol.go:43`; `newClient` sets none, `internal/transport/connect/client.go:25`) | `message size N is larger than configured max M` | `resource_exhausted`, no | `errServer` |

The rows were reproduced against the tree, except the read limit, which comes from connect-go's source.

Where connect-go makes them, with paths inside the module `connectrpc.com/connect/v2@v2.0.0`:

- Unary calls turn a non-200 response without a Connect JSON body into `connect.NewError(httpToCode(statusCode), statusMsg)` with no `WithRemote` (`connecthttp/protocol_connect.go:1446`). Streams do the same, with the message "HTTP status <status>", at `connecthttp/protocol_connect.go:687`.
- A dial or read failure that is not already a connect error becomes `CodeUnavailable` (`connecthttp/duplex_http_call.go:355`). Before that, `wrapIfContextError` turns `ECONNRESET` or `EPIPE` into `CodeCanceled` (`connecthttp/error.go:142-156`, called at `duplex_http_call.go:350`). It treats them as a client that went away, but on the client they mean the server did.
- `connecthttp/envelope.go` has three messages for a stream that stops early, and only the first one is "incomplete envelope":
  - a failed read of the 5-byte prefix: `CodeInvalidArgument`, "protocol error: incomplete envelope", with the read error as its cause (`:335-338`);
  - a body that ends cleanly before the size the prefix promised: `CodeInvalidArgument`, "protocol error: promised N bytes in enveloped message, got M bytes", with no cause attached (`:357-362`);
  - a body read that fails: `CodeUnknown`, "read enveloped message: ...", with the read error, such as `io.ErrUnexpectedEOF`, as its cause (`:369`).

So:

- Map to `ErrUnavailable` only what means the engine is gone or going. Today that is a non-`*connect.Error`, a non-remote `CodeUnavailable`, and the incomplete envelope (`internal/transport/connect/errors.go:81-84`). It should also cover the two body cuts and a connection reset (see "Known gaps").
- Send every other non-remote error to `errServer`, which nothing retries. Keep its text in the error, and never return a connect code to the caller (`errors.go:85`).
- Do not split on the code alone. A cut stream comes as `invalid_argument`, the same code an `ErrInvalid` from the engine has. Only `IsRemote()` tells them apart, which is why `fromConnect` sends every non-remote error to `fromLocal` before its switch on codes (`errors.go:57-58`).
- `fromLocal` recognises a cut stream by connect-go's message text. `TestFollowCutByServerIsUnavailable` pins it: disabling the cut branch makes that test fail. A connect-go upgrade that changes the text fails that test too, so read `envelope.go` again when it does.
- Check `ctx.Err()` first on every path that returns `ErrUnavailable`. `fromConnect` does it first (`errors.go:50`), and `Follow`'s `io.EOF` branch now goes through `fromConnect(ctx, errNoEnd)` (`internal/transport/connect/client.go:110`), so a Ctrl-C as the stream ends is never retried.

### Known gaps, left unapplied by #105

The review of #105 confirmed both at P2:

- **A cut after a message's prefix stops `Follow`.** When the engine dies while writing an event, the client gets one of the two body-cut errors above. `fromLocal` returns `errServer`, so `api.Follow` returns instead of reconnecting. Before #105 that error was `ErrUnavailable`. A raw server that sent a prefix promising 100 bytes, then 10 bytes, then closed, gave "the engine failed the call: invalid_argument: protocol error: promised 100 bytes..." with a plain body and "the engine failed the call: unknown: read enveloped message: unexpected EOF" with a chunked one.
- **A connection reset stops the runner.** The caller's context is live, so `fromConnect`'s context check does not catch the `CodeCanceled`, and `fromLocal` returns `errServer`. `runner.next` then returns "ask for work: ..." and the runner exits. A raw server that read the request, then closed with `SetLinger(0)`, gave "the engine failed the call: canceled: Post .../RunnerService/Next: ... connection reset by peer" for `Next`, and the same for `Follow`.

The fix the review suggests: in `fromLocal`, also return `unavailable(err)` for a message that starts with "protocol error: promised ", for a cause chain that holds `io.ErrUnexpectedEOF`, and for `errors.Is(err, syscall.ECONNRESET)`. Test each with a raw server: one that sends a prefix promising more bytes than it writes, then closes, and one that resets the connection. The comment on `incompleteEnvelope` (`errors.go:39-40`) also calls a prefix cut a cut "in the middle of a message", which it is not.

### Testing the context check on Follow's early end

connect-go checks `ctx.Err()` before every read of the response body. A test that cancels the context any earlier than the body's last read gets the context's error from connect-go itself and never reaches `Follow`'s `io.EOF` branch. The first version of #105's test cancelled from the send callback and passed before the fix, so it tested nothing.

`TestFollowCancelledAsStreamEndsEarlyReturnsContextError` (`internal/transport/connect/client_test.go:347`) gives the client an HTTP transport whose response body calls `cancel` as its read returns `io.EOF` (`cancelAtEOF`, `client_test.go:317-345`). It failed before the fix and passes after it.

## Why This Matters

Since #76, #77 and #78, the runner and `run` retry on `ErrUnavailable`. A mapping that is too broad and one that is too narrow both break them:

- Too broad, as before #105: a runner or `run` started with a wrong base URL retries a 403 or a 404 forever, and the error reads like an engine outage. A `Follow` event over the read limit restarts the stream from the build's start, hits the same event, and never reaches the build's end. A request that cannot be marshalled is resent forever.
- Too narrow, as the gaps above: an engine that dies mid-event, or whose connections are reset, makes `run` stop following and the runner exit. It should wait for the engine to come back.

A configuration or input error should fail once, with its cause. An engine that is gone should be waited for.

## When to Apply

- When fixing the two gaps above, or when adding any other case to `fromLocal`.
- When citing KTD4 from the plans for #69, #73 or #75. Its "non-remote" sentence was replaced by KTD11 in the plan for #105.
- When adding a client read limit, an HTTP check in front of the handler, or a new RPC to `internal/transport/connect`.
- When upgrading connect-go.

## Examples

`fromLocal` since #105 (`internal/transport/connect/errors.go:80-86`):

```go
func fromLocal(err *connect.Error) error {
	cut := err.Code() == connect.CodeInvalidArgument && strings.HasPrefix(err.Message(), incompleteEnvelope)
	if err.Code() == connect.CodeUnavailable || cut {
		return unavailable(err)
	}
	return fmt.Errorf("%w: %s", errServer, err.Error())
}
```

A client against the wrong address, with `connect` here being `internal/transport/connect`:

```go
connect.NewEngineClient("http://0.0.0.0:7700").Next(ctx, req)
// before #105: unavailable: permission_denied: 403 Forbidden           (errors.Is(err, model.ErrUnavailable) == true)
// since #105:  the engine failed the call: permission_denied: 403 Forbidden (errors.Is(err, model.ErrUnavailable) == false)
```

`TestWrongAddressIsNotUnavailable` (`client_test.go:299`) pins both the 403 and the 404, for `Next` and for `Follow`.

## Related

- Issue #105 (KTD11, the narrowed mapping, and its two unapplied review findings), #75 (the transport and the first review finding), #69 (KTD4 first written), #76, #77 and #78 (the callers that retry), #22 and #23 (parents).
- `docs/solutions/best-practices/select-on-exit-and-cancel-must-recheck-exit.md`: the same rule, re-check the other signal before reporting a failure, for a process's exit.
