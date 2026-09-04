# Go port design

**Status:** approved for planning, 2026-09-03.
**Plan:** `docs/superpowers/plans/2026-09-03-go-port.md`.
**Behavior reference:** `docs/behavior.md`.
**Motivating review:** `docs/review-2026-09-03.md`.

## Goal

Replace the `redirV2` Cloud Function with a Go service on Cloud Run that
serves the same links, from the same Firestore data, behind the same
Firebase Hosting sites and domains, with no visible change to visitors and
no data migration.

## Non-goals

- Changing the Firestore data model, field names, or counters.
- An admin UI or API for editing links. Links continue to be edited in the
  Firebase console.
- Moving off Firebase Hosting or changing DNS.
- Preview deployments per pull request. The current preview workflow cannot
  exercise backend changes and is removed rather than replaced.

## Architecture

```
Visitor -> Firebase Hosting (TLS, domains, public/, / redirect)
             -> rewrite "**" -> Cloud Run service "shorturl" (Go)
                                  -> Firestore (Admin SDK, same project)
```

Hosting stays the edge. Its rewrite block changes from a `function` target
to a `run` target. Hosting forwards the visitor's host in
`X-Forwarded-Host` and IP in `X-Forwarded-For`, which the service reads the
same way Express did.

The service is one Go binary, one container, one Cloud Run service in the
same region the function uses today (`us-central1`), so Firestore latency is
unchanged.

## Repository layout

```
cmd/shorturl/main.go          wiring: env, Firestore client, HTTP server, logging
cmd/pathaudit/main.go         one-off: report path rules that RE2 handles differently
internal/shorturl/
  request.go                  ParseRequest: host, slug, query, remainder, QR flags
  store.go                    Store interface, Link and PathRule types, JS truthiness
  firestore.go                Store implementation on cloud.google.com/go/firestore
  paths.go                    ResolvePath: first matching rule, ${name} substitution
  qr.go                       QRPNG with the original geometry
  frame.go                    html/template for frame mode
  passthrough.go              reverse proxy with the header allowlists
  handler.go                  Handler: mode dispatch, 404 flow, counters
  *_test.go                   unit tests on a fake Store; emulator test for Firestore
Dockerfile                    two stage, distroless static, nonroot
.github/workflows/ci.yml      gofmt, vet, test, container build on PRs
.github/workflows/deploy.yml  build, push, Cloud Run deploy, Hosting deploy on main
firebase.json                 rewrite target becomes the Cloud Run service
functions/                    unchanged until post-cutover cleanup, then deleted
```

The module path is the repository's GitHub path. `go.mod` declares
`go 1.27`, matching the newest `golang` container image tag.

## Components

### Request parsing

`ParseRequest(r *http.Request, hostOverride string) Request` reproduces the
five derivations in `docs/behavior.md`: host from `X-Forwarded-Host` with
port removed and `qr.` stripped, `/qr/` collapse, lowercase first segment as
slug, query after the first `?`, and the `^/[^/]+/` remainder. The raw
request target is used, not the decoded path, because the function never
decoded either.

`SHORTURL_HOSTNAME` keeps its name and meaning as the override.

### Store

```go
type Store interface {
    GetLink(ctx, host, slug string) (Link, error)        // ErrNotFound when absent
    ListPathRules(ctx, host, slug string) ([]PathRule, error)
    RecordClick(ctx, host, slug string, viaQR bool) error
    RecordQRCreate(ctx, host, slug string) error
    RecordPathMatch(ctx, host, slug, ruleID string) error
}
```

Documents are decoded from `map[string]any`, not into a struct, and flags
are read with JavaScript truthiness (`truthy`). Existing documents were
only ever tested with `if (data.flag)`, so a string `"true"` or a number `1`
must keep working. `statusCode` is accepted when it is a number between
200 and 599.

Slugs and hosts are checked with `ValidID` before any Firestore call.
Invalid IDs return `ErrNotFound`.

The Firestore implementation uses `firestore.Increment(1)` and
`firestore.ServerTimestamp` in `Update` calls, producing the same field
writes the function produced.

### Handler

`Handler` implements `http.Handler`. Order of operations:

1. Parse. Invalid host or slug: not-found flow.
2. `GetLink`. `ErrNotFound`: not-found flow. Other error: log, 500.
3. QR host and slug is not `404`: record `qrCreate`, respond PNG.
4. Record click (with `viaQR`).
5. Dispatch: passthrough, then path rules, then redirect.

Counter writes are synchronous with a 500 ms timeout and use
`context.WithoutCancel` so a visitor closing the connection does not abort
them. Cloud Run's request-based billing throttles CPU once the response is
complete, which is why they are not goroutines. Failure is logged and never
affects the response. The cost is one in-region Firestore write on the
request path, typically 10 to 30 ms.

Redirects write `Location` directly rather than using `http.Redirect`, so
relative destinations are sent as stored, matching Express.

### Passthrough

`net/http` client with a 30 s timeout. Same request and response header
allowlists as today, with `Content-Encoding` added to the response list
because the body is streamed. Body is forwarded for every method except GET
and HEAD. Non-2xx upstream becomes 500 unless `passthroughAnyStatus`.

### Path rules

`ResolvePath(logger, rules, remainder)` compiles each pattern with
`regexp.Compile`, skips patterns that fail to compile, and returns the first
match with `${name}` substituted. Unlike the function it does not require a
named group: the production data has 27 exact-match rules (GitHub release
asset mirrors) that the function could never match, and the intent of each
is unambiguous. Go 1.22 and later accept
`(?<name>...)`.

### QR

`github.com/skip2/go-qrcode`, level Medium, image size equal to the module
count (quiet zone included) times four pixels. Response carries
`Cache-Control: public, max-age=86400`.

### Frame

`html/template` with a doctype, `<meta charset>`, escaped title, a JSON
encoded short URL in the script, and a closed `<iframe>` element.

### Logging

`log/slog` JSON to stdout with `level` renamed to `severity` and `msg` to
`message` so Cloud Logging parses them. One warning per failed counter
write, per skipped path rule, and per passthrough upstream failure. No
request access log; Cloud Run produces one.

## Intentional deviations from the function

| Behavior | Function | Port |
|---|---|---|
| Empty or invalid slug | 500 | not-found flow |
| Firestore or other async error | request hangs to timeout | 500 |
| Path rule pattern fails to compile | request hangs | rule skipped, logged |
| Path rule with no named group | never matches | matches; destination used verbatim |
| Named group did not participate | substitutes `,` | substitutes `` |
| Missing `destination` | redirect to `undefined` | not-found flow |
| Counter write fails | unhandled rejection, process may exit | logged, redirect still served |
| Passthrough body | buffered, decompressed | streamed, `Content-Encoding` forwarded |
| QR image caching | none | `public, max-age=86400` |
| Frame HTML | quirks mode, unclosed iframe | HTML5, closed iframe |
| Regex dialect | JavaScript | RE2 (audit before cutover) |

Everything else, including counter field names, header allowlists, status
codes, the 404 flow, and the `apex` redirect, is preserved.

## Configuration

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | set by Cloud Run |
| `GOOGLE_CLOUD_PROJECT` | detected | Firestore project |
| `SHORTURL_HOSTNAME` | unset | collection override, same as today |

No secrets. The service authenticates to Firestore with the Cloud Run
service identity.

## Deployment

### Cloud Run

- Service name `shorturl`, region `us-central1`, unauthenticated
  invocations allowed (required by Hosting rewrites).
- `--max-instances 10`, `--concurrency 80`, `--cpu 1 --memory 256Mi`,
  `--timeout 60`. `--min-instances 0` to start; raise to 1 if cold start
  latency is noticed. A Go binary in a distroless image starts in well under
  a second.
- Image in Artifact Registry, repository `shorturl`, tagged with the short
  commit SHA.

### Hosting

`firebase.json` rewrite becomes

```json
{ "source": "**", "run": { "serviceId": "shorturl", "region": "us-central1" } }
```

The `functions` block is removed from `firebase.json` during cleanup, not
at cutover, so the old function keeps deploying until it is deleted on
purpose.

### CI

`ci.yml` on pull requests and pushes: gofmt, vet, `go test -race`, and a
container build. `deploy.yml` on push to `main`: test, authenticate with
Workload Identity Federation, build and push the image, `gcloud run
deploy`, then `firebase deploy --only hosting,firestore:rules`.

Project ID and Artifact Registry location come from GitHub repository
variables and the WIF provider and service account from repository secrets,
so no environment specific values live in the workflow files.

### Identity

One deployer service account with `roles/run.admin`,
`roles/firebasehosting.admin`, `roles/firebaserules.admin`,
`roles/firebase.viewer`, and `roles/serviceusage.serviceUsageConsumer` on the
project, `roles/artifactregistry.writer` on the `shorturl` repository only, and
`roles/iam.serviceAccountUser` on the Cloud Run runtime service account. The
Workload Identity provider's attribute condition admits only this repository's
`main` branch. The runtime service account needs only
`roles/datastore.user`. The JSON key secret is deleted after cutover.

## Cutover

1. Merge the port. `deploy.yml` creates the Cloud Run service. Hosting still
   points at the function because `firebase.json` has not changed yet.
2. Run `cmd/pathaudit` against production and fix or accept every reported
   rule.
3. Smoke test the service through its `run.app` URL with `X-Forwarded-Host`
   set, using the checklist in the plan.
4. Merge the `firebase.json` change. Hosting now routes to Cloud Run.
5. Re-run the smoke checklist against the real hostnames. Watch Cloud Run
   logs for `analytics write failed` and `passthrough` warnings for a day.
6. After a week: delete `functions/`, the preview workflows, the CodeQL
   JavaScript matrix entry, the `functions` block in `firebase.json`, the
   Cloud Function, and the JSON key secret.

### Rollback

Revert the `firebase.json` commit and let `deploy.yml` run, or run
`firebase deploy --only hosting` from the previous commit. The function is
still deployed until step 6. No data changes are involved in either
direction.

## Risks

- **Regex dialect.** Mitigated by `pathaudit`. If a rule needs lookaround,
  rewrite it as two rules or a wider match with post-filtering in the
  destination template. Decide per rule before step 4.
- **Firestore hot documents.** Unchanged from today. A burst on one link
  will slow or fail counter writes. Redirects still succeed because the
  write is bounded and non-fatal. If it becomes a problem, sample counter
  writes or move counters to a sharded subcollection; both are out of scope
  here.
- **Hosting to Cloud Run header behavior.** Verify `X-Forwarded-Host` on
  the first deployed revision before cutover; the smoke checklist covers it.
- **Cold starts with `--min-instances 0`.** Expected under a second. Raise
  to 1 if measured otherwise.

## Open decisions

None blocking. Two to revisit after cutover: whether to raise
`--min-instances` to 1, and whether to add an `SHORTURL_ALLOWED_HOSTS`
allowlist so direct hits on the `run.app` URL with arbitrary
`X-Forwarded-Host` values do not reach Firestore. Both are small follow-ups.
