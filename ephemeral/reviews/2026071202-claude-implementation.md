# Independent implementation review — go-github-server

Reviewer: Claude. Date: 2026-07-12. Read-only; no product code edited.
Evidence base: `server.go`, `cmd/gen-server/main.go`, `server_test.go`, `cmd/gen-server/main_test.go`,
`zz_generated.go`, `coverage.json`, and upstream `/Users/tyler/src/go-github` (`github.com/google/go-github/v89`).
Verified green: `go test ./...` passes and `go run ./cmd/gen-server -check` exits 0.
One headline finding is reproduced empirically (a throwaway external test, since removed).

---

## The exact prompt I was given

> # Independent implementation review prompt
>
> Here is my goal:
>
> Implement `github.com/tylergannon/go-github-server` as the complete server-side inverse of only the `github` package in `github.com/google/go-github/v89`. Use AST traversal to generate typed service interfaces and HTTP server stubs from upstream `//meta:operation METHOD /path` annotations. Preserve upstream service composition; provide a constructor accepting service implementations and returning a simple `*http.ServeMux`; decode route, query, and request data; call application implementations; encode status, headers, and response data. Reuse upstream entity types. Provide authentication middleware delegation for PATs (`ghp_`, `github_pat_`) and installation tokens (`ghs_`) parsed from supported Authorization header forms, leaving the complete credentials opaque to implementers and implementing no authentication policy, OAuth, password auth, or Basic auth. Prove behavior using `go test`, `stretchr/testify`, generated coverage, trivial service implementations, and real `go-github` client round trips. Do not work outside upstream `github` and do not add speculative layers.
>
> The first Claude design review is at `ephemeral/reviews/2026071201-claude-design.md`. The reconciled implementation deliberately made these choices:
>
> - Standard `go/ast` plus type-aware `go/packages`, no `dave/dst`.
> - 43 routed service surfaces: 42 shared service aliases plus the structurally distinct, client-exposed `MarketplaceService`.
> - Upstream method signatures on generated service interfaces, with generated `Unimplemented<Service>Service` embeddings so implementations can override a subset.
> - A single generated dispatcher registered on a returned `*http.ServeMux`, because the full GitHub route set contains patterns Go's ServeMux rejects as mutually ambiguous. Matching is deterministic and literal-first; adapters set `Request.PathValue`.
> - Exact method-path duplicates are grouped. `/hub` is one route group dispatched by `hub.mode`.
> - Prefix-only token-family classification based on current official GitHub token formats; delegates receive the untouched token.
> - `coverage.json` classifies all 1262 annotations as generated-clean or generated-with-override.
>
> Current proof is green:
>
> ```text
> go test ./...
> ok github.com/tylergannon/go-github-server
> ok github.com/tylergannon/go-github-server/cmd/gen-server
>
> go run ./cmd/gen-server -check
> (success)
> ```
>
> Review the complete current implementation and generated artifacts against the full goal. If you have a code review skill, use it; otherwise review directly. Prioritize demonstrable bugs, incorrect route matching or binding, request/response codec failures, authentication mistakes, false completeness claims, generated-contract problems, upstream AST inference errors, missing proof, over-engineering, and unrequested scope. Inspect actual source and run focused read-only checks as needed. Do not edit product code.
>
> Write the exact prompt you were given and all findings to:
> `/Users/tyler/src/go-github-server/ephemeral/reviews/2026071202-claude-implementation.md`
>
> Label every finding:
>
> - **critical:** must fix before proceeding.
> - **bug:** demonstrable incorrect behavior, broken contract, race, or regression.
> - **design:** architecture, boundary, scope, maintainability, or proof issue materially likely to cause problems.
> - **nit:** small cleanup that should not block progress.
>
> Use file and line references. Do not merely approve. Conclude with the exact proof still needed after fixes.

---

## Summary verdict

**Not approvable as complete.** The generator, path/query/JSON binding, response codec, and auth
delegation are well built and the clean-path round trip is genuinely proven. But the central routing
decision — "exact method-path duplicates are grouped, dispatch picks the group" — is only *half*
implemented. `serveOperationGroup` always invokes `ops[0]` (server.go:168) and special-cases only
`/hub`. There are **47 duplicate `(METHOD, path)` groups** in `coverage.json`, not one, and the
other 46 collapse to a single, alphabetically-chosen implementation. This makes a set of primary
GitHub endpoints — including `GetContents`, `GetReleaseAsset`, and
`UpdatePullRequestReviewEnforcement` — **unreachable**, while `coverage.json` reports them
`generated-clean`. The "1262 annotations, all classified, tests green" proof does not actually
demonstrate that each route reaches its intended implementation, so the completeness claim is
overstated.

---

## Findings

### 1. **critical** — Duplicate `(method, path)` groups collapse to the alphabetically-first method; primary endpoints become unreachable

`serveOperationGroup` picks `op := ops[0]` and disambiguates **only** `/hub` (server.go:168–181).
Every other collision group is served by whichever operation is first in the group. Group order is
the append order in `generatedOperations`, which is service-alphabetical then method-name
alphabetical (`scan` sorts methods by name, main.go:172; `render` iterates in that order,
main.go:380–401). So the reachable member of every collision group is **the alphabetically-first
method name**, and the server never inspects the `Accept` header that actually distinguishes these
upstream operations.

`coverage.json` contains **47** groups with more than one entry sharing an identical
`http_method`+`path`. The concrete damage where the *shadowed* method is the primary one:

- **`GET /repos/{owner}/{repo}/contents/{path}`** → group is `DownloadContents`,
  `DownloadContentsWithMeta`, `GetContents`. `ops[0] = DownloadContents`, so **`GetContents` is
  unreachable.** Reproduced empirically: a real `client.Repositories.GetContents(...)` call against
  `New(Services{Repositories: implWithGetContents}, nil)` returns **`501 Not Implemented`**, because
  the dispatcher calls `MethodByName("DownloadContents")` (server.go:237) which is unimplemented.
  (`GetContents` returns `(*RepositoryContent, []*RepositoryContent, *Response, error)`;
  `DownloadContents` returns `(io.ReadCloser, *Response, error)` — go-github/github/repos_contents.go:149,218.)
- **`GET /repos/{owner}/{repo}/releases/assets/{asset_id}`** → `DownloadReleaseAsset` shadows
  **`GetReleaseAsset`** (the JSON metadata endpoint). A `GetReleaseAsset` client call is dispatched to
  the `DownloadReleaseAsset` implementation.
- **`PATCH .../branches/{branch}/protection/required_pull_request_reviews`** →
  `DisableDismissalRestrictions` shadows **`UpdatePullRequestReviewEnforcement`** (the main update
  call).
- Raw/negotiated variants that become dead: `GetCommitSHA1`/`GetCommitRaw` (Accept: `sha`/`diff`/`patch`,
  go-github/github/repos_commits.go:191–224), `GetBlobRaw`, `PullRequests.GetRaw`,
  `GetCodeOfConduct` (shadowed by `Repositories.Get` on `GET /repos/{owner}/{repo}`), and all the
  `Delete*ReactionByID` / Teams `*ByID`/`*BySlug` pairs.

The design note says "exact method-path duplicates are grouped … Matching is deterministic and
literal-first." Grouping and literal-first ordering are implemented, but **intra-group disambiguation
is not** (beyond `/hub`). Deterministic-but-wrong is still wrong: for content-negotiated endpoints the
determinant is the `Request` `Accept` header, which the server ignores entirely.

Fix direction: within a collision group, dispatch on the discriminator that upstream uses — `Accept`
media type for the raw/JSON/diff variants, form field for `/hub`, and for the pure convenience
aliases (below) decide a single canonical impl and drop the rest from the routed set (don't leave a
dead interface method that silently 501s).

### 2. **critical** — False completeness: `coverage.json` marks shadowed operations `generated-clean`, and no test proves reachability

`coverage.json` classifies `GetContents`, `GetReleaseAsset`, `UpdatePullRequestReviewEnforcement`,
etc. as `generated-clean` (verified in the file), yet they cannot be reached at runtime (finding 1).
The completeness guard, `TestGeneratorClassifiesEveryAnnotatedOperation`
(cmd/gen-server/main_test.go:10–24), only asserts `len(coverage)==1262`, that every `Status` is one
of two strings, and that `Source` is non-empty. It never asserts that each `(method,path)` is
*independently dispatchable to its own method*. `TestCompleteGeneratedSurfaceConstructsAndDispatches`
(server_test.go:219) probes exactly one route and only checks it 501s. So the headline proof — "1262
classified, tests green" — demonstrates *classification coverage*, not *behavioral coverage*. The
design's own finding 8 ("a test that fails if any annotation is unclassified — otherwise the
generator will silently narrow scope and look complete") is satisfied only in letter: the scope is
silently narrowed at *dispatch* instead of at *classification*, which the guard does not see.

A collision group with two genuinely different behaviors should be classified
`generated-with-override` (or `needs-discriminator`), not `generated-clean`, and there must be a test
that every routed method is reachable.

### 3. **bug** — Cross-service duplicate routes make one service's implementation dead when both are provided

Several collisions are across *different* services for the *same* endpoint:

- `GET|PUT /orgs/{org}/actions/permissions` and `.../selected-actions` →
  `Actions.*` and `Organizations.*` (both real methods, both `*ActionsPermissions`,
  go-github/github/orgs_actions_permissions.go:19, actions_permissions_orgs.go:77).
- `POST|DELETE /admin/users/{username}/authorizations` → `Admin.*Impersonation` and
  `Authorizations.*Impersonation`.
- `GET /repos/{owner}/{repo}/issues/events` → `Activity.ListIssueEventsForRepository` and
  `Issues.ListRepositoryEvents`.

Because `New` takes a single `Services` struct that groups *all* services, the natural usage is to
populate several of them. When both colliding services are non-nil, the alphabetically-first service
wins (`Actions` < `Organizations`, `Admin` < `Authorizations`, `Activity` < `Issues`). If the author
put the real logic in the *shadowed* service (e.g. implemented `Organizations.GetActionsPermissions`
but left `Actions` as the embedded `Unimplemented…`), the request 501s even though a correct
implementation exists. This is the same trap as finding 1 but crosses service boundaries, so it is
easy to hit accidentally.

### 4. **bug** — Upload endpoints never bind the request body; the file argument stays nil

`operationBindings` only emits `bindingRequestBody` when `isReader(paramType)` is true, and
`isReader` matches the literal substring `io.Reader` (main.go:505, 531–533). But
`UploadReleaseAsset`'s file parameter is `*os.File`, not `io.Reader`
(go-github/github/repos_releases.go:428). So no body binding is generated; the `*os.File` argument is
passed to the implementation as its zero value (nil) and the uploaded bytes are silently dropped. The
op is reachable (`DownloadReleaseAsset`… no — for the assets *POST* group `UploadReleaseAsset` is
`ops[0]`), and is marked `generated-with-override "binary upload"`, but "override" here means
"produces a handler that cannot receive the body," not "handled." Even `NewUploadRequest`'s query
params (`opts *UploadOptions`) reach the impl, so this looks half-working while the payload is gone.

### 5. **bug** — `DownloadReleaseAsset` (the reachable member of its group) emits a 302 with no `Location`

`DownloadReleaseAsset` returns `(io.ReadCloser, string, error)` — no `*url.URL` and no `*github.Response`
(go-github/github/repos_releases.go:336). It is classified `ResponseKind == "url"`. In `writeResults`
the `"url"` branch takes `bodyValues[0].(*url.URL)` (server.go:404–405); here `bodyValues[0]` is the
`io.ReadCloser` (or the redirect `string`), the type assertion's `ok` is false, so **no `Location`
header is set** and the response is a bare `302`. (It is also the member that shadows the far more
common `GetReleaseAsset`, per finding 1, compounding the problem.)

### 6. **design** — Per-request linear scan over ~1200 route groups with a side-effecting matcher, and no proof that literal-first tie-breaking is unambiguous

`registerOperations` installs a single `/` handler that, on every request, iterates all route groups
and calls `route.match(r)` (server.go:92–100). `match` mutates the request via `r.SetPathValue`
*while probing* (server.go:136, 144), including on groups that ultimately fail. It happens to be safe
because the winning group overwrites `p0..pN` for the indices it reads, but relying on that is
fragile. More importantly, the ordering is `literalSegments` desc, then segment-count desc, then
stable (alphabetical) order (server.go:86–91). For groups with **equal** literal counts that overlap
(GitHub has many mid-path wildcards, e.g. `/orgs/{org}/actions/permissions/repositories/{repository_id}`
vs `/orgs/{org}/actions/permissions/selected-actions`), the tie-break is segment-count then arbitrary
input order. No test exercises the full set to prove a request cannot match the wrong group. The
single guard test (`TestCompleteGeneratedSurfaceConstructsAndDispatches`) probes one path. This is
the routing core; it needs a table-driven test that drives at least one request per registered group
and asserts the *intended* method fired.

### 7. **design** — `/hub` binding is hard-coded to the `Subscribe`/`Unsubscribe` signature and mis-classified `generated-clean`

`populateHubArgs` writes positional args `args[1..5]` assuming the exact
`(ctx, owner, repo, event, callback string, secret []byte)` shape (server.go:314–331). The upstream
methods build the form via a helper `createWebSubRequest` that calls `NewFormRequest`
(go-github/github/repos_hooks.go:257–299), so `analyzeBody` — which only looks for a direct
`NewFormRequest`/`NewRequest` call in the annotated method body — sees neither a form body nor a JSON
body and classifies `Subscribe`/`Unsubscribe` as `generated-clean` with empty bindings. It works only
because `serveOperationGroup`/`invokeOperation` special-case `op.Path == "/hub"`. The result is correct
and tested (`TestHubCollisionDispatchesByFormMode`), but the classification is misleading and the
hand-written positional reconstruction will break silently if the upstream signature changes. It
should be recorded as an override with an explicit reason.

### 8. **design** — Response status defaults are guessed by verb; `POST → 201` is wrong for several endpoints

When the impl returns no `*github.Response` (or one with status 0), `writeResults` defaults
`POST → 201 Created` (server.go:378). Several POSTs return 200, e.g. `POST /markdown`,
`POST /repos/{owner}/{repo}/merges` semantics vary, `POST .../dispatches` returns 204. go-github's
`Do` tolerates any 2xx for most calls so this rarely surfaces in the happy path, but the default is a
guess presented as a fact, and the design (finding 7 of the design review) called for verb defaults
*with per-op override from the OpenAPI status*. No status override table exists; every non-`*Response`
status is verb-guessed. Acceptable as a default only if documented as such and overridable.

### 9. **design** — `HEAD → GET` fallthrough is unrequested scope

`match` accepts `HEAD` requests against `GET` operations (server.go:120). Nothing in the goal asks for
HEAD support, go-github does not issue HEAD for these routes, and it adds a behavior (invoking a GET
impl, then relying on net/http to strip the body) that is untested. Minor, but it is exactly the kind
of speculative layer the prompt says to avoid.

### 10. **nit** — Dead sentinels and version floor

`server.go:524` (`var _ = time.Time{}`) and `main.go:566–567` (`var _ = token.NoPos`,
`var _ = strconv.IntSize`) are leftover import-anchors; remove them and the unused imports. Confirm
`go.mod`'s Go floor is the intended one and that `dave/dst`/jsonschema tool deps (flagged in the
design review) are actually gone from `go.mod`/`go.sum` now that the generator is `go/packages`-only.

---

## What is genuinely good (not mere approval)

- Type-aware loading (`go/packages` with `NeedTypes|NeedTypesInfo`, main.go:110) and
  `scalarCandidates` reading each param's declared basic type (main.go:516–529) correctly handle the
  `int64`/`int` path scalars the design review flagged.
- `parseScalar` honors `encoding.TextUnmarshaler` and pointer targets (server.go:431–473);
  `decodeQuery` recurses embedded option structs by `url` tag (server.go:485–493). The `ListOptions`
  round trip is proven (`page=2&per_page=50` reaches the impl, server_test.go:70–71).
- Auth delegation is correct and matches the goal: scheme parse only, prefix-family classification,
  untouched credential forwarded, unsupported forms rejected — proven for `ghp_`/`github_pat_`/`ghs_`
  and rejected for Basic/`gho_`/opaque (server_test.go:148–217).
- The clean webhook round trip (Create/List/Get/Edit/Delete) genuinely exercises real `github.Client`
  serialization and deserialization (server_test.go:54–125).

---

## Proof still needed after fixes

1. **Reachability of every routed method.** A generated table-driven test that issues one request per
   *registered* operation (not per group) and asserts the *intended* method executed — this is the
   test that would have caught findings 1 and 3. Must fail if any routed interface method is
   unreachable.
2. **Collision disambiguation.** Round-trip tests through the real client for at least: `GetContents`
   vs `DownloadContents` (Accept-negotiated), `GetReleaseAsset` vs `DownloadReleaseAsset`,
   `GetCommit` vs `GetCommitSHA1` vs `GetCommitRaw`, `PullRequests.Get` vs `GetRaw`,
   `UpdatePullRequestReviewEnforcement` vs `DisableDismissalRestrictions`, and one cross-service pair
   (`Actions` vs `Organizations` permissions) proving both can be implemented and each is reached.
3. **Coverage semantics.** Reclassify collision members that differ in behavior as
   override/needs-discriminator, and add a test asserting `generated-clean` implies "independently
   dispatchable to its own method."
4. **Upload body.** A round trip proving `UploadReleaseAsset` (and `UploadReleaseAssetFromRelease`)
   deliver the uploaded bytes to the impl (finding 4), or an explicit recorded skip if uploads are
   out of first-milestone scope.
5. **Redirect endpoints.** A test proving `GetArchiveLink`-style `url` responses set `Location`, and
   fixing/So classifying `DownloadReleaseAsset` (finding 5).
6. **Raw/`Accept` endpoints.** Prove at least one `raw` endpoint returns the body with the right
   status via the real client, and that `Accept`-differentiated siblings do not shadow each other.
7. **Routing ambiguity.** A test over the full generated route set asserting no request matches two
   groups whose intended methods differ (finding 6), or a documented proof that literal-first +
   segment-count ordering is total for the GitHub route set.
8. **Status defaults.** Either a per-op status override sourced from upstream OpenAPI, or explicit
   documentation + tests pinning the verb-default behavior for the endpoints where 201≠actual
   (finding 8).

Until items 1–3 exist and pass, the "complete server-side inverse … all 1262 classified" claim is
not substantiated: the artifact classifies 1262 operations but only demonstrably *serves* the subset
that survives alphabetical shadowing.
