# Decisions

## Initial documentation scaffold

- **Decision:** Use `AGENTS.md` as the shared source of coding-agent instructions; keep `CLAUDE.md` as a short pointer to it.
- **Reason:** This keeps guidance consistent across Codex, Claude Code, and OpenCode without duplicating it.
- **Scope:** Shared agent documentation.

## 2026-10-19 — Application architecture and scope

- **Decision:** Go backend, React/TypeScript frontend, SQLite, one Docker container on Debian, LAN access and one password-protected administrator; up to 100 proxies.
- **Reason:** Compact deployment without a separate database or broker for this scale.
- **Decision:** HTTP, HTTPS and SOCKS5 with optional authentication; individual and pasted-list import; independent user-defined availability/speed intervals.
- **Decision:** English-only project content, including UI, documentation, source comments, tests and messages. No localization system. Development conversations with agents are Russian; user-entered proxy names/tags remain unrestricted.
- **Details:** `docs/superpowers/specs/2026-10-19-proxy-checker-design.md`.

## 2026-10-20 — Task 1 technical choices

- **Decision:** Go 1.27.0 (installed with user approval via winget) and pure-Go `modernc.org/sqlite` v1.60.1 with `CGO_ENABLED=0`; module name `proxy-checker`.
- **Decision:** Timestamps are Unix milliseconds in UTC, durations microseconds. One `database/sql` pool with DSN pragmas (WAL, foreign_keys, busy_timeout 5000) plus a write mutex; each migration runs in one transaction with its version record.
- **Decision:** Credentialed proxies are rejected by the store (`ErrAuthUnsupported`) until encrypted storage lands in Task 2/3; `credential_digest` is reserved for duplicate identity.
- **Decision:** Default listen address is `127.0.0.1:8080` (safe by default); the container will set it explicitly in Task 10.

## 2026-10-20 — Task 2 security choices

- **Decision:** The CSRF token is HMAC-SHA256 keyed by the session token, so it is not stored and only the session holder can compute it. Only SHA-256 digests of session tokens are stored.
- **Decision:** Login throttling uses the TCP peer address only (never `X-Forwarded-For`); denied attempts are not recorded; tracked peers are capped and the limiter fails closed when full.
- **Decision:** Protected routes return 503 `not_initialized` until the administrator password is set via the CLI; `GET /api/auth/session` stays public and reports `authenticated: false`.
- **Decision:** `secrets.Load` creates the key atomically; the app refuses to start without the key when encrypted records exist, so a missing key is never silently regenerated.
- **Decision:** Mutations without an `Origin` header are allowed (non-browser clients) but still need the CSRF token; a present, non-matching `Origin` is rejected.

## 2026-10-20 — Task 3 proxy management choices

- **Decision:** Credentials: usernames are stored in plain text, passwords encrypted with AES-GCM. Duplicate identity uses `credential_digest`, an HMAC-SHA256 of username and password keyed by a key derived from the secrets key (separate from the encryption key); no plain password hash is stored. Losing the key makes stored credentials unreadable, which the startup guard already reports.
- **Decision:** The store limits proxies to 100 inside the write transaction; imports are all-or-nothing and report `ErrLimit` or `ErrConflict`.
- **Decision:** API field semantics: `name`/`tags`/schedules omitted on PUT keep the stored value; schedule `null` clears the override (inherit); `password` omitted keeps, a string (even empty) replaces, `null` is rejected; `auth_enabled:false` removes credentials; credentials without authentication enabled are rejected. A blank or omitted name on create defaults to `host:port`.
- **Decision:** Import accepts only `scheme://[user:password@]host:port` and `host:port`; formats like `host:port:user:password` and bare `user:pass@host:port` are rejected as ambiguous. At most 1000 non-blank lines and 256 KiB per request; previews mask passwords and fully redact user-info of rejected lines.
- **Decision:** Plain IPv6 hosts are stored unbracketed in canonical form; hostnames are lowercase without a trailing dot; non-ASCII hostnames are rejected (no IDNA).

## 2026-10-20 — Task 4 transport and availability choices

- **Decision:** Own CONNECT and SOCKS5 dialers (no extra dependency) behind one `http.Transport.DialContext`, so there is exactly one way to open a connection and it always goes to the proxy. `Transport.Proxy` is nil (environment variables ignored), redirects and compression are off, keep-alives are off, TLS is verified for the proxy (HTTPS proxies) and the target. Test CAs are injected only through `proxy.WithRootCAs`.
- **Decision:** SOCKS5 sends hostnames unresolved (remote DNS); the reply is read byte-exact so tunnel bytes are never consumed; CONNECT keeps bytes buffered after the reply headers.
- **Decision (state rules):** explicit proxy failures are definite and give `down` without trying the fallback provider: TCP connect refused/timeout, proxy TLS failure, 401/403/407 or SOCKS auth failure, garbage proxy replies. Tunnel refusals (502 and similar, SOCKS host/network unreachable), timeouts after the TCP connect, target TLS errors and provider errors (429, 4xx/5xx, invalid body, non-public exit IP) are not attributable to the proxy: if both providers fail the result is `indeterminate`. Cancellation is reported as `canceled` and never as `down`.
- **Decision:** Exit IPs must be global unicast, not private and not CGNAT (100.64.0.0/10); bodies are capped at 4096 bytes; only HTTP 200 counts. Primary provider `https://api64.ipify.org?format=json`, fallback `https://checkip.amazonaws.com/`. Results record the provider name as `endpoint`, the check time and the duration of the successful request (total elapsed time for failures).

## 2026-10-20 — Task 5 geolocation choices

- **Decision:** `internal/geo.Service` (not listed in the plan's file map) owns the 24 h cache, request coalescing per IP, the 1000 attempts per UTC day budget and provider pauses. Persistent state lives in `geo_cache`, the single-row `geo_state` (migration 002) and the encrypted `geo.api_key` setting.
- **Decision:** An attempt is counted when a request starts, even if it fails. A 429 pauses until `Retry-After` (seconds or date) or an exponential backoff of 1 minute doubling to 1 hour; 401/403 pause until the key changes (usage is kept); other failures (network, 5xx, invalid body) back off the same way so a broken provider is not hammered. Without a key nothing is sent and the view says `not_configured`; fresh cached data stays usable, expired data is shown as stale.
- **Decision:** Lookups are direct POSTs with the key in the JSON body only; the geolocation client ignores environment proxies and redirects, uses a 5 s timeout and 64 KiB response cap, and requires the response `ip` to match the request. Only public addresses are looked up.
- **Decision:** Geo results never feed back into availability: the geo package has no access to availability writes.

## 2026-10-20 — Task 6 throughput choices

- **Decision:** Measurement method `app-stream-v1`: a 256 KiB warmup then one timed sample (default 8 MiB, 1 byte to 64 MiB) per direction, download first. Download timing runs from the first body read to the end of the body; upload timing runs from the first body read by the transport to the arrival of the confirming response. Memory use is independent of the sample size (32 KiB buffers, generated ChaCha8 upload data with a known Content-Length).
- **Decision:** A rate exists only for a complete, verified sample (exact download length; `cf-meta-upload-bytes` equal to the sent size). A completed sample whose duration the clock cannot resolve gets no rate and low confidence, not a failure. Samples under 250 ms are low confidence. A failed direction keeps its bytes and error and never shows zero; the other direction is still reported. `TotalBytes` counts every byte moved, warmups and failures included.
- **Decision:** Provider-wide cooldown is stored in `provider_pause` (migration 003): 429 pauses by `Retry-After` or backoff, 403 pauses until `ClearProviderPause` (the future resume endpoint). No traffic is generated while paused, and an upload is skipped if the download just started a pause. 5xx and timeouts do not pause.
- **Decision:** New failure codes beyond the spec's list: `provider_forbidden` and `invalid_config` (speed and geo views only).

## 2026-10-20 — Task 7 jobs, schedules and incidents

- **Decision:** Global settings are one JSON document (`app.settings`, validated against the specified bounds; defaults: both schedules disabled, 5 min / 6 h, 5 availability workers, 10 s timeout, down threshold 3, retention 30 days). Intervals are stored in seconds.
- **Decision:** Job claiming is one transaction: the oldest due pending job whose proxy has no running job (this is what keeps the two check kinds from overlapping). A unique partial index allows one active job per proxy and kind, which also bounds the queue. Manual enqueue promotes a scheduled pending job (source becomes manual, due now) so disabling schedules never drops it. Scheduled enqueue re-checks `next_*_at` inside its transaction, because a check finishing after the scheduler took its snapshot would otherwise be queued again (found by the Linux race run).
- **Decision:** Next run = completion time + effective interval for scheduled and manual runs alike; with no enabled schedule it stays unset. A schedule that is newly enabled or overdue by more than a minute starts after 0-30 s of jitter; missed intervals are never replayed. Disabling removes pending scheduled jobs and clears next runs; manual jobs stay.
- **Decision:** One availability worker pool (size read at start, so a changed concurrency applies after a restart) and exactly one speed worker. While the speed provider is paused, speed jobs stay pending instead of being claimed. User cancel finishes a job as `canceled`, shutdown as `interrupted`; neither stores a result nor touches incident state. On startup, jobs left running become `interrupted`.
- **Decision:** Incident state: failure streaks live in `proxy_state` (migration 004); `RecordAvailabilityOutcome` writes the streak, incident, event and outbox row in one transaction. Only definite `down` results count; up closes an open incident; indeterminate, unknown, canceled and interrupted change nothing. A database unique index allows one open incident per proxy and one outbox row per event. New outbox rows are due 10 s after the event so changes close together can be grouped.
- **Decision:** `POST /api/jobs` returns 202 with `new`, `existing` and `missing_proxy_ids`; cancelling a running job returns 202 (the runner stops it), a pending one 200, a finished one 409. Deleting a proxy cancels its jobs first. Exit-IP geolocation is requested in the background after an `up` result and never delays or changes it.

## 2026-10-20 — Task 8 Telegram and events

- **Decision:** The Telegram client posts plain text only (no `parse_mode`) and validates the token (`digits:[A-Za-z0-9_-]{20,100}`) and chat ID before building the URL, which rules out path or query injection. Because the token is part of the URL, errors are classified into short codes (`timeout`, `network`, `rate_limited`, `forbidden`, ...) and the transport error text is never forwarded, stored or logged.
- **Decision:** Delivery is at-least-once. A new outbox row waits 10 s so nearby changes are grouped; due items are sent as one message per up to 4000 characters, each item exactly one sanitized line (control and invisible formatting characters removed, 400 characters per line). A failing message ends the run: its items are rescheduled after 30/60/120/240 s (Telegram `retry_after` replaces the fixed delay) and the fifth failed attempt is final but stays visible; later messages are untouched. Without a configured token and chat nothing is sent and no attempt is consumed. Shutdown during a send is not a failed attempt.
- **Decision:** `PUT /api/settings` is partial and validated as a whole (nothing is applied if any field is invalid). Secrets are write-only: omitted keeps, a string replaces, an empty string clears, null is rejected; responses only carry `has_token` and `has_key`. Monitoring schedule changes are applied immediately through the job service. `POST /api/settings/telegram/test` only queues a message and returns its ID; delivery is reported through the delivery summary. `POST /api/settings/speed/resume` clears the speed provider pause.
- **Decision:** `GET /api/events` is newest first with an opaque `(created_at, id)` cursor, limit 1-200 (default 50), optional `proxy_id` and repeated `kind` filters, and each event carries its delivery state (`none`, `pending`, `delivered`, `failed`).

## 2026-10-20 — Task 10 operations

- **Decision:** One container, uid 10001, read-only root, `/tmp` tmpfs, all capabilities dropped, named volume `/data`. The port is published only on `${LAN_IP}` (required) so the service is never exposed on all interfaces by accident. Session cookies stay non-`Secure` by default (plain LAN HTTP); `SESSION_SECURE=true` is for TLS-terminating proxies. Documented in `docs/operations.md`.
- **Decision:** Backup is a `tar.gz` of a `VACUUM INTO` snapshot plus `secrets.key`, created while running, never served over HTTP. Restore needs a stopped server, validates the archive (exactly two regular files, 32-byte key, integrity check) before touching `/data`, requires `--force` over existing data, and accepts `--input -` (stdin) because a 0600 root-owned archive is unreadable by uid 10001.
- **Decision:** Retention runs hourly in batches of 1000 and always keeps the latest result per proxy, open incidents and undelivered notifications.
- **Open:** base images are pinned by tag, not digest; pin digests for production builds.

## 2026-10-20 — Task 9 web interface

- **Decision:** React 19 + strict TypeScript + Vite 7, react-router 7, Vitest 3, Playwright 1.56. I chose proven majors instead of the newest releases (the registry also offered newer ones) and pinned exact versions in `web/package.json` plus `package-lock.json`. No UI component library, no chart library (small SVG charts that break lines at gaps), no i18n library, system font stack, flags from the MIT `flag-icons` package bundled locally (attribution in `docs/THIRD_PARTY.md` and on the Settings page).
- **Decision:** The backend gained the data the screens need: `GET /api/proxies` (and detail) now carry `status`, `speed`, `geo`, `uptime` (definite checks in the last 24 h with the sample count), `active_jobs` and `next_runs`; `GET /api/proxies/{id}/history` (cursor paging, 1-200) and `/chart` (at most 500 buckets, empty intervals stay as gaps). Geolocation in views comes from the cache only (`Peek`), never from a request.
- **Decision:** Visual language per the brief: neutral macOS-like surfaces, system fonts, one blue accent, states always written as words plus a dot, light/dark/system theme via CSS variables and `data-theme`, sidebar on desktop and a top bar with stacked cards below 860 px. Polling is every 5 s only while the tab is visible, never overlapping, aborted on unmount, and restarted immediately when a filter or route changes.
- **Decision:** Secrets are write-only in the UI: password, bot token and API key inputs are never prefilled; an omitted password keeps the stored one, "Use an empty password" is an explicit choice, and removal is an explicit empty value. A missing speed is a dash with the reason, never zero; "Not configured" (no geo key) and "Unknown" (key set, no data) are different.
- **Decision:** The Go binary embeds `internal/webui/dist` (built by `node scripts/build-web.mjs`; only `.gitkeep` is tracked). Non-file paths fall back to `index.html`, `/api` and `/healthz` never do, and a strict CSP (`default-src 'self'`, no inline scripts) with `X-Frame-Options: DENY` is sent on every response.
- **Decision:** Browser tests run on a local stand (`internal/e2e`, build tag `e2e`, started by `scripts/e2e-server.mjs`): the real application with fixture HTTP/HTTPS/SOCKS5 proxies, fake ipify/AWS/Cloudflare/ipapi/Telegram services and a local CA, wired through `app.Options`. They reuse one login because the server (correctly) limits sign-ins to five per minute per client.

## 2026-10-19 — Public providers

- **Decision:** ipify for observed exit IP, AWS checkip as independent fallback, keyed ipapi.is with a 24h shared cache, Cloudflare as a replaceable best-effort Download/Upload adapter.
- **Reason:** Direct probes passed; ipapi.is documents the required geo/ASN data and 1000 daily free keyed lookups. Cloudflare accepted bounded exact-size bidirectional transfers.
- **Limits:** Real proxy/Debian operation and keyed geo were not tested. No hosted Cloudflare backend-automation quota/guarantee was established. Never evade provider limits with other proxy exits.
- **Evidence:** `docs/research/2026-10-19-public-services.md`.
