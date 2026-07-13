# Independent design review — go-github-server

Reviewer: Claude. Date: 2026-07-12. Read-only; no product code edited.
Evidence base: `/Users/tyler/src/go-github` @ `c0e23235476f55cb3f358ef3f18d886e6f8f872e`
(module `github.com/google/go-github/v89`), `HANDOFF.md`, `/Users/tyler/src/go-gen-jsonschema`,
and empirical Go/ServeMux checks. Verified: 42 `type XService service` decls, 1262
`//meta:operation` annotations, base `type service struct` at `github/github.go:253`.

---

## The exact prompt I was given

> # Independent design review prompt
>
> Here is my goal:
>
> Build `github.com/tylergannon/go-github-server` as a complete, generator-backed server stub for only the `github` package of `github.com/google/go-github/v89`. Use AST traversal in the spirit of go-github's accessor generators (or dave/dst techniques from `~/src/go-gen-jsonschema`) to invert the client package into server method stubs. Preserve go-github's 42 service boundaries. Generate typed interfaces implemented by application code and a constructor/registration surface returning a simple Go 1.22+ `http.ServeMux`/`http.Handler`. Generated transport code must decode paths, query options, and bodies, invoke the implementation, and encode responses. Entity structs may be aliases/re-exports of upstream go-github. Add an authentication middleware interface that recognizes supported GitHub token header forms, distinguishes personal access tokens from GitHub App installation tokens, treats token contents as opaque, and delegates to methods such as `AuthenticateWithPAT` and `AuthenticateWithInstallationToken`; do not implement authentication, OAuth, password auth, or product policy. Tests must construct a trivial implementation, use real HTTP/client calls, prove request deserialization and response serialization, and prove PAT and app-token delegation. Stay basic: do not invent unrelated abstractions or work outside upstream `github`.
>
> Observed source evidence:
>
> - Upstream checkout: `/Users/tyler/src/go-github` at `c0e23235476f55cb3f358ef3f18d886e6f8f872e`, module `github.com/google/go-github/v89`.
> - 204 non-test Go files, 1,262 `//meta:operation METHOD /path` annotations, and 42 `type XService service` declarations.
> - Methods use `context.Context`, path scalars, `url:`-tagged options passed to `addOptions`, bodies passed to `NewRequest`, and response targets passed to `Client.Do`.
> - Some methods have multiple route annotations; some operations share the same method/path (for example Subscribe and Unsubscribe both POST `/hub` and differ by form body); request wire bodies may be unexported projections (for example `CreateHook` converts `*Hook` to `*createHookRequest`); streaming, redirect, archive, upload, raw, and form endpoints exist.
> - Upstream accessor generators use standard `go/ast`, `go/parser`, and templates. `go-gen-jsonschema` uses `dave/dst` plus loaders/wrappers, but generated server code does not intrinsically require comment-preserving rewrites.
> - Current repo has only `go.mod`, `go.sum`, `lefthook.yml`, `HANDOFF.md`, and ephemeral artifacts; there is no implementation yet.
>
> Review a minimal concrete design before implementation. Look for correctness problems, missing constraints, unnecessary layers, wrong ownership boundaries, leaky abstractions, data-flow mistakes, failure modes, and proof gaps. In particular, decide what should be generated versus handwritten; what exact public service/constructor/response/auth API is simplest; how route annotations should map to client signatures and wire expressions; how broad completeness can be measured honestly; and how exceptional or colliding endpoints should be represented without silently narrowing the requested scope.
>
> Inspect the referenced repositories as useful. Do not edit product code. Write the exact prompt you were given and your findings to:
> `/Users/tyler/src/go-github-server/ephemeral/reviews/2026071201-claude-design.md`
>
> Label every finding with one of:
>
> - **critical:** must fix before proceeding.
> - **bug:** demonstrable incorrect behavior, broken contract, race, or regression.
> - **design:** architecture, boundary, scope, maintainability, or proof issue materially likely to cause problems.
> - **nit:** small cleanup that should not block progress.
>
> Conclude with a recommended minimal design and proof plan. Do not merely approve.

---

## Findings

### 1. **critical** — "Distinguish PAT from installation token" while "treating token contents as opaque" is unsatisfiable against go-github's own client

The auth requirement contains an internal contradiction that will surface the moment you write the delegation test.

- `WithAuthToken` (`github/github.go:611-612`) sets `Authorization: Bearer <token>` for **every** token — a PAT and an App installation token are byte-identical on the wire from the server's point of view. Across the whole package there are only 3 `"Bearer "` and 3 `"token "` literal forms, none of which vary by PAT-vs-installation *from the caller side*.
- The only header-level signals that could distinguish them are (a) the **scheme word** (`token ` classic vs `Bearer ` app/JWT) — but go-github emits `Bearer` for both, so the reference client destroys this signal — or (b) the **token prefix** (`ghp_`/`github_pat_` for PATs, `ghs_` for installation tokens), which requires inspecting token *contents* — forbidden by "treat as opaque."

So a middleware that "recognizes token header forms" and "treats contents as opaque" **cannot** decide PAT-vs-installation for a request produced by a normal `go-github` client. The test "prove PAT and app-token delegation" can only pass if you pin down a rule the transport can actually apply. Resolve **before** writing the `Authenticator` interface. Recommended resolution:

- Middleware parses `Authorization` into `(scheme, credential)` only — that is the honest limit of "opaque, form-based" recognition.
- PAT-vs-installation classification is **application policy**, not a transport fact. Either (a) require the caller to inject a classifier `func(scheme, cred string) TokenKind`, or (b) explicitly narrow "opaque" to permit *prefix-only* classification and document it. Do not pretend the transport can know.
- The delegation test must then drive whichever rule you chose (e.g. two requests whose scheme/prefix differ), not assume the byte-identical `Bearer` case can be split.

If you keep both `AuthenticateWithPAT(ctx, cred)` and `AuthenticateWithInstallationToken(ctx, cred)`, make the routing rule between them a first-class, documented input — this is the single largest correctness/scope risk in the whole design.

### 2. **critical** — Colliding routes (`Subscribe`/`Unsubscribe` → `POST /hub`) **panic** a Go 1.22 `ServeMux` at registration

Empirically verified: registering `POST /hub` twice on `http.NewServeMux()` panics —
`pattern "POST /hub" ... conflicts with pattern "POST /hub" ... matches the same requests`.
Any design that maps client methods 1:1 to `mux.Handle("METHOD /path", h)` **crashes at construction** for the two `/hub` methods (and for any other pair that resolves to an identical method+path). The operation-first IR in `HANDOFF.md` ("one record per HTTP method and canonical path") is the correct instinct — but it must be enforced as a hard invariant, and the generated public surface must not expose colliding client methods as independently-registered routes.

Required design constraints:

- The generator must **detect** method+path collisions structurally (group IR by `(METHOD, path)`), not assume uniqueness.
- `/hub` is additionally form-encoded (`NewFormRequest`, `github/repos_hooks.go:273`) and body-discriminated (`hub.mode = subscribe|unsubscribe`). It is **not** a JSON handler. Represent it as **one** handler for `POST /hub` that either hands the parsed form to a single impl method, or dispatches on `hub.mode` to two impl methods — never two mux registrations.
- For the first milestone, **exclude** `Subscribe`/`Unsubscribe` with a recorded skip reason rather than letting the collision block webhook delivery (see finding 11).

### 3. **design** — Server interface signature ≠ client method signature; the wire-body ownership rule must be decided explicitly (and `HANDOFF`'s instinct is right)

For ~25–30 methods the outgoing wire body is a **projected or unexported** type, not the public argument:

- `createHookRequest` (`repos_hooks.go:69`) is unexported and **hard-codes `Name:"web"`**; the public arg is `*Hook`. A server that mirrors the client signature (`CreateHook(ctx, owner, repo, *Hook)`) would have to *reverse* the projection, and the `"web"` constant is unrecoverable/meaningless on the receiving side.
- ~12 unexported `xxxRequest` types (`createRepoRequest`, `pullRequestMergeRequest`, `renameBranchRequest`, `markdownRenderRequest`, …), ~10 inline struct-literal bodies (`repoIDs{...}`, `scopeType{...}`), and ~6 `map[string][]string{...}` bodies.

Decision required: **generate server interfaces in terms of the WIRE contract, not the Go convenience signature.** `HANDOFF.md`'s rule — "decode using the actual wire-body type, not a guessed public argument type" — is correct; make it a hard rule. Concretely: emit an exported request DTO per operation (aliasing upstream entity structs where they are already exported/public, generating an exported equivalent where the upstream type is unexported/projected), and an exported response DTO. Entity structs (`Hook`, `Repository`) may be aliases; **request wrappers built from unexported types cannot** and must get generated exported equivalents or an explicit override. This is the central "what is generated" call.

### 4. **design** — Type-aware loading is mandatory; go-github's own generator style (pure `go/ast`) cannot do this job

To resolve the static type of the `NewRequest(ctx, m, u, <expr>)` body and the `Do(req, <target>)` target you need full `go/types` via `golang.org/x/tools/go/packages` with `NeedTypes|NeedTypesInfo` — the argument is usually an identifier whose declared type lives elsewhere. go-github's accessor/metadata generators (`github/gen-accessors.go`, `tools/metadata/metadata.go`) are **parse-only** (`parser.ParseDir`, regex + `ast.CommentMap`, no `go/types`) and cannot resolve expression types. `go-gen-jsonschema`'s loader (`internal/syntax/loader.go`, `decorator.Load` with `NeedTypes|NeedTypesInfo`) is the pattern to imitate for the type layer.

Additional forcing function: path scalars are **not all strings** — 391 methods take an `int64` path arg, 56 take `int`, plus `ArchiveFormat` (custom string enum) and a `time.Time` formatted into a URL. The decoder must read each arg's **declared type** for `strconv`; you cannot treat every `%v` as a string.

Recommended smallest stack: `go/packages` (type-aware) + `ast.CommentMap` for `//meta:operation` (imitate `tools/metadata/metadata.go:450-458`) + `text/template` + `go/format`. **`dave/dst` is not needed** — you are emitting fresh files, not doing comment-preserving rewrites (the prompt concedes this). Dropping dst also lets you trim the `tool`/require block in `go.mod` (finding 10).

### 5. **design** — Path→arg mapping is positional and sound, but must fail-closed on the enumerable exceptions

Positional mapping (Nth `%v` = Nth `{placeholder}`) is reliable — spot-checked across 463 `repos/%v/%v` builds with no reordering, and Go arg names routinely differ from placeholder names (`id int64` → `{hook_id}`; `org` → `{owner}`; `templateOwner` → `{template_owner}`). Drive the mapping off the authoritative `//meta:operation` placeholder list, positionally, against `fmt.Sprintf` args (1069 of 1262 ops build the URL with a single `Sprintf`; 44 use a constant). But **fail closed to a manual-override table** whenever:

- `%v` count ≠ placeholder count (query baked into the format: `?includes_parents=%v`, `?since=%v`, `search/%v?%v`);
- composite placeholder (`{basehead}` = `base` + `...` + `head`, each `url.QueryEscape`'d — `repos_commits.go:232`);
- URL not built by a single `fmt.Sprintf` (`admin_users.go:49` concat; six `u +=` sites; `GetArchiveLink` appends `opts.Ref` as a path segment);
- a bare non-ctx scalar `bool`/`int` sits between ctx and opts/body (route/flag selectors, see finding 6).

This tail is ~40–70 operations (3–5%). The 95–97% majority is genuinely mechanical.

### 6. **design** — Some Go parameters map to no wire element; on the server they are reconstructed from *which route fired*, not decoded

Exactly **33 methods carry 2 `//meta:operation` routes** (none carry 3+). The selector is usually an empty-string arg (`user==""` → `/user/...` vs `/users/{user}/...`; `org==""` → creation route) or a bool (`publicOnly`, `opts.PublicOnly`), and in one case the HTTP verb itself differs (`EditOrgMembership`: `PUT /orgs/...` vs `PATCH /user/...`). Plus non-wire params: `maxRedirects int` (client-only redirect depth, 6 methods), `RawOptions.Type` (switches `Accept: diff|patch`), `recursive bool` → `?recursive=1`.

Data-flow consequence for the server: each dual-route method = **two inbound routes → one impl**, and the impl (or the generated adapter) must reconstruct the selector (empty string / bool) from *which* path matched. Model these as two operations in the IR that map to the same impl method with the selector materialized by the adapter. Do not try to route them through a single mux pattern.

### 7. **design** — Response status and headers are not in the client signature; don't assume 200+JSON

Client methods encode neither success status (200/201/204) nor the pagination/rate-limit headers that `Response` exposes (`NextPage`, `Cursor`, `Rate`, populated from `Link`/`X-RateLimit` headers). `Do` also special-cases `nil` (313 sites, ~26%), `io.Writer`/`bytes.Buffer` (raw), and status-as-bool (`parseBoolResponse`, 12 methods like `IsStarred`). The response model must therefore represent **body + status + headers separately** (as `HANDOFF.md` already says) and let the impl set status/headers; generated code owns `Link`/`Rate` encoding as an injected policy. `openapi_operations.yaml` omits status codes — pull them selectively from `github/rest-api-description` only when needed, and default by verb (POST→201, DELETE→204, else 200) with per-op override. The GitHub error envelope must be an injected handwritten policy, not synthesized.

### 8. **design** — "Honest completeness": the denominator is 1262 operations, and the exceptional tail must be *classified and visible*, never silently dropped

Deduplicated by method, ~30% of operations (≈380–400) touch at least one non-"decode JSON → impl → encode JSON" concern; the dominant driver is the 313 `Do(req,nil)` status calls (mostly trivial). The **structurally hard** set — projected/unexported bodies (~25–30), dual routes (33), redirects/`*url.URL` (5+~10 variants), raw content (`string`/`[]byte`, ~15), form/upload (3) — is a much smaller, enumerable ~80–90 methods, plus cross-cutting custom `Accept` (152 sites) and `WithVersion` (7). Completeness is honest only if the tool:

- parses **all 1262** annotations and asserts every one is classified as `generated-clean | generated-with-override | skipped(reason)`;
- emits a coverage report listing skips with reasons (as `HANDOFF.md` requires);
- has a test that **fails if any annotation is unclassified** — otherwise the generator will silently narrow scope and *look* complete.

### 9. **design** — Keep the interface layering flat: ~42 service interfaces + a registrar, not per-operation micro-interfaces

`HANDOFF.md` floats "small capability/operation interfaces … optionally composed into resource interfaces." Taken literally that multiplies types toward 1262 and complicates registration. Simplest surface that still allows partial implementation: **one interface per service** (42), one method per operation; a generated `RegisterRepositories(mux, impl, ...opts)` that installs only the routes whose methods the impl actually provides (Go structural typing + per-capability type assertions, or an `Option`-gated registration). The constructor returns/populates a plain `*http.ServeMux` (which is an `http.Handler`). Do **not** leak `*github.Response`/pagination into the impl-facing interface — that is a client abstraction bleeding into the server contract.

### 10. **nit** — Trim `go.mod` once the dst decision is made; confirm Go-version floor

`go.mod` pulls `dave/dst`, `go-gen-jsonschema`, and `go-jsonschema` as tool/require deps. If you adopt the `go/packages`-only generator (finding 4), `dst` and the jsonschema tools are dead weight — remove them. Method-pattern routing needs Go 1.22+; the module declares `go 1.26`, upstream `go 1.25.0` — satisfied, no action beyond trimming.

### 11. **design** — The chosen first slice (`repos_hooks.go`) bundles the two hardest patterns; split it

Webhooks are a good first target, but the file simultaneously contains the `createHookRequest` wire projection (finding 3) **and** the `/hub` collision (finding 2). Sequence the milestone: (1) `List/Get/Edit/Delete/Ping/Test` hooks — clean, prove end-to-end first; (2) `CreateHook` — the wire-body extraction test; (3) explicitly **skip** `Subscribe`/`Unsubscribe` in slice 1 with a recorded reason. This proves the mechanical path and the projection path without letting the collision panic gate the milestone.

---

## Recommended minimal design

**Generate (deterministic, from a type-aware IR):**

1. **Operation IR** — one record per `(METHOD, canonical path)`, built with `go/packages` (`NeedTypes|NeedTypesInfo`) + `ast.CommentMap` for `//meta:operation`. Fields: service, opID, method, path, path params `{name (from placeholder) → Go type (resolved)}`, query-options type, **wire request type (static type of the `NewRequest` body expr)**, response target + kind (`json|nil|bool|raw|url`), success status, collision-group + discriminator, and provenance for every inferred field. Group by `(method,path)` to detect collisions.
2. **Per-service interface** (42) — one method per clean operation, signatures in terms of **exported wire request/response DTOs** (`func(ctx, Req) (Resp, error)`), not the client convenience signature. Entity structs alias upstream; unexported/projected bodies get generated exported DTOs.
3. **Registrar** per service — `RegisterX(mux *http.ServeMux, impl X, ...Option)` installing only implemented routes; constructor returns a plain `*http.ServeMux` (`http.Handler`).
4. **Transport adapters** — decode `PathValue` with per-type `strconv`, decode query via reconstructed `url:` tags (recursing embedded `ListOptions`), decode JSON into the wire DTO, invoke impl, encode `status + headers + (json|raw|none)`.
5. **Auth middleware** — an `Authenticator` interface with `AuthenticateWithPAT(ctx, cred)` / `AuthenticateWithInstallationToken(ctx, cred)`; generated middleware parses `Authorization` into `(scheme, credential)` and delegates via an **explicit, injected classification rule** (finding 1). No auth logic implemented.

**Handwrite / inject (the enumerable tail, ~80–90 methods + policies):**

- Override table for redirects/`*url.URL`, raw `string`/`[]byte`, uploads, form (`/hub`), projected/unexported bodies, composite/`?`-in-`Sprintf` paths, dual routes.
- The PAT/installation **classifier** (application policy).
- GitHub **error-envelope** mapper and **pagination/rate-limit header** policy (injected).

**Skip (recorded, never silent):** anything the inference can't validate; each carried in the coverage report with a reason.

## Proof plan

1. **Read-only discovery**: emit IR JSON for `repos_hooks` ops; assert placeholder/arg counts reconcile; print per-op classification (`clean|override|skip`).
2. **Generate** contract + adapters for `List/Get/Edit/Delete/Ping/Test` hooks only; golden-file the output.
3. **Real round-trip (tdd)**: drive a real `github.Client` at an `httptest.Server` wrapping the generated mux + a trivial impl. Prove: `ListHooks` → `[]*Hook`; `GetHook` decodes `int64` `{hook_id}`; `EditHook` PATCH body round-trips; `DeleteHook` → 204/no body. Assert **request fields reached the impl** (deserialization) and **response fields reached the client** (serialization).
4. **Wire-body projection**: `CreateHook` — client sends `*Hook`, server decodes an exported `createHook`-shaped DTO; assert impl sees `Config/Events/Active` and that the `"web"` name is handled explicitly, not silently invented.
5. **Auth delegation**: a fake `Authenticator` recording which method fired; two requests whose scheme/prefix differ per the finding-1 rule prove PAT vs installation routing. (This test is what forces the auth contradiction to be resolved.)
6. **Collision + completeness guards**: a test that registering the full generated set never panics (proves collision handling); a test that parses all 1262 annotations and **fails on any unclassified** op.
7. **Expand service-by-service**, each with an explicit coverage delta, only after webhooks pass end-to-end.

## Verdict

**Not approved as-is.** Three things must be settled in the design before implementation: the auth PAT-vs-installation contradiction (finding 1), the `/hub` collision that panics `ServeMux` (finding 2), and the wire-body ownership rule (finding 3). The operation-first IR, type-aware loading, positional path mapping with fail-closed overrides, flat 42-interface surface, and a classify-every-annotation coverage gate together make the "complete, generator-backed, honestly-measured" goal achievable — with a small, enumerable handwritten tail rather than a silently narrowed scope.
