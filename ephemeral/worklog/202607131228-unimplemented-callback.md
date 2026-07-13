# Unimplemented service callback

goal: Add an optional callback to every generated `Unimplemented<Service>Service`, then merge and tag a new release.
worktree: `/Users/tyler/src/go-github-server/.worktrees/unimplemented-callback`
branch: `codex/unimplemented-callback`
base: `origin/main` at `7ec969b`

decision: Keep partial service implementation based on embedding the generated unimplemented service.
decision: Report a structured event containing service, method, and call time, and pass the method context separately so callback consumers can use request-scoped values.
decision: Callback invocation means an unimplemented method was called; shared-route candidate probing may therefore report a call even if a later candidate handles the HTTP request.
decision: Callback implementations must be concurrency-safe and should not panic.

skill_use: session-worklog source=pagerguild/core-tools -> preserve implementation, proof, PR, merge, and release state.
skill_use: proof-of-work source=pagerguild/core-tools -> prove the callback through a running HTTP server and real go-github client request.
skill_use: ship source=pagerguild/core-tools -> commit, push, and publish the PR before merge.

proof: Started a standalone HTTP server using `githubserver.New` and an embedded `UnimplementedRepositoriesService`, then requested `GET /repos/octo/demo/hooks/41` with curl.
proof_result: Callback logged `service=Repositories method=GetHook called_at_set=true`; HTTP response was `501 Not Implemented` with `github service method is not implemented`.
checks: `go run ./cmd/gen-server -check`, `go test -race ./...`, `golangci-lint run ./...`, and `git diff --check` passed.
