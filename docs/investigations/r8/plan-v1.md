# R8 plan v1 — phase-aware read deadline preservation

## Baseline and authorization

- Worktree: `/Users/vonng/.codex/worktrees/3bae/silo`; HEAD and origin/main: `9ebe81c1b3611f9cc73e676b5b741c2be62c467a` (live checked).
- Go: go1.27.1 darwin/arm64; CLI: Claude Code 2.1.270. No production implementation has started.
- Root AGENTS.md is ignored and was absent from the generated worktree; copied the actual primary checkout guide into this worktree. Maintained PGSTY stack and inexpensive compatibility govern this repair.
- GitHub open PRs: #184 and #187, neither changes R8. No R8 implementation PR found.
- Parent workflow permits research and temporary reproductions before consensus; ordinary implementation after explicit same-version Codex/Opus consensus. Merge, publish and deployment excluded.

## Evidence and cause

`evidence/baseline.log` is a fresh real TCP run on this SHA with the parent's temporary Go overlay. Header limit 100ms, connection idle 2s, header finishes at 400ms. Standard net/http rejects; SILO returns HTTP 204. This is inherited DeadlineConn behavior, not a new SILO option.

`DeadlineConn.Read` calls `setReadDeadline`, replacing an explicit future deadline by now+readIdle+250ms. Past deadlines and explicit zero are already special (abort and disable respectively). `cmd/server-main.go` sets ReadTimeout and WriteTimeout to IdleTimeout as well as setting TCPOptions.IdleTimeout. Therefore globally preserving every explicit deadline makes normal HTTP/1 uploads time out at the idle duration in total.

Current Go source examined: `/opt/homebrew/Cellar/go/1.27.1/libexec/src/net/http/server.go` and `net/http/internal/http2/server.go`. Public semantics: https://pkg.go.dev/net/http#Server and https://pkg.go.dev/net#Conn .

- StateNew occurs before serving; TLS read timeout is min positive header/read/write timeouts.
- HTTP/1 reads under the configured header deadline. After parsing/validation, readRequest sets wholeReqDeadline based on ReadTimeout, then the serving loop calls StateActive (also on malformed input, followed by rejection).
- With a body, net/http registers an EOF callback; without a body it immediately calls startBackgroundRead. That clears the read deadline. Body EOF also clears the deadline before launching background disconnect detection.
- StateIdle happens after finishRequest/abortPendingRead, before setting keep-alive deadline and peeking the next request. The next header deadline is then installed. Thus StateIdle can restore strictness for both waiting and reading the next header.
- TLS wrapping remains `tls.Conn -> DeadlineConn -> TCPConn`; use tls.Conn.NetConn to reach the wrapper in the state callback.
- TLS can negotiate h2 (`cmd/utils.go`). HTTP/2 uses per-stream ReadTimeout (currently an absolute body limit), clears the underlying read deadline after handshake, and has independent connection state hooks. Do not impose a connection-wide request timer on multiplexed h2. Preserve this existing behavior; test HTTP/2 smoke and record that its preexisting absolute stream timeout is not fixed by this R8 HTTP/1 change.

## Options and decision

1. Globally clamp all future deadlines: too broad; turns ReadTimeout=idle into a total HTTP/1 upload limit, changes Linux internode caller behavior.
2. Remove ReadTimeout and interpret zero as rolling idle: wrong; zero is also how net/http disables background read deadlines, and a long handler could be spuriously canceled. H2 loses its existing per-stream read timeout.
3. Body-reader/ResponseController timer wrappers: possible but introduce body/drain/EOF bookkeeping, affect buffered/chunked reads, require h2-specific treatment and rewrite behavior beyond the header bug.
4. Selected: opt-in preservation of explicit read deadlines during HTTP/1 header/keep-alive/TLS phases; retain legacy rolling reads during the HTTP/1 body/handler phase. Keep generic DeadlineConn default and existing production timeout configuration.

## Concrete implementation

### internal/deadlineconn/deadlineconn.go

- Add mutex-protected last explicit read deadline and a `readDeadlineStrict` boolean (default false). Existing constructors and internode callers retain rolling semantics.
- Store the explicit read timestamp in SetReadDeadline and SetDeadline; preserve zero/abort flags and immediate forwarding to net.Conn.
- Expose a small `SetReadDeadlineStrict(bool)` method, documented as toggling whether automatic idle renewal may extend explicit deadlines. Lock the same mutex and reset readSetAt so the next read applies the current mode.
- In setReadDeadline, recheck abort/inf under the lock, keep existing throttling and idle slack, and when strict and explicit is nonzero take min(idleCandidate, explicit). Never add 250ms slack to the absolute limit. Deadline updates themselves are never throttled.
- Writes remain unchanged. Expired explicit times remain expired when strict. Existing explicit zero always disables read renewal, and explicit past time keeps abort semantics.

### internal/http/listener.go

- Keep the accepted concrete type *DeadlineConn, read/write idle durations and unwrap compatibility.
- Enable strict read mode before returning each newly accepted connection. This covers initial HTTP headers and TLS handshake reads, including a normal net/http Server using this listener.

### internal/http/server.go

- In Init, compose (do not drop) the caller's existing ConnState hook.
- Find the *DeadlineConn, unwrapping one *tls.Conn with NetConn when necessary.
- For HTTP/1 StateActive: turn strict mode off, retaining the current rolling ReadTimeout=idle semantics for bodies and long uploads. Do so before the caller's state hook.
- For StateNew/StateIdle: turn strict mode on. Ignore other states.
- For negotiated HTTP/2, skip per-request phase changes; after handshake raw zero deadlines stay disabled, and stream timeouts remain native net/http behavior.
- Do not remove ReadTimeout/WriteTimeout or change flags. Add a short explanatory comment around the production timeout setup if useful.

## Failure paths and compatibility

- Slow/incomplete headers (including byte trickles across many 250ms update intervals): explicit cap must hold.
- Continuous uploads: request duration can exceed idle; underlying socket reads renew with existing +250ms slack. A truly stalled socket body read times out. No promise of application CPU/storage wait deadlines.
- Empty and completed bodies: net/http's zero deadline must keep disconnect detection from timing out otherwise active long handlers.
- Keep-alive: caller sets idle wait, then next header absolute timeout; both remain capped after StateIdle.
- TLS: read handshake cap cannot be renewed; completed handshake transitions into fresh HTTP header cap. Write-side handshake deadline behavior is unchanged/out of this read-side defect.
- Chunked encoding/Expect 100-continue/early close: preserve existing generic read path; exercise actual requests.
- Explicit body deadlines retain historical SILO behavior (future can renew; past abort works); this patch does not promise a generic net.Conn behavior migration.
- Linux internode DriveOPTimeout caller uses default mode false; same read/write/zero/abort behavior. Grid raw upgrade still unwraps the same concrete type; TLS upgraded connections stay in legacy body mode. No data/format migration or stored-state rewriting.

## Validation matrix

1. Deterministic recording net.Conn tests: strict future cap, strict far-future deadline bounded by idle, no slack on cap, mode transition, default rolling behavior, SetDeadline/read direction separation, zero disable, past cancellation, explicit updates resetting throttle, expired future deadline, concurrent Read/SetReadDeadline (race).
2. Real TCP HTTP/1: initial slow header; byte trickle longer than header timeout; successful ordinary request; keep-alive idle and second slow header; continuous long body exceeding scaled idle; truly idle body; chunked body and Expect 100-continue; early body close followed by next request where supported; handler spending >idle after no body/after EOF without request context cancellation.
3. Run HTTP/1 body and header matrix through TLS (real tls.Client), plus stalled TLS ClientHello/handshake. Test existing ConnState hook chaining.
4. HTTP/2 over real TLS smoke and native timeout preservation; explicitly no cross-stream socket timeout introduced.
5. DeadlineConn existing tests and internal/http full suite under race, targeted grid roundtrips/disconnect; Linux compile for modified packages and default caller behavior fixture (Darwin does not exercise dial_linux at runtime).
6. One explicit >30s continuously progressing upload using production default idle, both cleartext and TLS HTTP/1 if feasible, to guard against an unintended total-30s cap. This may be opt-in to keep the routine suite fast, but run it for acceptance.
7. Scope tests, gofmt, git diff --check. If Linux runtime available without unrelated environment changes, run focused tests there; otherwise report compile vs runtime separately.

## Work and delivery

Estimate: 1–3 engineer days including design review and matrix; expected production diff tens of lines plus tests. Isolated `codex/` branch after agreement. Save raw Opus JSONL and stderr outside Git; keep prompt, exact plan SHA256, actual assistant model identity, effort, reviewer text, issue dispositions and consensus record here. No commit/push required for local review, and no merge/release/deploy is authorized in this phase.

Codex position: recommend option 4. Opus must independently verify the phase ordering, EOF/background behavior, keep-alive, TLS/H2 and shared-caller boundaries. Any blocking disagreement requires a revised plan and another review; a failed/limited/wrong-model response is not consensus.
