# shorturl

A Firebase-hosted URL shortener with QR code generation, reverse proxy passthrough, regex-based path routing, and per-link analytics.

## Architecture

```
Request → Firebase Hosting → redirV2 Cloud Function → Firestore lookup → response
```

- **Runtime:** Node.js 20, Firebase Cloud Functions v2 API (`firebase-functions` 7.x)
- **Database:** Cloud Firestore (collections keyed by hostname, documents by slug)
- **Hosting:** Firebase Hosting with all non-root paths rewritten to the `redirV2` function
- **Security:** Firestore rules deny all client access; data is accessed exclusively via Admin SDK

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

## Project structure

```
functions/
  index.js            # redirV2 Cloud Function (entire application logic)
  package.json        # Dependencies: firebase-admin, firebase-functions, qrcode
  .eslintrc.json      # Linting rules
public/
  index.html          # Root redirect (customize for your org)
  404.html            # Fallback 404 page
.github/workflows/
  deploy.yml          # Push to main → production deploy
  preview.yml         # PR → preview channel (expires 7 days)
  preview-cleanup.yml # PR close → delete preview channel
  codeql.yml          # Weekly + PR security scanning
firebase.json         # Hosting rewrites, emulator config, predeploy hooks
firestore.rules       # Deny all client access
```

## Development

```bash
cd functions
npm install
npm run lint        # ESLint
npm run serve       # Firebase emulator (localhost:5000)
```

### Version stamping

The predeploy hook runs `npm run version:generate`, which writes the short Git SHA to `functions/version.json`. The function exposes this as the `X-ShortUrl-Ver` response header in passthrough mode.

## Deployment

Automated via GitHub Actions on push to `main`. PRs get ephemeral preview deployments.

Manual deploy:

```bash
cd functions
npm run version:generate
firebase deploy --only functions,hosting
```

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

| Package | Purpose |
|---|---|
| `firebase-admin@^13` | Firestore access, app initialization |
| `firebase-functions@^7` | Cloud Functions HTTP handler |
| `qrcode@^1.5.4` | QR code PNG generation |

## Status

Planning a port to Go on Cloud Run behind the same Firebase Hosting sites.
The current behavior is documented in `docs/behavior.md`, the review that
motivated the port in `docs/review-2026-09-03.md`, the design in
`docs/superpowers/specs/2026-09-03-go-port-design.md`, and the step by step
plan in `docs/superpowers/plans/2026-09-03-go-port.md`.

**Status (2026-09-03):** Tasks 1 through 11 of the plan are done. The Go
service, tests, container, CI, deploy workflow, and the cloud identity and
registry setup are in place, and an idle Cloud Run service exists. Hosting
still routes to the Cloud Function.

A second Hosting site (`go` target in `firebase.json`) routes to the Cloud
Run service so a deprecated test domain can exercise it while every other
domain stays on the function.

**Next:** test on the `go` site, then Task 12 (repoint the `prod` rewrite)
and Task 13 (remove `functions/`) as separate pull requests.
