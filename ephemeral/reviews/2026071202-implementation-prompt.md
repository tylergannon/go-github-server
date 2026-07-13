# Independent implementation review prompt

Here is my goal:

Implement `github.com/tylergannon/go-github-server` as the complete server-side inverse of only the `github` package in `github.com/google/go-github/v89`. Use AST traversal to generate typed service interfaces and HTTP server stubs from upstream `//meta:operation METHOD /path` annotations. Preserve upstream service composition; provide a constructor accepting service implementations and returning a simple `*http.ServeMux`; decode route, query, and request data; call application implementations; encode status, headers, and response data. Reuse upstream entity types. Provide authentication middleware delegation for PATs (`ghp_`, `github_pat_`) and installation tokens (`ghs_`) parsed from supported Authorization header forms, leaving the complete credentials opaque to implementers and implementing no authentication policy, OAuth, password auth, or Basic auth. Prove behavior using `go test`, `stretchr/testify`, generated coverage, trivial service implementations, and real `go-github` client round trips. Do not work outside upstream `github` and do not add speculative layers.

The first Claude design review is at `ephemeral/reviews/2026071201-claude-design.md`. The reconciled implementation deliberately made these choices:

- Standard `go/ast` plus type-aware `go/packages`, no `dave/dst`.
- 43 routed service surfaces: 42 shared service aliases plus the structurally distinct, client-exposed `MarketplaceService`.
- Upstream method signatures on generated service interfaces, with generated `Unimplemented<Service>Service` embeddings so implementations can override a subset.
- A single generated dispatcher registered on a returned `*http.ServeMux`, because the full GitHub route set contains patterns Go's ServeMux rejects as mutually ambiguous. Matching is deterministic and literal-first; adapters set `Request.PathValue`.
- Exact method-path duplicates are grouped. `/hub` is one route group dispatched by `hub.mode`.
- Prefix-only token-family classification based on current official GitHub token formats; delegates receive the untouched token.
- `coverage.json` classifies all 1262 annotations as generated-clean or generated-with-override.

Current proof is green:

```text
go test ./...
ok github.com/tylergannon/go-github-server
ok github.com/tylergannon/go-github-server/cmd/gen-server

go run ./cmd/gen-server -check
(success)
```

Review the complete current implementation and generated artifacts against the full goal. If you have a code review skill, use it; otherwise review directly. Prioritize demonstrable bugs, incorrect route matching or binding, request/response codec failures, authentication mistakes, false completeness claims, generated-contract problems, upstream AST inference errors, missing proof, over-engineering, and unrequested scope. Inspect actual source and run focused read-only checks as needed. Do not edit product code.

Write the exact prompt you were given and all findings to:
`/Users/tyler/src/go-github-server/ephemeral/reviews/2026071202-claude-implementation.md`

Label every finding:

- **critical:** must fix before proceeding.
- **bug:** demonstrable incorrect behavior, broken contract, race, or regression.
- **design:** architecture, boundary, scope, maintainability, or proof issue materially likely to cause problems.
- **nit:** small cleanup that should not block progress.

Use file and line references. Do not merely approve. Conclude with the exact proof still needed after fixes.
