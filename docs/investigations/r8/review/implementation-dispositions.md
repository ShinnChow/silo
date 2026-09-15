# V1 implementation-review dispositions (carried into v2)

The real Opus implementation review returned REQUEST_CHANGES with one test/dependency blocker and no production-behavior defect. This is not recorded as implementation approval.

- B1 accepted: the test's direct x/net/http2 import would change the indirect annotation during tidy. Removed that import and use the Go1.27 standard-library HTTP/2 client with an explicit Protocols set containing only HTTP/2. Keep the server's HTTP/1-first ALPN order, and assert actual h2 negotiation and HTTP/2.0 response. No go.mod/go.sum change. Revalidation and dependency hygiene follow.
- N1 accepted: add repeated-renewal recording-connection test across three actual 300ms intervals, without resetting the cap.
- N2 noted: the existing concurrent test is a race probe; semantic assertions are supplied by separate deadline and HTTP tests, not inferred from that probe.
- N3 accepted for h2: give the fixture a 3s server keep-alive idle period while retaining 400ms ReadTimeout and isolating the native read timer. Small scaled streaming tests retain their prior margin, separately backed by 33s production-default runs.
- N4 retained: local conn shadowing is legal, no behavior concern; avoid unrelated cleanup.
- N5 retained: redundant deadline calls are low-cost and intentional; no performance redesign.
- N6 retained: explicit h2 exclusion documents the multiplexed boundary and is safe; zero deadlines remain the fundamental compatibility mechanism.
- N7/N8 accepted: release notes distinguish header/handshake-read tightening from unchanged h2 stream and TLS handshake-write limitations.
- N9 resolved with direct final macOS/Linux race logs, then recheck modified fixtures after this review's changes.
- N10 accepted: record explicit command and exit status for vet and final checks, not only empty stdout.

The new common-main.go binding is still unmodified and awaits complete-plan v2 agreement. V1 approval cannot cover it. V2 review must also verify B1's chosen no-new-dependency resolution.
