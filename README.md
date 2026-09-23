# shorturl

A Firebase-hosted URL shortener with QR code generation, reverse proxy passthrough, regex-based path routing, and per-link analytics.

## Architecture

```
Request → Firebase Hosting → Cloud Run (Go) → Firestore lookup → response
```

- **Runtime:** Go 1.27 on Cloud Run, one distroless container (`cmd/shorturl`)
- **Database:** Cloud Firestore (collections keyed by hostname, documents by slug)
- **Hosting:** Firebase Hosting with all non-root paths rewritten to the `shorturl` Cloud Run service
- **Security:** Firestore rules deny all client access; the service reads and writes with its own Cloud Run identity

## Features

| Mode | Firestore field | Behavior |
|---|---|---|
| **Redirect** | (default) | 307 redirect to `destination` |
| **Frame** | `frame: "Title"` | Renders destination in a full-page iframe with the given title; replaces browser URL via `history.replaceState` |
| **Passthrough** | `passthrough: true` | Reverse-proxies the request to `destination`, forwarding select headers and returning the upstream response |
| **Path routing** | `usePaths: true` | Matches remaining path segments against regex patterns in a `paths` subcollection; named capture groups interpolate into the destination template |

Additional per-link options:

- `passQueryString: true` — appends the original query string to the destination
- `statusCode: 301` (etc.) — overrides the default 307 redirect status
- `passthroughAnyStatus: true` — returns upstream response even on non-2xx status

### QR codes

Requesting via the `qr.*` subdomain (e.g. `qr.example.com/slug`) returns a QR code PNG pointing to `https://hostname/qr/slug`. Visiting a `/qr/...` path follows the normal redirect flow but additionally increments QR-specific counters.

### Analytics

Each redirect increments Firestore counters on the document:

- `clickCount` / `clickLast`
- `qrUseCount` / `qrUseLast` (when accessed via `/qr/` path)
- `qrCreateCount` / `qrCreateLast` (when QR image is generated)
- Per-path-pattern: `matchCount` / `matchLast`

Counts are batched per instance and written at most every 5 seconds, so a
busy link does not exceed Firestore's per-document write rate. An idle
instance writes its counts on its next request or at shutdown, so totals
can lag. The `*Last` fields hold the time of the latest visit.

## Project structure

| Path | Responsibility |
|---|---|
| `cmd/shorturl/` | entry point: env, Firestore client, HTTP server, shutdown |
| `cmd/pathaudit/` | report of stored path rules that RE2 handles differently from JavaScript |
| `internal/shorturl/request.go` | request parsing: host, slug, query, remainder |
| `internal/shorturl/store.go` | `Store` interface, `Link`, `PathRule`, ID validation |
| `internal/shorturl/firestore.go` | `Store` backed by Firestore |
| `internal/shorturl/handler.go` | mode dispatch and the 404 flow |
| `internal/shorturl/counters.go` | batched analytics counters |
| `internal/shorturl/passthrough.go` | reverse proxy with header allowlists |
| `internal/shorturl/paths.go` | path rule matching |
| `internal/shorturl/qr.go`, `frame.go` | QR images and frame pages |
| `public/` | static root redirect and 404 page |
| `Dockerfile` | container build |
| `.github/workflows/` | `ci.yml` (PR checks), `deploy.yml` (push to main), `codeql.yml` |
| `firebase.json`, `firestore.rules` | Hosting rewrites; rules that deny all client access |

## Development

```bash
go test ./...

# The Firestore store test runs against the emulator and is skipped otherwise.
npx firebase-tools@15.5.1 emulators:exec --only firestore --project shorturl-test "go test ./..."
```

`gofmt -l .` must print nothing and `go vet ./...` must pass; CI checks both.

### Configuration

| Variable | Meaning |
|---|---|
| `PORT` | listen port, set by Cloud Run (default `8080`) |
| `GOOGLE_CLOUD_PROJECT` | Firestore project (detected when unset) |
| `SHORTURL_HOSTNAME` | replaces the request host as the collection name |
| `SHORTURL_HOST_ALIASES` | comma separated `host:collection` pairs, for example `www.example.com:example.com` |

The build stamps the short Git SHA into the binary, reported as the `X-ShortUrl-Ver` header on passthrough requests and responses.

## Deployment

Automated via GitHub Actions on push to `main`: `deploy.yml` tests, builds and pushes the image, deploys it to Cloud Run, then deploys Hosting and Firestore rules. It authenticates with Workload Identity Federation, so no key is stored in the repo or in GitHub. Environment variables set on the Cloud Run service are kept across deploys.

## Firestore data model

Each hostname served is a top-level collection. Documents within are keyed by slug (lowercased).

```
example.com/                     # collection = hostname
  demo/                          # document = slug
    destination: "https://..."
    frame: "Page Title"          # optional
    passthrough: false           # optional
    passQueryString: true        # optional
    usePaths: false              # optional
    statusCode: 307              # optional
    clickCount: 42               # auto-incremented
    clickLast: Timestamp         # auto-updated
    paths/                       # subcollection (when usePaths: true)
      rule1/
        pattern: "^docs/(?<page>.+)$"
        destination: "https://docs.example.com/${page}"
        matchCount: 5
        matchLast: Timestamp
```

## Dependencies

| Module | Purpose |
|---|---|
| `cloud.google.com/go/firestore` | Firestore access |
| `github.com/skip2/go-qrcode` | QR code PNG generation |

## Status

Running on Cloud Run since 2026-09-04. The Cloud Function it replaced was removed on 2026-09-22. Exact behavior is in `docs/behavior.md`; the port's design and the review that motivated it are in `docs/superpowers/specs/2026-09-03-go-port-design.md` and `docs/review-2026-09-03.md`.

Passthrough request bodies are buffered (10 MiB cap), so upstream 307/308 redirects and HTTP/2 GOAWAY retries re-send the body. No open work is queued.
