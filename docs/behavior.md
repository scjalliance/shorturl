# Request handling behavior

This is the exact behavior of the current `redirV2` Cloud Function, written
down so a replacement can be checked against it. Line references point at
`functions/index.js`. Where the Go port intentionally differs, the
difference is marked **port:** and also listed in the design spec.

## Request parsing

1. **Host.** `request.hostname` (Express, so `X-Forwarded-Host` wins over
   `Host`, and the port is dropped). A leading `qr.` is removed to get the
   collection name. If a `qr.` was removed, this is a **QR create** request.
   The environment variable `SHORTURL_HOSTNAME`, when set, replaces the
   collection name but does not affect QR detection. (Lines 14 to 16.)
2. **URL.** `request.url` is the raw path and query, not URL decoded. A
   leading `/qr/` (any number of slashes on either side of `qr`) collapses
   to `/`. If that changed anything, this is a **QR scan** request. (Lines
   18 to 19.)
3. **Slug.** Remove one leading `/`, take everything up to the first `/` or
   `?`, lowercase it. Not URL decoded, so `/Foo%20Bar` looks up
   `foo%20bar`. `/` and `//foo` produce an empty slug. (Line 21.)
4. **Query.** Everything after the first `?`, without the `?`. (Lines 22 to
   27.)
5. **Remainder** (path rules only). URL with `^/[^/]+/` removed. If there
   is no second slash the remainder is the whole URL, leading slash and
   query string included. Because `[^/]` matches `?`, a URL like
   `/slug?x=1/foo` has remainder `foo`. (Line 149.)

## Lookup

Collection = host, document = slug. (Line 42.)

- Empty slug, or a slug Firestore rejects as a document ID (`.`, `..`,
  `__name__` style, over 1500 bytes), throws synchronously and the visitor
  gets a 500. **port:** these take the not-found flow.
- A Firestore error rejects the promise chain, which has no catch. The
  request hangs until the function timeout. **port:** 500.

## Not found

If the document does not exist (lines 177 to 183):

- Slug is `404`: 302 to `/404.html`, the static page in `public/`.
- Otherwise: 302 to `/404` followed by the URL, for example
  `/missing?x=1` goes to `/404/missing?x=1`. That second request looks up
  the `404` document under the same host, so each host can have its own
  404 link with `usePaths` rules or a plain destination.

No counters are touched for a missing document.

## QR create (request on a `qr.` host)

If the document exists and the slug is not `404` (lines 47 to 58):

1. Increment `qrCreateCount`, set `qrCreateLast`. This write is awaited.
2. Respond `200 image/png` with a QR code for
   `https://<collection host>/qr<URL>`. Note the collection host, so with
   `SHORTURL_HOSTNAME` set the code points at the override host.
3. `clickCount` is **not** incremented.

The QR image uses the node `qrcode` defaults: error correction M, four
module quiet zone, four pixels per module. No `Cache-Control` is set.
**port:** `Cache-Control: public, max-age=86400`.

A `qr.` host request for a missing slug follows the not-found flow, so the
visitor is redirected to `/404/...` on the `qr.` host.

## Counters on a hit

Before any mode runs (lines 60 to 72), fire and forget, not awaited, no
error handling:

- Always: `clickCount` +1, `clickLast` = server time.
- QR scan: also `qrUseCount` +1, `qrUseLast` = server time.

**port:** the same fields are written synchronously with a 500 ms timeout;
failure is logged and the redirect still happens.

## Modes

Checked in this order. Exactly one runs.

### Passthrough (`passthrough` truthy, lines 74 to 146)

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
  HEAD.
- Redirects from the upstream are followed.
- Response: `X-ShortUrl-Ver` is always set. If the upstream status is not
  2xx and `passthroughAnyStatus` is falsy, respond
  `500 Internal Server Error`. Otherwise copy only
  `Access-Control-Allow-Origin`, `Cache-Control`, `Content-Type`, `Pragma`,
  then the upstream status and the whole body. Any upstream error also
  produces a 500.
- The body is fully buffered in memory. Node's `fetch` decompresses gzip
  responses and the `Content-Encoding` header is not copied, so the visitor
  always receives an uncompressed body. **port:** streams the body and also
  copies `Content-Encoding`, so the visitor may receive a compressed body
  with the matching header.

### Path rules (`usePaths` truthy, lines 148 to 171)

1. Read every document in the `paths` subcollection, in document ID order.
2. For each, build `RegExp(pattern)` (no flags) and run it against the
   remainder. The first document whose pattern matches **and has at least
   one named capture group** wins. A pattern with no named groups never
   matches, which silently disables every exact-match rule such as
   `^file\.zip$`. **port:** a matching rule with no named groups wins and
   its destination is used verbatim. A pattern that fails to compile throws
   and hangs the request. **port:** logged and skipped.
3. On a match, increment `matchCount` and set `matchLast` on the rule (fire
   and forget), then take the rule's `destination` and replace every
   `${name}` with the group's value. A named group that did not participate
   substitutes as `,` because of how `Array.join(undefined)` behaves.
   **port:** substitutes as the empty string.
4. If no rule matched, use the link's own `destination`.
5. Continue with **Redirect** below using the chosen destination.

The pattern dialect is JavaScript `RegExp`. **port:** Go RE2. The
`(?<name>...)` syntax works in both. Lookahead, lookbehind, and
backreferences do not exist in RE2; run `cmd/pathaudit` before cutover.

### Redirect (default, lines 29 to 40)

1. `passQueryString` as in passthrough.
2. `frame` truthy: respond `200 text/html` with a page whose `<title>` and
   iframe `title` are the `frame` value, an iframe whose `src` is
   `encodeURI(destination)`, and a script that calls
   `history.replaceState` with `https://<collection host><URL>` so the
   address bar shows the short link. The page has no doctype and the iframe
   tag is written as `<iframe .../>`. **port:** valid HTML5 with a closing
   tag and the same content.
3. Otherwise respond with `Location: destination` and status `statusCode`
   if truthy, else 307. Express does not resolve the destination against
   the request path, so a relative destination is sent as written.
   A missing `destination` redirects to the literal string `undefined`.
   **port:** empty destination takes the not-found flow.

## Response headers not set

No `Cache-Control` is set on redirects or frame pages. Firebase Hosting
therefore treats them as uncacheable, which is what keeps click counting
accurate.

## Static files and hosting

`firebase.json` serves `public/` first. `/` redirects (302) to `/apex`, so
the `apex` slug is the home page link. `/404.html` is static. Everything
else rewrites to the function. Hosting sets `X-Forwarded-Host` to the host
the visitor used and `X-Forwarded-For` to the visitor's IP.

## Known quirks kept on purpose

- Slugs are case-insensitive on lookup but the stored document ID must be
  lowercase.
- A `statusCode` of 301 or 308 lets browsers cache the redirect, after
  which `clickCount` stops increasing for repeat visitors.
- Passthrough forwards `Authorization`. Only point passthrough links at
  destinations you trust with the visitor's credentials.
- Anyone who reaches the backend directly, bypassing Hosting, can set
  `X-Forwarded-Host` to any value and read any collection's redirect
  destinations. All of that data is public by design, and the backend holds
  nothing else, so this is accepted.
