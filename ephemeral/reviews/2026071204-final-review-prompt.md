# Final independent implementation review prompt

Goal: finish `github.com/tylergannon/go-github-server` as the complete, basic AST-generated server inverse of the routed `github.com/google/go-github/v89/github` surface: typed upstream-shaped service interfaces, a constructor returning `*http.ServeMux`, generated path/query/body/response adapters, upstream entity reuse, opaque PAT/installation-token delegation, complete route classification, and real-client/testify proof. Only upstream `github` is in scope.

This is the next consensus round after:

- `ephemeral/reviews/2026071202-claude-implementation.md`
- `ephemeral/reviews/2026071203-claude-fix-review.md`

The latest review's critical/bug findings were addressed:

- Any public method whose first result is `*url.URL` is now classified `url` from its type signature; the URL response encoder accepts both `*url.URL` and string redirect results.
- Real-client tests prove `DownloadArtifact`, `GetWorkflowRunLogs`, `MigrationArchiveURL`, and both archive format/ref reconstruction and redirect encoding.
- `DownloadArtifact`'s client-only redirect budget is no longer bound to `{archive_format}`; its actual SDK-supported route has literal `/zip`.
- `GetArchiveLink` uses generated constant archive-format and option-field ref bindings; `CompareCommits` and its raw sibling split `{basehead}` into base/head.
- AST analysis of request composite/map literals now records one-field wire projections (e.g. `selected_repository_ids`) and field-decodes them into the public method parameter; real-client proof covers it.
- Entity path-field bindings no longer suppress JSON/query decoding of the same argument.
- Single-operation uploads stream directly to their temporary spool; body replay occurs only for shared-operation fallback.
- Status fallback is now explicitly documented as 200 with body / 204 empty; endpoint-specific statuses come from returned `*github.Response`.
- `/hub`'s special signature is generation-validated so upstream drift fails closed.
- Unused jsonschema/dst tools and transitive dependencies were removed; only Lefthook's goimports and modernize tools remain.

Current proof all passes:

```text
go generate ./...
go run ./cmd/gen-server -check
go test ./...
go test -race ./...
go vet ./...
golangci-lint run ./...  # 0 issues
```

Review the complete current implementation against the full goal, not merely these fixes. Inspect actual code and run focused read-only probes. Look for remaining critical, bug, or design findings in route inference/binding, body projection, response classification, shared-operation dispatch, authentication, coverage truthfulness, generated API usability, or missing behavioral proof. Do not edit product code.

Write the exact prompt and findings to:
`/Users/tyler/src/go-github-server/ephemeral/reviews/2026071204-claude-final.md`

Labels:

- **critical:** must fix before proceeding.
- **bug:** demonstrable incorrect behavior, broken contract, race, or regression.
- **design:** architecture, boundary, scope, maintainability, or proof issue materially likely to cause problems.
- **nit:** small cleanup that should not block progress.

Use file/line references, do not merely approve, and conclude with any proof still needed.
