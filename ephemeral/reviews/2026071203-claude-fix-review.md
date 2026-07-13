# Independent post-fix review (round two) — go-github-server

Reviewer: Claude. Date: 2026-07-12. Read-only; no product code edited.
Evidence base: `server.go`, `cmd/gen-server/main.go`, `zz_generated.go`, `coverage.json`,
`server_test.go`, `cmd/gen-server/main_test.go`, the prior review
`ephemeral/reviews/2026071202-claude-implementation.md`, and upstream
`/Users/tyler/src/go-github/github` (`github.com/google/go-github/v89`).

Proof re-run green on this machine:

```text
go build ./...            ok
go vet ./...              ok
go test ./...             ok
go test -race ./...       ok
golangci-lint run ./...   0 issues
go run ./cmd/gen-server -check   (exit 0)
go generate ./...        → zz_generated.go and coverage.json byte-stable
```

Two headline findings below are reproduced empirically with throwaway in-package tests
(since removed), driven both at the wire level and through the real `github.Client`.

---

## The exact prompt I was given

> # Independent post-fix review prompt
>
> Here is my goal:
>
> Implement `github.com/tylergannon/go-github-server` as the complete AST-generated server-side inverse of the routed `github` package in `github.com/google/go-github/v89`: typed service interfaces preserving upstream grouping, a constructor returning `*http.ServeMux`, generated path/query/body codecs and response codecs, reuse of upstream entity types, opaque PAT and installation-token delegation, complete route classification, and real `go-github`/testify round-trip proof. Stay within `github`; do not invent unrelated layers.
>
> This is review round two after the findings in `ephemeral/reviews/2026071202-claude-implementation.md`. Review the complete current implementation, not just the listed fixes. The prior critical/bug findings were addressed as follows:
>
> - Shared HTTP operations are still grouped, but candidates now order by upstream AST-extracted `Accept` media types and direct transport ownership, then fall through only when the candidate returns `ErrNotImplemented`. This supports content negotiation and implementations provided through cross-service aliases.
> - `coverage.json` now marks all shared operations `generated-with-override`; six client convenience annotations whose signatures cannot reconstruct a shared canonical path are `generated-alias` and are omitted as independent route candidates.
> - Real-client regressions cover `GetContents` plus `DownloadContents`, release asset JSON vs octet-stream redirect selection, JSON/diff/patch/SHA commit selection, and cross-service Actions/Organizations fallback.
> - Binary uploads bind `*os.File` through a temporary spool and `io.Reader` directly; a real `UploadReleaseAsset` test proves bytes arrive.
> - Redirect-or-download methods now encode either streamed content or `Location` correctly.
> - Entity-derived path parameters such as `Repository.ID` and secret names are reconstructed using generated field bindings; a real-client repository-ID test proves this path.
> - `/hub` is explicitly classified as a form-discriminated override and remains round-trip tested.
> - The matcher no longer mutates `PathValue` while probing, no longer adds unrequested HEAD behavior, and a full generated representative-path test proves each unique pattern selects itself under literal-first ordering.
> - Dead import anchors and lint findings were removed.
>
> Current proof:
>
> ```text
> go generate ./...
> go run ./cmd/gen-server -check
> go test ./...
> go test -race ./...
> go vet ./...
> golangci-lint run ./...
> ```
>
> All pass; golangci-lint reports `0 issues`.
>
> Review against the full original goal and the prior review. Look especially for remaining critical, bug, or design findings involving shared-operation dispatch, route/path binding, projected bodies, uploads, raw/redirect responses, coverage truthfulness, authentication, or proof scope. Inspect and run focused read-only checks. Do not edit product code.
>
> Write the exact prompt and all findings to:
> `/Users/tyler/src/go-github-server/ephemeral/reviews/2026071203-claude-fix-review.md`
>
> Use labels:
>
> - **critical:** must fix before proceeding.
> - **bug:** demonstrable incorrect behavior, broken contract, race, or regression.
> - **design:** architecture, boundary, scope, maintainability, or proof issue materially likely to cause problems.
> - **nit:** small cleanup that should not block progress.
>
> Use file/line references and conclude with proof still needed. Do not merely approve.

---

## Summary verdict

**Not approvable as complete.** The round-two work genuinely fixes the round-one critical
routing collapse: `serveOperationGroup` now scores candidates by AST-extracted `Accept` media
type and falls through on `ErrNotImplemented` (server.go:186–212, 227–262), the matcher no longer
mutates `PathValue` while probing (server.go:85–88, 134–170), uploads spool `*os.File` through a
temp file (server.go:403–415), the HEAD fallthrough is gone, and the content-negotiation and
cross-service-fallback claims are all backed by real-client tests that I re-ran green. Findings
1, 3, 4, 6 (mutation), 9 and 10 (sentinels) from the prior review are addressed.

**But the prompt's claim "Redirect-or-download methods now encode either streamed content or
`Location` correctly" is false, and demonstrably so.** Only the *one* case the prior review named
by name — the `download`-kind `DownloadReleaseAsset` string redirect — was fixed. Every *other*
redirect endpoint is broken:

- **Five `*url.URL` redirect endpoints** (`DownloadArtifact`, `GetWorkflowRunLogs`,
  `GetWorkflowJobLogs`, `GetWorkflowRunAttemptLogs`, `GetArchiveLink`) are classified as ordinary
  **`json`** responses. The server JSON-encodes the `url.URL` struct instead of emitting a
  `Location` redirect. Four of the five are marked **`generated-clean`** — the same false-
  completeness pattern the prior review flagged as its finding 2.
- **`MigrationArchiveURL`**, the *only* operation correctly detected as `url` kind, returns a
  `string`, but the `"url"` response branch only handles `*url.URL`, so it emits a **bare 302 with
  no `Location`**.
- **`DownloadArtifact` returns HTTP 400 on every call**, because the generator binds the
  non-path control parameter `maxRedirects int` to the `{archive_format}` path placeholder.

So the same class of defect the round-one review opened with — an operation marked
`generated-clean` that cannot actually serve its contract, with no test exercising it — is still
present, just moved from "unreachable method" to "reachable method that encodes the wrong
response / mis-binds the path." The behavioral guard added this round
(`TestEveryGeneratedRouteHasAnUnambiguousRepresentativePath`) proves *path → route-group*
selection but asserts nothing about binding or response encoding, which is exactly why these
slipped through.

---

## Findings

### 1. **critical** — Five `*url.URL` redirect endpoints are classified `json`; the server serializes the URL struct instead of redirecting, and four are `generated-clean`

The redirect endpoints that return `(*url.URL, *Response, error)` all delegate to unexported
`*WithRateLimit` / `*WithoutRateLimit` helpers rather than calling `bareDoUntilFound` in the
annotated method body:

- `DownloadArtifact` (actions_artifacts.go:161)
- `GetWorkflowRunLogs` (actions_workflow_runs.go:383)
- `GetWorkflowRunAttemptLogs` (actions_workflow_runs.go:263)
- `GetWorkflowJobLogs` (actions_workflow_jobs.go:150)
- `GetArchiveLink` (repos_contents.go:337, two routes tarball/zipball)

`analyzeBody` only recognizes the redirect shape when the literal call `bareDoUntilFound` appears
in the method body (main.go:278–280). Because these methods call it *indirectly*, the discriminator
is missed and `ResponseKind` falls through to `"json"` (main.go:297–299). `writeResults` then takes
the default JSON branch (server.go:584–594) and `json.Marshal`s the `*url.URL`.

Empirically confirmed (throwaway test, since removed), driving `GetWorkflowRunLogs` through the real
client and at the wire:

```text
client Actions.GetWorkflowRunLogs(...) → url=<nil>  err=<nil>        // silently returns nil URL
raw wire: status=302 Location="" Content-Type="application/json"
          body={"Scheme":"https","Opaque":"","User":null,"Host":"downloads.example.test",
                "Path":"/logs.zip", ...}
```

The go-github client's `bareDoUntilFound` expects a 3xx with a `Location`; it receives a 302 whose
body is a JSON dump of `url.URL` and whose `Location` is empty, so it returns **`nil, nil`** — the
caller gets no URL and no error. All five endpoints are broken this way. `coverage.json` marks
`DownloadArtifact`, `GetWorkflowRunLogs`, `GetWorkflowRunAttemptLogs`, and `GetWorkflowJobLogs`
**`generated-clean`** (verified in the file), so the "complete, all classified" claim again asserts
correctness for operations that cannot serve their contract.

Fix direction: detect the redirect shape by *result signature* (`results[0]` is `*url.URL`), not by
spotting `bareDoUntilFound` in the body — mirroring how `isDownloadSignature`/`isStreamSignature`
already work off the signature (main.go:305–323). Then have the `url` branch set `Location` and a
302. Add a real-client test per redirect family.

### 2. **bug** — `MigrationArchiveURL` (the only `url`-kind op) returns a `string`, so the `"url"` branch never sets `Location`; response is a bare 302

`MigrationArchiveURL` (migrations.go:180) *does* call `bareDoUntilFound` directly, so it is the one
operation correctly classified `ResponseKind == "url"` (coverage.json: `generated-with-override`,
reason `url response`). But its signature is `(url string, err error)` — a `string`, not a
`*url.URL`. The `"url"` branch asserts `bodyValues[0].(*url.URL)` (server.go:544–552); the assertion
fails for a `string`, so **no `Location` header is written** and the client gets a bare 302.

Empirically confirmed (throwaway test, since removed):

```text
MigrationArchiveURL: status=302 Location=""
```

This is the identical failure mode the prior review called out as its finding 5 for
`DownloadReleaseAsset`; the fix applied there (the `download` branch handling a redirect `string`,
server.go:553–570) was not carried over to the `url` branch. The `url` branch must handle a `string`
result (parse/set it as `Location`) as well as `*url.URL`.

### 3. **bug** — `DownloadArtifact` returns 400 on every call: the control parameter `maxRedirects int` is bound to the `{archive_format}` path placeholder

`operationBindings` maps the i-th path placeholder to the i-th entry of `scalarCandidates`, which
collects *every* string/int/uint parameter (main.go:562–569, 675–688). For
`DownloadArtifact(ctx, owner, repo string, artifactID int64, maxRedirects int)` on
`/repos/{owner}/{repo}/actions/artifacts/{artifact_id}/{archive_format}` there are four placeholders
and four scalar candidates, so the binding is:

```text
p0→owner  p1→repo  p2→artifactID  p3(archive_format)→maxRedirects   // wrong
```

`maxRedirects` is a client-side redirect budget, not a URL value (the upstream URL even hardcodes
`/zip` and never interpolates `archive_format`). Binding the `{archive_format}` string ("zip") into
the `int` parameter fails to parse:

```text
GET /repos/octo/demo/actions/artifacts/5/zip
→ 400  "path parameter p3: strconv.ParseInt: parsing \"zip\": invalid syntax"
```

(Confirmed empirically, since removed.) The generated binding in `zz_generated.go` shows four
`bindingPath` entries with no query/option for the trailing segment.

`GetArchiveLink` has the same root cause with a different symptom: its `{ref}` placeholder is bound
to the `archiveformat ArchiveFormat` parameter (index 3, string-underlying), while the real `ref`
value upstream comes from `opts.Ref` (repos_contents.go:337–341). So `archiveformat` receives the
git ref and `opts.Ref` is never populated — a silent mis-bind even before finding 1's encoding bug
applies.

Root problem: positional scalar→placeholder alignment assumes every scalar parameter, in order, is a
path parameter. Control/format parameters (`maxRedirects`, and formats that are encoded as path
*literals* rather than placeholders) break the alignment. The binder needs to align placeholders to
parameters by name/URL-construction evidence (it already parses `fmt.Sprintf` for
`structuredPathCandidates`, main.go:623–651 — the same evidence could exclude non-path scalars),
not by raw positional order.

### 4. **bug** — `CompareCommits`: the composite `{basehead}` segment is bound whole into `base`, leaving `head` empty

`CompareCommits(ctx, owner, repo, base, head string, opts *ListOptions)` is annotated
`GET /repos/{owner}/{repo}/compare/{basehead}` (repos_commits.go:240). On the wire the single
segment is `base...head`. The generated binding (zz_generated.go) is:

```text
p0→owner  p1→repo  p2(basehead)→base     // head is never bound → ""
```

so a client `CompareCommits(ctx, owner, repo, "v1", "v2", nil)` reaches the implementation as
`base="v1...v2", head=""`. The operation is flagged with reason `composite path parameter`
(main.go:612–614) but is still emitted as a live, `generated-with-override` route that silently
delivers wrong arguments — the override label documents that something is special without preventing
the incorrect dispatch. Either split `{basehead}` on `...` into the two scalars, or treat it as an
alias/omitted route the way the six `generated-alias` entries are, rather than serving it wrong.
(`CompareCommitsRaw` shares the segment and additionally the finding-1 encoding issue.) No test
covers either compare endpoint.

### 5. **design** — Response status is still verb-guessed with no upstream override; `POST → 201` remains a fact-shaped guess

`writeResults` still defaults `POST → 201 Created` when the implementation returns no
`*github.Response` with a status (server.go:519–520). This is prior finding 8, unaddressed: no
per-operation status table sourced from upstream exists. It is tolerable for go-github's happy path
(its `Do` accepts any 2xx), and the tests all pass explicit statuses so the default is never
exercised, but the default is presented as correct for endpoints where it is not (e.g.
`.../dispatches` is 204, `POST /markdown` is 200). Document it as a fallback and make it overridable,
or derive per-op expected status.

### 6. **design** — The new completeness guard proves route *selection*, not *binding* or *response encoding*; findings 1–4 are invisible to it

`TestEveryGeneratedRouteHasAnUnambiguousRepresentativePath` (server_test.go:400–418) is a real
improvement: it drives one representative path per route group and asserts the correct group is
selected under literal-first ordering, and the `New(generatedUnimplementedServices())` construction
proves every service interface is satisfiable. But it substitutes `123` for all wildcards and never
invokes the implementation, so it cannot see that `DownloadArtifact` 400s on a non-integer segment,
that a `*url.URL` is JSON-encoded, or that `CompareCommits` drops `head`. `coverage.json`'s guard
(main_test.go:10–38) asserts collision members are `generated-with-override` and that classification
is total — but "classified" still does not imply "serves its contract." A generated table test that,
for each *routed* operation, issues a representative request against a stub and asserts the intended
method received well-formed arguments and that the response kind matches the upstream method's
result signature would have caught findings 1–4. This is the single most valuable missing proof.

### 7. **design** — `/hub` reconstruction is still hand-written positional args tied to the `Subscribe`/`Unsubscribe` signature

Classification is improved — `/hub` is now `generated-with-override` with reason
`form-discriminated operation` (main.go:495–498), addressing the prior "mis-classified clean"
complaint — and dispatch-by-`hub.mode` is tested (server_test.go:179–203). But
`populateHubArgs` still writes positional `args[1..5]` assuming the exact upstream shape
`(ctx, owner, repo, event, callback string, secret []byte)` (server.go:455–472). If the upstream
signature changes, this reconstructs silently-wrong arguments rather than failing to generate. It is
the one place a hand-maintained shape is smuggled into an otherwise generated pipeline; consider
deriving the form-field→parameter mapping the same way bodies are derived.

### 8. **design** — Unused generator tool dependencies remain in `go.mod` (prior finding 10 not completed)

The generator is `go/packages`-only — `grep` finds no import of `dave/dst`, `go-jsonschema`, or
`gen-jsonschema` in any `.go` file. Yet `go.mod`'s `tool (...)` block still lists
`github.com/atombender/go-jsonschema` and `github.com/tylergannon/go-gen-jsonschema/gen-jsonschema`,
which `go mod why` confirms is the sole reason `github.com/dave/dst`, `dario.cat/mergo`,
`sanity-io/litter`, `spf13/cobra`, `goccy/go-yaml`, etc. are pulled into the module graph as
indirect deps. Only `goimports` and `modernize` from the tool block are actually used (by
`lefthook.yml`). The prior review's finding 10 explicitly asked to "confirm dst/jsonschema tool deps
are actually gone"; they are not. Drop the two unused tool directives and run `go mod tidy`.

### 9. **design** — `serveOperationGroup` reads the entire request body into memory for every request, defeating the upload temp-file spool

`serveOperationGroup` does `payload, err := io.ReadAll(r.Body)` unconditionally (server.go:200) so it
can replay the body across fall-through candidates, then resets `r.Body` to a reader over `payload`
for each candidate. For `UploadReleaseAsset` this means the entire binary is buffered in memory as
`payload` *before* `bindingTempFile` copies it to a temp file (server.go:403–415) — so the temp-file
spool, whose purpose is to avoid holding the upload in memory, provides no benefit; the payload is
already fully in RAM. Fall-through is only needed for collision groups; upload routes are single-op
groups. Read-and-replay could be scoped to groups with more than one candidate, letting single-op
upload routes stream `r.Body` straight to the spool.

### 10. **nit** — Fall-through can leak headers if a partially-implemented method returns `ErrNotImplemented` alongside a non-nil `*github.Response`

In the fall-through loop, `writeResults` copies `response.Header` into `w.Header()` (server.go:500–504)
during result processing, but returns the error *before* `WriteHeader` when a non-nil `error` result
is present (server.go:480–485). For the generated `Unimplemented*` stubs this is harmless (they
return a nil `*github.Response`), but a hand-written partial implementation that returns
`(value, resp, ErrNotImplemented)` would deposit `resp`'s headers into `w` and then fall through to
the next candidate, which would emit them. Contrived, but worth a guard: only copy response headers
once a candidate is committed.

---

## What is genuinely good (not mere approval)

- The round-one critical (alphabetical shadowing) is really fixed. `orderedOperations` +
  `operationScore` (server.go:227–262) rank by AST-extracted `Accept` media type, and the
  `ErrNotImplemented` fall-through (server.go:206–212) lets a shadowed or cross-service
  implementation win. Verified end-to-end: `GetContents` vs `DownloadContents`
  (server_test.go:219–240), `GetReleaseAsset` vs `DownloadReleaseAsset` (…:265–289), JSON/diff/patch/
  SHA commit selection (…:352–371), and the cross-service Actions/Organizations fallback (…:299–311)
  all pass through the real client.
- `Accept` extraction is correct where the header is set from a string constant, including the
  dual diff/patch constants on `GetCommitRaw` and the `octet-stream` on `DownloadReleaseAsset`
  (main.go:282–286; verified in `zz_generated.go` and `TestGeneratorExtractsAcceptDiscriminator`).
- The upload path now works: `bindingTempFile` spools `*os.File` and a real
  `UploadReleaseAsset` round trip proves the bytes arrive (server.go:403–415, server_test.go:254–289).
  Prior finding 4 resolved.
- Entity-field path reconstruction works for `*repo.ID`-style `fmt.Sprintf` interpolation
  (`structuredPathCandidates`/`selectorParameter`, main.go:623–673) and is proven by
  `TestPathParameterRehydratesEntityField` (server_test.go:324–333).
- The matcher no longer mutates the request while probing — `match` returns a values map that the
  caller applies only for the winning group (server.go:85–88, 134–170). Prior finding 6 (mutation)
  resolved. The HEAD fallthrough is gone (prior finding 9 resolved); import sentinels are gone
  (prior finding 10, partially — see finding 8 above for the leftover module deps).
- Authentication is unchanged and still correct: scheme-only parse, prefix-family classification,
  untouched credential forwarded, malformed/unsupported forms rejected (server.go:264–296), proven
  for `ghp_`/`github_pat_`/`ghs_` and rejected for Basic/`gho_`/opaque
  (server_test.go:152–177, 373–389).

---

## Proof still needed after fixes

1. **Response-encoding conformance per response kind.** A generated test that, for every routed
   operation, asserts the emitted response kind matches the upstream method's *result signature*:
   `*url.URL`/redirect → 302 + `Location` (no body); `io.ReadCloser` → streamed body; `bool` →
   204/404; `string`/`[]byte` raw → body with the right `Content-Type`; else JSON. This is what would
   catch findings 1 and 2. Until it exists, "encodes … `Location` correctly" is unsubstantiated.
2. **Redirect round trips through the real client.** At least `DownloadArtifact`,
   `GetWorkflowRunLogs`, `GetArchiveLink`, and `MigrationArchiveURL` driven through
   `github.Client`, asserting the returned `*url.URL`/`string` is non-nil and equals the server's
   redirect target. All four currently fail.
3. **Path-binding conformance.** A generated test that, per routed operation, issues a representative
   request with *distinct, type-appropriate* segment values and asserts each declared path parameter
   arrives intact — catching `DownloadArtifact`'s `maxRedirects` mis-bind (finding 3),
   `GetArchiveLink`'s `ref`→`archiveformat` mis-bind, and `CompareCommits`'s dropped `head`
   (finding 4). The current representative-path test uses `123` for all wildcards and never inspects
   arguments.
4. **Coverage truthfulness for redirect/composite ops.** Reclassify the five `*url.URL` redirect
   methods off `generated-clean` (they need response-kind override), and either split or alias the
   composite `{basehead}` route, with a `main_test.go` assertion that `generated-clean` implies
   "well-formed path binding AND response kind == json".
5. **Status defaults.** Either a per-op expected-status source or an explicit test pinning the
   verb-default for the endpoints where the guess is wrong (204/200 POSTs) — finding 5.
6. **Dependency hygiene.** `go mod tidy` after dropping the unused `go-jsonschema`/`gen-jsonschema`
   tool directives, with a check that the module graph no longer contains `dave/dst` et al.
   (finding 8).

Until items 1–4 exist and pass, the round-two claim "Redirect-or-download methods now encode either
streamed content or `Location` correctly" is contradicted by the artifact: every `*url.URL` redirect
endpoint and `MigrationArchiveURL` are broken, `DownloadArtifact` 400s on every call, and four of
the redirect endpoints are still advertised as `generated-clean`.
