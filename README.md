# go-github-server

`go-github-server` is the server-side inverse of
[`google/go-github`](https://github.com/google/go-github). Implement the same
typed service methods exposed by the Go client and serve them through a
GitHub-compatible HTTP API.

The project generates its server surface directly from the route annotations,
Go types, and method documentation in `go-github`. The current generated API is
based on `go-github` v89 and covers 1,262 annotated REST routes across 1,229
service methods.

## Why use it?

- Implement GitHub-compatible APIs with typed Go methods instead of handwritten
  HTTP handlers.
- Use the real `go-github` client as an integration client for tests and local
  services.
- Implement only the endpoints you need by embedding generated unimplemented
  service types.
- Decode path parameters, query options, JSON, forms, uploads, and raw bodies
  without endpoint-specific transport code.
- Encode JSON, streams, downloads, redirects, status-only responses, and
  response headers from the generated return signatures.
- Delegate personal access token and GitHub App installation-token validation
  to application code.
- Serve GitHub.com paths and the GitHub Enterprise `/api/v3` and
  `/api/uploads` prefixes.
- Read upstream endpoint prose, effective HTTP routes, deprecation notices, and
  GitHub documentation links directly in Go documentation.

The constructor returns the requested `*http.ServeMux`. Internally, a generated
literal-first dispatcher handles route pairs that Go's standard ServeMux cannot
register together because their wildcard and literal segments are mutually
ambiguous. No third-party router is required.

## Install

```sh
go get github.com/tylergannon/go-github-server@latest
```

The module currently requires Go 1.26 or newer.

## Quick start

Implement a generated service interface. Embed the matching
`Unimplemented<Service>Service` to leave every other endpoint unimplemented:

```go
package main

import (
	"context"
	"log"
	"net/http"

	"github.com/google/go-github/v89/github"
	githubserver "github.com/tylergannon/go-github-server"
)

type repositories struct {
	githubserver.UnimplementedRepositoriesService
}

func (repositories) GetHook(
	ctx context.Context,
	owner string,
	repo string,
	id int64,
) (*github.Hook, *github.Response, error) {
	return &github.Hook{ID: &id}, nil, nil
}

func main() {
	mux := githubserver.New(githubserver.Services{
		Repositories: repositories{},
	}, nil)

	log.Fatal(http.ListenAndServe(":8080", mux))
}
```

Passing a nil authenticator disables credential validation. See
[Authentication](#authentication) before exposing a server outside a trusted
test or development environment.

## Find an endpoint

If you know the service and method, use `go doc`:

```sh
go doc github.com/tylergannon/go-github-server.RepositoriesService.CreateHook
```

The result includes the upstream prose, exact Go signature, effective HTTP
route, and official GitHub API documentation URL.

If you know the HTTP operation, query the generated coverage inventory:

```sh
jq -r '.[] | select(.http_method == "POST" and .path == "/repos/{owner}/{repo}/hooks") | "\(.service)Service.\(.method)"' coverage.json
```

Or search the generated source by prose, method name, or route:

```sh
rg -n -B8 -A3 'CreateHook|HTTP: POST /repos/\{owner\}/\{repo\}/hooks' zz_generated.go
```

For a model-oriented setup and lookup guide, start with
[`llms.txt`](./llms.txt).

## Responses and errors

Service methods return the same typed values as their `go-github` client
counterparts. The adapter converts those values to the appropriate HTTP
response.

- A returned `*github.Response` can set an explicit status and headers through
  its underlying `*http.Response`.
- Without an explicit response, a body defaults to status 200 and an empty
  result defaults to status 204.
- A returned `*github.ErrorResponse` preserves the status and headers from its
  underlying `*http.Response` and is encoded as a GitHub JSON error payload.
- Ordinary implementation errors become status 500.
- An unhandled `githubserver.ErrNotImplemented` becomes status 501.
- Shared routes can fall through an embedded unimplemented method to another
  registered service implementation.

## Unimplemented-method telemetry

Set the callback on an embedded `Unimplemented<Service>Service` to observe calls
to methods the service does not implement:

```go
type repositories struct {
	githubserver.UnimplementedRepositoriesService
}

service := repositories{
	UnimplementedRepositoriesService: githubserver.UnimplementedRepositoriesService{
		Callback: func(ctx context.Context, call githubserver.UnimplementedCall) {
			log.Printf("unimplemented GitHub method: %s.%s at %s",
				call.Service, call.Method, call.CalledAt)
		},
	},
}
```

The callback runs immediately before the method returns
`ErrNotImplemented`. Calls may happen concurrently, so the callback must be
concurrency-safe and must not panic. On shared HTTP routes, the server may try
more than one candidate method; the callback reports each unimplemented method
that is actually tried, even if a later candidate handles the request.

## Unmatched-route telemetry

Use `WithNotFoundCallback` to observe requests that do not match any registered
generated operation:

```go
mux := githubserver.New(services, authenticator,
	githubserver.WithNotFoundCallback(func(r *http.Request) {
		log.Printf("unsupported GitHub operation: %s %s", r.Method, r.URL.RequestURI())
	}),
)
```

The callback receives the original request, including GitHub Enterprise
prefixes such as `/api/v3` and `/api/uploads`. It runs immediately before the
server writes its standard 404 response. Matched operations that return a
resource-level 404 do not invoke it. Because HTTP requests may run concurrently,
the callback must be safe for concurrent use and must treat the request as
read-only.

Endpoints with non-default success codes should return an explicit
`*github.Response`:

```go
return hook, &github.Response{Response: &http.Response{
	StatusCode: http.StatusCreated,
	Header:     make(http.Header),
}}, nil
```

Return a `*github.ErrorResponse` to reproduce an expected GitHub API failure:

```go
return nil, nil, &github.ErrorResponse{
	Response: &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{"X-GitHub-Request-Id": {"request-123"}},
	},
	Message:          "Not Found",
	DocumentationURL: "https://docs.github.com/rest",
}
```

## Authentication

`Authenticator` delegates credential validation to the application:

```go
type Authenticator interface {
	AuthenticateWithPAT(context.Context, string) (context.Context, error)
	AuthenticateWithInstallationToken(context.Context, string) (context.Context, error)
}
```

The adapter accepts both `Authorization: Bearer TOKEN` and
`Authorization: token TOKEN` and passes the complete credential untouched:

- `ghp_` and `github_pat_` call `AuthenticateWithPAT`.
- `ghs_` calls `AuthenticateWithInstallationToken`.

App JWTs are not opaque-token middleware credentials. For GitHub's installation
token issuance operation, the generated server methods expose the credential
from the required `Authorization: Bearer <JWT>` header as an `appJWT` argument:

```go
func (apps) CreateInstallationToken(
	ctx context.Context,
	appJWT string,
	installationID int64,
	body *github.InstallationTokenOptions,
) (*github.InstallationToken, *github.Response, error) {
	// Verify appJWT, authorize installationID and body restrictions, then issue
	// the installation token.
}
```

`CreateInstallationTokenListRepos` receives the same header and path arguments
with its `*github.InstallationTokenListRepoOptions` body. These endpoint
implementations are responsible for validating the App JWT signature and claims,
confirming that the installation belongs to that App, enforcing requested scope
narrowing, and issuing the installation token. The opaque-token `Authenticator`
is not called for this operation.

On routes handled by opaque-token middleware, unsupported authorization schemes
and token types receive status 401. Requests without an Authorization header are
allowed even when an authenticator is configured, so the application remains
responsible for endpoint-specific authorization policy. A context returned by
the authenticator is installed on the request before invoking the service
method.

## Test through the real client

The intended integration-test shape is:

1. Start an `httptest.Server` with `githubserver.New`.
2. Point a real `github.Client` at the test server.
3. Call the normal client service method.
4. Assert that the typed server implementation received the decoded values and
   returned the response expected by the client.

See [`server_test.go`](./server_test.go) for round trips covering JSON, query
options, uploads, raw formats, redirects, aliases, authentication, and route
collisions.

## Generation and coverage

The generator uses `go/packages` with full type information and standard
`go/ast` traversal. It emits:

- [`zz_generated.go`](./zz_generated.go): service interfaces, unimplemented
  embeddings, method documentation, and operation metadata.
- [`coverage.json`](./coverage.json): one classification for every upstream
  route annotation.

After upgrading `go-github`, regenerate and verify the inventory:

```sh
go generate ./...
go run ./cmd/gen-server -check
go test ./...
```

The generator tests pin the upstream inventory so dependency upgrades cannot
silently add or remove endpoints.

## Attribution

This project is built from the public API surface and route metadata of
[`google/go-github`](https://github.com/google/go-github). Generated service
interfaces and method documentation include material derived from that project:

> Copyright (c) 2013 The go-github AUTHORS. All rights reserved.

`google/go-github` is distributed under the BSD 3-Clause License. Its copyright
notice, license conditions, and disclaimer are reproduced in
[`THIRD_PARTY_NOTICES.md`](./THIRD_PARTY_NOTICES.md).

GitHub is a trademark of GitHub, Inc. This project is not affiliated with or
endorsed by GitHub, Inc. or Google LLC.

## License

Original work in this repository is released under the
[Zero-Clause BSD License](./LICENSE), SPDX identifier `0BSD`. It permits use,
copying, modification, and distribution for any purpose, with or without fee.

Material derived from `google/go-github` remains subject to its BSD 3-Clause
License as described in [`THIRD_PARTY_NOTICES.md`](./THIRD_PARTY_NOTICES.md).
