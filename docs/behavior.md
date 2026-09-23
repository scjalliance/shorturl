# Request handling behavior

This is the exact behavior of the Go service in `internal/shorturl`, which
runs on Cloud Run behind Firebase Hosting. It replaced the `redirV2` Cloud
Function on 2026-09-04 and serves the same Firestore data. Where it
intentionally differs from the function, the difference is listed in the
design spec's "Intentional deviations" table. The function's own behavior is
in this file's git history before the function was removed.

## Request parsing

1. **Host.** `X-Forwarded-Host` (set by Hosting) if present, else `Host`,
   with the port dropped. A leading `qr.` is removed to get the collection
   name. If a `qr.` was removed, this is a **QR create** request. The
   environment variable `SHORTURL_HOSTNAME`, when set, replaces the
   collection name but does not affect QR detection. After that,
   `SHORTURL_HOST_ALIASES` maps a host to another collection, for example
   `www.example.com:example.com`.
2. **URL.** The raw path and query, not URL decoded. A leading `/qr/` (any
   number of slashes on either side of `qr`) collapses to `/`. If that
   changed anything, this is a **QR scan** request.
3. **Slug.** Remove one leading `/`, take everything up to the first `/` or
   `?`, lowercase it. Not URL decoded, so `/Foo%20Bar` looks up
   `foo%20bar`. `/` and `//foo` produce an empty slug.
4. **Query.** Everything after the first `?`, without the `?`.
5. **Remainder** (path rules only). URL with `^/[^/]+/` removed. If there
   is no second slash the remainder is the whole URL, leading slash and
   query string included. Because `[^/]` matches `?`, a URL like
   `/slug?x=1/foo` has remainder `foo`.

## Lookup

Collection = host, document = slug.

- An empty slug, or a host or slug Firestore cannot use as an ID (`.`,
  `..`, `__name__` style, over 1500 bytes, or containing `/`), takes the
  not-found flow without calling Firestore.
- A Firestore read error responds `500 Internal Server Error`.

## Not found

If the document does not exist:

- Slug is `404`: 302 to `/404.html`, the static page in `public/`.
- Otherwise: 302 to `/404` followed by the URL, for example
  `/missing?x=1` goes to `/404/missing?x=1`. That second request looks up
  the `404` document under the same host, so each host can have its own
  404 link with `usePaths` rules or a plain destination.

No counters are touched for a missing document.

## QR create (request on a `qr.` host)

If the document exists and the slug is not `404`:

1. Count `qrCreate` (see Counters).
2. Respond `200 image/png` with a QR code for
   `https://<collection host>/qr<URL>`. Note the collection host, so with
   `SHORTURL_HOSTNAME` or an alias set, the code points at that host.
3. `clickCount` is **not** incremented.

The QR image uses error correction M, a four module quiet zone, and four
pixels per module, and is sent with `Cache-Control: public, max-age=86400`.

A `qr.` host request for a missing slug follows the not-found flow, so the
visitor is redirected to `/404/...` on the `qr.` host.

## Counters

On a hit, before any mode runs:

- Always: `clickCount` +1, `clickLast` = visit time.
- QR scan: also `qrUseCount` +1, `qrUseLast` = visit time.

Path rule matches count `matchCount` and `matchLast` on the rule document.

Counts are batched. Each instance sums them in memory and writes one
increment per document, with a 1 second timeout, from the first request
that arrives at least 5 seconds after its previous flush. An idle instance
holds its counts until its next request or its shutdown, when everything
pending is written. The `*Last` fields hold the time of the latest visit,
taken from the instance clock, not the write time. Because instances flush
independently, an idle instance writing an old visit at shutdown can set a
`*Last` field earlier than a visit another instance already recorded.

A write that Firestore rejected unapplied (contention, quota) is retried on
the next flush. Any other failure, including a timeout, is logged and
dropped, because a timed-out write may have committed and retrying it would
double count. A counter failure never affects the response.

## Modes

Checked in this order. Exactly one runs.

### Passthrough (`passthrough` truthy)

Reverse proxy to `destination`.

- `passQueryString` truthy and query non-empty: append the query with `?`
  or `&` depending on whether `destination` already contains `?`.
- Request headers copied from the visitor when present: `Accept` (default
  `*/*`), `Accept-Encoding`, `Accept-Language`, `Cache-Control`, `Pragma`,
  `Authorization`, `User-Agent`, `Content-Type`, `X-Forwarded-For` (default
  the visitor's IP), and the seven `X-Goog-*` push notification headers
  (`Channel-ID`, `Channel-Token`, `Channel-Expiration`, `Resource-ID`,
  `Resource-URI`, `Resource-State`, `Message-Number`). No other request
  headers are forwarded, so cookies are not.
- Added request headers: `X-Passthrough-Domain` (collection host),
  `X-Passthrough-Slug`, `X-ShortUrl-Ver` (build version).
- Method is forwarded. Body is forwarded for every method except GET and
  HEAD. It is read into memory first, up to 10 MiB; a larger body responds
  `413 Request Entity Too Large` without calling the upstream, and a body
  read error responds `400 Bad Request`. Bodies buffered at once on one
  instance are capped at 64 MiB in total; a body that does not fit responds
  `503 Service Unavailable` with `Retry-After: 1`. The upstream call times
  out after 30 seconds.
- Upstream redirects are followed, up to 10. GET and HEAD keep their
  method. Other methods follow a 301, 302, or 303 as a GET without the
  body, and a 307 or 308 with the same method and body.
- Response: `X-ShortUrl-Ver` is always set. If the upstream status is not
  2xx and `passthroughAnyStatus` is falsy, respond
  `500 Internal Server Error`. Otherwise copy only
  `Access-Control-Allow-Origin`, `Cache-Control`, `Content-Encoding`,
  `Content-Type`, `Pragma`, then the upstream status and body. Any upstream
  error also produces a 500.
- The body is streamed as received. When the visitor sent
  `Accept-Encoding`, a compressed upstream body reaches the visitor
  compressed, with the matching `Content-Encoding`.

### Path rules (`usePaths` truthy)

1. Read every document in the `paths` subcollection, in document ID order.
   A read error responds 500.
2. For each, compile `pattern` as a Go RE2 expression and run it against
   the remainder. The first matching rule wins. A pattern that does not
   compile is logged and skipped.
3. On a match, count `match` on the rule (see Counters), then take the
   rule's `destination` and replace every `${name}` with that named group's
   value. A group that did not participate substitutes as the empty string.
   A rule with no named groups uses its destination verbatim.
4. If no rule matched, use the link's own `destination`.
5. Continue with **Redirect** below using the chosen destination.

`(?<name>...)` named groups work as in JavaScript. Lookahead, lookbehind,
and backreferences do not exist in RE2; `cmd/pathaudit` reports stored
rules that use them.

### Redirect (default)

1. `passQueryString` as in passthrough.
2. An empty destination takes the not-found flow.
3. `frame` truthy: respond `200 text/html` with an HTML5 page whose
   `<title>` and iframe `title` are the `frame` value, an iframe whose
   `src` is the destination as normalized by Go's `html/template` (an unsafe
   scheme such as `javascript:` becomes `#ZgotmplZ`), and a script that calls
   `history.replaceState` with `https://<collection host><URL>` so the
   address bar shows the short link.
4. Otherwise respond with `Location: destination` and status `statusCode`
   when it is a number from 200 to 599, else 307. The destination is not
   resolved against the request path, so a relative destination is sent as
   written.

## Response headers not set

No `Cache-Control` is set on redirects or frame pages. Firebase Hosting
therefore treats them as uncacheable, which is what keeps click counting
accurate.

## Static files and hosting

`firebase.json` serves `public/` first. `/` redirects (302) to `/apex`, so
the `apex` slug is the home page link. `/404.html` is static. Everything
else rewrites to the Cloud Run service. Hosting sets `X-Forwarded-Host` to
the host the visitor used and `X-Forwarded-For` to the visitor's IP.

## Known quirks kept on purpose

- Slugs are case-insensitive on lookup but the stored document ID must be
  lowercase.
- Flag fields (`passthrough`, `usePaths`, and so on) use JavaScript
  truthiness, so a string `"true"` or a number `1` counts as set. Existing
  documents rely on this.
- A `statusCode` of 301 or 308 lets browsers cache the redirect, after
  which `clickCount` stops increasing for repeat visitors.
- Passthrough forwards `Authorization`. Only point passthrough links at
  destinations you trust with the visitor's credentials.
- Anyone who reaches the backend directly, bypassing Hosting, can set
  `X-Forwarded-Host` to any value and read any collection's redirect
  destinations. All of that data is public by design, and the backend holds
  nothing else, so this is accepted.
