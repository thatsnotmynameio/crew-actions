---
title: A non-remote connect-go error does not mean the engine is unreachable, so it must not all become ErrUnavailable
date: 2026-10-14
category: best-practices
module: internal/transport/connect
problem_type: best_practice
component: api_layer
severity: medium
root_cause: wrong_api
applies_when:
  - A change touches fromConnect in internal/transport/connect/errors.go, or any client path that returns model.ErrUnavailable
  - Code retries a call, or restarts Follow, when it gets model.ErrUnavailable, such as the runner or run
  - A plan or a doc says a non-remote error is a refused connection or a cut stream
  - Code adds a client read limit, an HTTP middleware that answers with a status, or a new RPC to the connect transport
tags: [connect-go, isremote, error-mapping, err-unavailable, retry, follow, context-cancellation, ktd4]
---

# A non-remote connect-go error does not mean the engine is unreachable, so it must not all become ErrUnavailable

## Context

Issue #75 added the connect transport. KTD4 in its plan, copied from the plan for #69, says: "A non-remote error, such as a refused connection or a stream cut by a dying server, also comes back as `ErrUnavailable`" (`docs/plans/2026-10-13-1434-issue-69-plan.md:40`, `docs/plans/2026-10-14-0438-issue-75-plan.md:47`). The client does exactly that. `fromConnect` returns `model.ErrUnavailable` for any error that is not a `*connect.Error` or whose `IsRemote()` is false (`internal/transport/connect/errors.go:48`).

`model.ErrUnavailable` means "the server is stopping or cannot be reached" (`internal/model/errors.go:16`). The runner and `run` are meant to retry on it: #77 has the client follow a build across engine restarts.

The review of #75 found that connect-go v2 (v2.0.0) makes non-remote errors for more than an engine it cannot reach. The review rated it P2, and #75's change does not apply it. The plans are not updated after they ship, so they still state the broad mapping.

## Guidance

`IsRemote()` false only means the error did not come from a Connect error body the server sent. connect-go makes such errors in all of these cases:

| Case | What the client gets now | Code, remote |
|---|---|---|
| Connection refused | `unavailable: dial tcp ...: connection refused` | `unavailable`, no |
| Stream cut by the server mid-flight | `protocol error: incomplete envelope: unexpected EOF` | `invalid_argument`, no |
| Engine answered 403 (its own Host check, for a base URL of `http://0.0.0.0:<port>` or the machine's hostname) | `unavailable: permission_denied: 403 Forbidden` | `permission_denied`, no |
| Engine answered 404 (wrong path in the base URL) | `unavailable: unimplemented: 404 Not Found` | `unimplemented`, no |
| Request fails to marshal on the client (a `Submit` `FileName` with invalid UTF-8) | `unavailable: internal: marshal message: string field contains invalid UTF-8` | `internal`, no |
| A `Follow` event over the client's read limit (4 MiB by default, connect-go's `connecthttp/protocol.go:43`; `newClient` sets none, `internal/transport/connect/client.go:25`) | `ErrUnavailable` | `resource_exhausted`, no |

The first five rows were reproduced against the tree. The last comes from connect-go's source and the review, not from a run.

Where connect-go makes them, with paths inside the module `connectrpc.com/connect/v2@v2.0.0`:

- Unary calls turn a non-200 response without a Connect JSON body into `connect.NewError(httpToCode(statusCode), statusMsg)` with no `WithRemote` (`connecthttp/protocol_connect.go:1446`). Streams do the same, with the message "HTTP status <status>", at `connecthttp/protocol_connect.go:687`.
- A dial or read failure that is not already a connect error becomes `CodeUnavailable` (`connecthttp/duplex_http_call.go:355`).
- A stream that stops in the middle of an envelope becomes `CodeInvalidArgument`, "protocol error: incomplete envelope", with the read error as its cause (`connecthttp/envelope.go:335-338`).

So:

- Map to `ErrUnavailable` only what means the engine is gone or going: a non-`*connect.Error`, a non-remote `CodeUnavailable`, and the incomplete envelope of a cut stream. Keep `TestClosedServerIsUnavailable` and `TestFollowCutByServerIsUnavailable` in `internal/transport/connect/client_test.go` green.
- Send the other non-remote errors to `errServer` or to a new error that is never retried. Keep the cause in it, and never return a connect code to the caller.
- Do not split on the code alone. A cut stream comes as `invalid_argument`, the same code an `ErrInvalid` from the engine has. Only `IsRemote()` tells them apart. A mapping that looks only at codes, or drops the `IsRemote` check, would show "protocol error: incomplete envelope" to the submitter as an invalid request. A mapping that keeps `ErrUnavailable` for non-remote `unavailable` only would make a dying engine look like a failed call.
- Pin the fix with a test whose base URL is `http://0.0.0.0:<port>` and that expects no `ErrUnavailable`.
- Check `ctx.Err()` first on every path that returns `ErrUnavailable`, not only in `fromConnect`. `Follow`'s `io.EOF` branch returns `unavailable(errNoEnd)` without it (`internal/transport/connect/client.go:109`). If the caller's context is cancelled after the stream ends early, a Ctrl-C comes back as `ErrUnavailable` and gets retried. KTD4 says that never happens. #75's change does not fix this either.

## Why This Matters

No caller retries yet, so nothing loops today. Once the runner and `run` retry on `ErrUnavailable`:

- a runner or `run` started with a wrong base URL retries a 403 or a 404 forever, and the error reads like an engine outage;
- `Follow` starts again from the build's start (`internal/api/api.go:25`), hits the same oversized event, and never reaches the build's end;
- a request that cannot be marshalled is resent forever.

Each of these is a configuration or input error that should fail once, with its cause.

## When to Apply

- When fixing the P2 finding left on #75, or when #76 or #77 starts retrying on `ErrUnavailable`.
- When citing KTD4 from the plans for #69 or #75. Its "non-remote" sentence describes the two cases it names, not every non-remote error.
- When adding a client read limit, an HTTP check in front of the handler, or a new RPC to `internal/transport/connect`.

## Examples

What `fromConnect` does now (`internal/transport/connect/errors.go:44-50`):

```go
if ctxErr := ctx.Err(); ctxErr != nil {
	return ctxErr
}
cerr, ok := errors.AsType[*connect.Error](err)
if !ok || !cerr.IsRemote() {
	return unavailable(err) // also a 403, a 404, a marshal failure, a read limit
}
```

What a client against the wrong address gets from the engine's own Host check, with `connect` here being `internal/transport/connect`:

```go
connect.NewEngineClient("http://0.0.0.0:7700").Next(ctx, req)
// unavailable: permission_denied: 403 Forbidden  (errors.Is(err, model.ErrUnavailable) == true)
```

## Related

- Issue #75 (this change, and its unapplied review findings), #69 (KTD4 first written), #76 and #77 (callers that will retry), #22 (parent).
- `docs/solutions/best-practices/select-on-exit-and-cancel-must-recheck-exit.md`: the same rule, re-check the other signal before reporting a failure, for a process's exit.
