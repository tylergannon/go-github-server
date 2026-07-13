# Issue 3 typed GitHub HTTP errors

- Started: 2026-07-13 10:25 CST
- Goal: solve GitHub issue 3, preserving typed GitHub HTTP errors returned by service implementations.
- Constraint: work in a new worktree; stop and grill the user if material implementation questions remain unresolved by the issue or repository.
- Worktree: `/Users/tyler/src/go-github-server/.worktrees/issue-3-typed-errors`
- Branch: `codex/issue-3-typed-errors`
- Base: `origin/main` at `f5f2149`

skill_use: session-worklog source=pagerguild/core-tools -> record commands, decisions, implementation proof, and closeout state for this non-trivial repository change.
doc_lookup: GitHub issue 3 -> acceptance contract requires selectable status, headers, and GitHub JSON payload; real github.Client decoding; ordinary errors remain 500; ErrNotImplemented behavior remains unchanged; tests cover 401, 404, 409, and 422.

## Activity

- Refreshed `origin` with `git fetch origin --prune`; `origin/main` remains `f5f2149`.
- Read issue 3 via `gh issue view 3 --json ...`; no comments or unresolved choices were present.
- Created the task worktree and branch from current `origin/main`.
- Read `HANDOFF.md`, `server.go`, the relevant tests, and the upstream
  `go-github/v89` definitions for `github.ErrorResponse` and `CheckResponse`.
- Added a real-`github.Client` table test for 401, 404, 409, and 422 response
  round trips, including status, `X-GitHub-Request-Id`, message, structured
  errors, and documentation URL.
- Confirmed the new typed-error test failed against the old implementation:
  every case returned status 500 and lost the typed payload and header.
- Implemented typed error serialization and added explicit regression coverage
  that an ordinary service error remains status 500.
- Documented the typed error-response contract and an example in `README.md`.
- Built and ran a standalone proof server from `/tmp/go-github-server-issue-3-proof`
  against the worktree module, then exercised the real HTTP route with `curl`.
- The first proof loop used zsh's read-only `status` variable and failed before
  making requests; renamed the loop variable to `code` and reran successfully.

## Decisions

decision: Proceed without grilling because issue 3 specifies the externally observable contract and explicitly allows recognizing `*github.ErrorResponse`; implementation details will be derived from the existing adapter and go-github API.
decision: Recognize `*github.ErrorResponse` with `errors.As` after the existing `ErrNotImplemented` and request-error branches, preserving fallthrough/501 and request-validation behavior while also supporting wrapped typed errors.
decision: Treat a typed error as transport-ready only when its underlying `*http.Response` has a valid nonzero HTTP status; otherwise preserve the ordinary status-500 fallback.
decision: Marshal the GitHub payload before writing headers, clone all supplied response headers, and default `Content-Type` to `application/json` only when the service did not select one.

doc_lookup: `github.com/google/go-github/v89/github.ErrorResponse` -> its `Response` is excluded from JSON while message, errors, block, and documentation URL form the expected GitHub payload.
doc_lookup: `github.CheckResponse` -> non-success bodies are decoded into `*github.ErrorResponse`, so real-client proof validates wire compatibility rather than only handler output.

## Proof and closeout

- Targeted red test: failed as expected because typed errors became HTTP 500.
- Targeted green test: `go test ./... -run 'Test(ServiceErrorResponseRoundTripsThroughGoGitHubClient|UnexpectedServiceErrorRemainsInternalServerError|CompleteGeneratedSurfaceConstructsAndDispatches)$'` -> pass.
- `git diff --check` -> pass.
- Runtime proof: standalone `githubserver.New` process plus HTTP requests to
  `/repos/octo/demo/hooks/{401,404,409,422}` -> each response used the requested
  status, `Content-Type: application/json`, `X-GitHub-Request-Id: proof-<code>`,
  and a JSON payload containing message, structured error, and documentation URL.
- Full repository gates -> pass:
  - `go generate ./...`
  - `go run ./cmd/gen-server -check`
  - `go test ./...`
  - `go test -race ./...`
  - `go vet ./...`
  - `golangci-lint run ./...` (`0 issues`)
- Proved PR head: `92e7e12bba3772470c294af0442dd08769fb6bbf`
  (`fix: preserve typed GitHub errors`).
- Pull request: https://github.com/tylergannon/go-github-server/pull/5
- Runtime proof and all hygiene checks are included in the PR body.
- GitHub reported no configured status checks for the exact PR head.
- Squash merge completed at 2026-07-13 10:30 CST as
  `45fabca43d6cf4d1b02b04bf1fda7d77c5ea294d` on `origin/main`.
- `gh pr merge --auto --squash --delete-branch` completed the remote merge but
  could not perform local cleanup because `main` belongs to the root worktree;
  the remote feature branch was then deleted explicitly.
- Remaining debt: none identified.
