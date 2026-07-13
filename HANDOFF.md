# Handoff: go-github-server

## Current state

The repository implements a generator-backed HTTP server inverse for
`github.com/google/go-github/v89/github`.

- `cmd/gen-server` loads the upstream package with `go/packages`, walks its AST,
  resolves types, reads all `//meta:operation` annotations, and emits the
  generated service surface and route metadata.
- `zz_generated.go` contains 43 routed service interfaces, matching
  `Unimplemented<Service>Service` embeddings, the `Services` aggregate, and
  1,262 classified annotations.
- `coverage.json` records each annotation as clean, generated with an explicit
  override, or a client convenience alias of a canonical shared operation.
- `server.go` provides `New(Services, Authenticator) *http.ServeMux`, route
  matching, codecs, shared-operation dispatch, and authentication delegation.
- `server_test.go` drives the server through the real upstream client.

## Design decisions

- Preserve upstream service and method signatures so implementations reuse the
  normal `go-github` entity types.
- Embed generated unimplemented service structs to implement only the desired
  methods.
- Register one generated dispatcher on a standard-library `ServeMux`. The
  upstream route set contains wildcard/literal patterns that `ServeMux` refuses
  to register independently, so the dispatcher performs literal-first matching
  and sets `Request.PathValue`.
- Group identical HTTP operations. Select content-negotiated siblings through
  AST-extracted `Accept` metadata and otherwise fall through only when an
  implementation returns `ErrNotImplemented`.
- Keep explicit generated overrides for the small exceptional tail: WebSub
  forms, redirects and streams, binary uploads, entity-backed path fields,
  archive selectors, composite compare refs, projected JSON fields, formatted
  query values, and client convenience aliases.
- Delegate credentials based on documented GitHub token prefixes while passing
  the complete token unchanged to application authentication code.

## Proof

The closeout gates are:

```sh
go generate ./...
go run ./cmd/gen-server -check
go test ./...
go test -race ./...
go vet ./...
golangci-lint run ./...
```

The tests cover JSON, query, projected-body, path, entity-field, form, upload,
raw, redirect, composite-ref, shared-route, cross-service, status/header, and
PAT/installation-token behavior through `github.Client` and `httptest.Server`.

Consensus artifacts and the session worklog are under `ephemeral/`.
