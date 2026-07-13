# Independent post-fix review prompt

Here is my goal:

Implement `github.com/tylergannon/go-github-server` as the complete AST-generated server-side inverse of the routed `github` package in `github.com/google/go-github/v89`: typed service interfaces preserving upstream grouping, a constructor returning `*http.ServeMux`, generated path/query/body codecs and response codecs, reuse of upstream entity types, opaque PAT and installation-token delegation, complete route classification, and real `go-github`/testify round-trip proof. Stay within `github`; do not invent unrelated layers.

This is review round two after the findings in `ephemeral/reviews/2026071202-claude-implementation.md`. Review the complete current implementation, not just the listed fixes. The prior critical/bug findings were addressed as follows:

- Shared HTTP operations are still grouped, but candidates now order by upstream AST-extracted `Accept` media types and direct transport ownership, then fall through only when the candidate returns `ErrNotImplemented`. This supports content negotiation and implementations provided through cross-service aliases.
- `coverage.json` now marks all shared operations `generated-with-override`; six client convenience annotations whose signatures cannot reconstruct a shared canonical path are `generated-alias` and are omitted as independent route candidates.
- Real-client regressions cover `GetContents` plus `DownloadContents`, release asset JSON vs octet-stream redirect selection, JSON/diff/patch/SHA commit selection, and cross-service Actions/Organizations fallback.
- Binary uploads bind `*os.File` through a temporary spool and `io.Reader` directly; a real `UploadReleaseAsset` test proves bytes arrive.
- Redirect-or-download methods now encode either streamed content or `Location` correctly.
- Entity-derived path parameters such as `Repository.ID` and secret names are reconstructed using generated field bindings; a real-client repository-ID test proves this path.
- `/hub` is explicitly classified as a form-discriminated override and remains round-trip tested.
- The matcher no longer mutates `PathValue` while probing, no longer adds unrequested HEAD behavior, and a full generated representative-path test proves each unique pattern selects itself under literal-first ordering.
- Dead import anchors and lint findings were removed.

Current proof:

```text
go generate ./...
go run ./cmd/gen-server -check
go test ./...
go test -race ./...
go vet ./...
golangci-lint run ./...
```

All pass; golangci-lint reports `0 issues`.

Review against the full original goal and the prior review. Look especially for remaining critical, bug, or design findings involving shared-operation dispatch, route/path binding, projected bodies, uploads, raw/redirect responses, coverage truthfulness, authentication, or proof scope. Inspect and run focused read-only checks. Do not edit product code.

Write the exact prompt and all findings to:
`/Users/tyler/src/go-github-server/ephemeral/reviews/2026071203-claude-fix-review.md`

Use labels:

- **critical:** must fix before proceeding.
- **bug:** demonstrable incorrect behavior, broken contract, race, or regression.
- **design:** architecture, boundary, scope, maintainability, or proof issue materially likely to cause problems.
- **nit:** small cleanup that should not block progress.

Use file/line references and conclude with proof still needed. Do not merely approve.
