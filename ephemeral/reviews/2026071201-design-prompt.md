# Independent design review prompt

Here is my goal:

Build `github.com/tylergannon/go-github-server` as a complete, generator-backed server stub for only the `github` package of `github.com/google/go-github/v89`. Use AST traversal in the spirit of go-github's accessor generators (or dave/dst techniques from `~/src/go-gen-jsonschema`) to invert the client package into server method stubs. Preserve go-github's 42 service boundaries. Generate typed interfaces implemented by application code and a constructor/registration surface returning a simple Go 1.22+ `http.ServeMux`/`http.Handler`. Generated transport code must decode paths, query options, and bodies, invoke the implementation, and encode responses. Entity structs may be aliases/re-exports of upstream go-github. Add an authentication middleware interface that recognizes supported GitHub token header forms, distinguishes personal access tokens from GitHub App installation tokens, treats token contents as opaque, and delegates to methods such as `AuthenticateWithPAT` and `AuthenticateWithInstallationToken`; do not implement authentication, OAuth, password auth, or product policy. Tests must construct a trivial implementation, use real HTTP/client calls, prove request deserialization and response serialization, and prove PAT and app-token delegation. Stay basic: do not invent unrelated abstractions or work outside upstream `github`.

Observed source evidence:

- Upstream checkout: `/Users/tyler/src/go-github` at `c0e23235476f55cb3f358ef3f18d886e6f8f872e`, module `github.com/google/go-github/v89`.
- 204 non-test Go files, 1,262 `//meta:operation METHOD /path` annotations, and 42 `type XService service` declarations.
- Methods use `context.Context`, path scalars, `url:`-tagged options passed to `addOptions`, bodies passed to `NewRequest`, and response targets passed to `Client.Do`.
- Some methods have multiple route annotations; some operations share the same method/path (for example Subscribe and Unsubscribe both POST `/hub` and differ by form body); request wire bodies may be unexported projections (for example `CreateHook` converts `*Hook` to `*createHookRequest`); streaming, redirect, archive, upload, raw, and form endpoints exist.
- Upstream accessor generators use standard `go/ast`, `go/parser`, and templates. `go-gen-jsonschema` uses `dave/dst` plus loaders/wrappers, but generated server code does not intrinsically require comment-preserving rewrites.
- Current repo has only `go.mod`, `go.sum`, `lefthook.yml`, `HANDOFF.md`, and ephemeral artifacts; there is no implementation yet.

Review a minimal concrete design before implementation. Look for correctness problems, missing constraints, unnecessary layers, wrong ownership boundaries, leaky abstractions, data-flow mistakes, failure modes, and proof gaps. In particular, decide what should be generated versus handwritten; what exact public service/constructor/response/auth API is simplest; how route annotations should map to client signatures and wire expressions; how broad completeness can be measured honestly; and how exceptional or colliding endpoints should be represented without silently narrowing the requested scope.

Inspect the referenced repositories as useful. Do not edit product code. Write the exact prompt you were given and your findings to:
`/Users/tyler/src/go-github-server/ephemeral/reviews/2026071201-claude-design.md`

Label every finding with one of:

- **critical:** must fix before proceeding.
- **bug:** demonstrable incorrect behavior, broken contract, race, or regression.
- **design:** architecture, boundary, scope, maintainability, or proof issue materially likely to cause problems.
- **nit:** small cleanup that should not block progress.

Conclude with a recommended minimal design and proof plan. Do not merely approve.
