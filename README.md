# go-github-server

`go-github-server` generates the server-side inverse of
[`google/go-github`](https://github.com/google/go-github). The upstream
`github` package describes requests as typed service methods annotated with
`//meta:operation METHOD /path`; this module turns those descriptions into
service interfaces and a GitHub-compatible `http.ServeMux`.

## Use

Implement one or more generated service interfaces. Embed the corresponding
`Unimplemented<Service>Service` to implement only the methods your server
supports:

```go
type repositories struct {
	githubserver.UnimplementedRepositoriesService
}

func (repositories) GetHook(
	ctx context.Context,
	owner, repo string,
	id int64,
) (*github.Hook, *github.Response, error) {
	return &github.Hook{ID: &id}, nil, nil
}

mux := githubserver.New(githubserver.Services{
	Repositories: repositories{},
}, authenticator)
http.ListenAndServe(":8080", mux)
```

The generated adapters decode path parameters, `url`-tagged query option
structs, JSON request bodies, form bodies, and raw request bodies. They call the
typed service method and encode its body, status, and headers. A returned
`*github.Response` can provide an explicit status and headers. Without one, the
adapter uses 200 for a response body and 204 for an empty response; methods with
endpoint-specific success codes should return them explicitly.

Both regular GitHub API paths and the `/api/v3` GitHub Enterprise prefix are
accepted.

## Authentication

`Authenticator` performs no authentication itself. The adapter parses either
`Authorization: Bearer TOKEN` or `Authorization: token TOKEN`, classifies the
documented GitHub token prefix, and passes the complete untouched credential to
the application:

- `ghp_` and `github_pat_` call `AuthenticateWithPAT`.
- `ghs_` calls `AuthenticateWithInstallationToken`.

OAuth tokens, GitHub App user tokens, refresh tokens, Basic authentication,
password authentication, and unknown token formats are deliberately rejected.
Requests without an Authorization header remain available for public API
implementations.

## Generation and coverage

Run `go generate ./...` after upgrading `go-github`. The generator uses
`go/packages` with full type information plus standard `go/ast` traversal. It
emits:

- `zz_generated.go`: all routed upstream services, interfaces, unimplemented
  embeddings, and operation metadata.
- `coverage.json`: one classification for every upstream route annotation.

`go run ./cmd/gen-server -check` fails when either generated artifact is stale.
The generator tests also pin the upstream inventory so a dependency upgrade
cannot silently add or remove routes.
