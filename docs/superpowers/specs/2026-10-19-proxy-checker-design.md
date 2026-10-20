# Proxy Checker Design Specification

## Scope and approval

Personal monitoring of up to 100 proxies on a local network. One Docker container on Debian: Go backend, React + TypeScript frontend, SQLite on a persistent local volume. One password-protected administrator. Only external public test services; a free geolocation API key is acceptable.

The user approved the feature scope and architecture in chat and explicitly requires English-only UI, documentation and source code, including comments, tests and messages. No language selector, translation catalogs or i18n library. User-entered names/tags are not restricted by this language rule. Development conversations with agents are in Russian; Russian text is not part of project deliverables.

The technical defaults below are implementation recommendations recorded for review, not additional claims of user approval. No application code exists yet.

## Proxy management

- HTTP with CONNECT to HTTPS targets; HTTPS with TLS to the proxy and CONNECT; SOCKS5 with target DNS resolved remotely. Validate both proxy and target TLS certificates.
- Every protocol supports authenticated and unauthenticated connections. Accept IPv4, hostnames and bracketed IPv6; ports 1–65535.
- Individual form: name, protocol, host, port, optional tags and authentication toggle. When authentication is enabled, username is required; an empty password is permitted.
- Pasted list: `scheme://user:password@host:port`, `scheme://host:port`, or `host:port` with a form-selected default protocol. Credentials use URL percent-encoding. Do not guess ambiguous `host:port:user:password` formats.
- Preview invalid/duplicate rows and save selected valid rows atomically. Reject an import exceeding 100 total records without partially saving it.
- Duplicate identity includes normalized protocol/host/port and credentials. Different credentials at one endpoint are allowed. Never return stored secrets in API responses or error messages.

## Checks and state

- Manual checks for one, selected or all proxies; availability and speed are independent job types. Deduplicate pending/running jobs by proxy and kind. Different check kinds must not run simultaneously for the same proxy.
- Availability sends an HTTPS request through the selected proxy and validates a public exit IP. Primary provider: ipify; fallback: AWS checkip. Latency means complete HTTP check duration, not ICMP RTT.
- No direct connection fallback when the proxy fails; ignore environment proxy settings for test transports. Private proxy endpoint addresses are permitted for LAN use.
- State: unknown, up, down, indeterminate. Separate reason codes: connection_refused, timeout, auth_required, auth_failed, proxy_tls, target_tls, provider_rate_limited, provider_unavailable, invalid_response, canceled, interrupted.
- A valid response from either target establishes up. Explicit proxy connection/authentication failures establish down. If both targets fail and the proxy cannot be distinguished from route/provider failure, record indeterminate. Track provider/network health separately; direct diagnostic requests can never establish proxy success.
- Record the endpoint and time of each exit-IP observation. Rotating proxies may expose different IPs to different connections.
- Speed checks perform download then upload through the selected proxy, with one global speed worker. Never generate public speed-test traffic from the panel browser.
- Proposed method: 256 KiB warmup per direction, then one 8 MiB sample per direction. Configurable sample size 1–64 MiB and direction timeout 1–120 s (default 30 s). Include warmup and failed attempts in traffic accounting. Disable compression and redirects; stream data without full-payload buffering and send a known upload Content-Length.
- Download rate: `bytes * 8 / seconds / 1e6`, measured monotonically from first body read to completion. Upload duration runs from body transmission start through successful response confirmation, including confirmation overhead. Persist raw byte counts/durations, endpoint and method. Label as approximate speed; samples shorter than 250 ms are low confidence, not peak bandwidth.
- Verify Cloudflare's cf-meta-upload-bytes when completing uploads. A mismatch is invalid_response. Timed-out/canceled samples retain bytes and error but no successful Mbps value. Keep a successful direction if the other fails.

## Scheduling and recovery

- User-defined intervals from 1–10080 minutes; UI supports minutes/hours. Independent enable switches for availability/speed; both start disabled. Suggested values before enabling: 5 minutes / 6 hours.
- Global schedules with per-proxy overrides. Bounded pending queue and unique active proxy/kind jobs. Availability concurrency defaults to 5, configurable 1–20; timeout defaults to 10 s, configurable 1–60 s.
- Store timestamps in UTC. Persist next_run_at. Spread overdue work with small jitter after downtime; do not replay missed intervals. Compute next run from completion; manual runs also advance their kind's next due time.
- Restart changes running jobs to interrupted without recording a proxy failure. Disabling schedules removes pending scheduled jobs but preserves manual jobs. Deleting a proxy cancels its jobs.
- Provider 429 pauses use Retry-After (seconds or HTTP-date); otherwise exponential backoff from 60 s up to 1 hour. Cloudflare 403 pauses speed until explicit resume. Do not bypass limits with other proxy exits.

## Geo enrichment

Use keyed ipapi.is directly from the backend for the observed public IP. Cache by IP for 24 hours, coalesce concurrent lookups and cap local attempts at 1000 per UTC day. Lower observed quotas/429 take priority. Without a key, show Not configured instead of silently spending anonymous quota.

Show country flag, country, city, ASN and organization; missing values are Unknown. Refresh failure retains dated stale data. Bundle flag SVGs locally by ISO code and attribute their license. Preserve provider-returned English geographic names. Geo failure never changes proxy availability.

## Administration, persistence and secrets

- One administrator. Set/reset initial password with a container command using stdin/TTY, not URL/command-line secret arguments; minimum 12 characters. Salted Argon2id; no open web registration.
- Opaque 256-bit session token; store its digest only. HttpOnly/SameSite=Strict cookies, Secure under HTTPS; document LAN HTTP explicitly. Sessions expire after 24 hours. Logout revokes its session; password change/reset revoke all sessions. CSRF and Origin checks for mutations; bounded login rate limiting.
- SQLite WAL, foreign_keys, 5 s busy timeout, serialized short writes. No network filesystem storage or transactions held across network calls.
- Tables: proxies, settings, admin, sessions, jobs, availability_results, speed_results, geo_cache, events, notification_outbox, schema_migrations. Index history by (proxy_id, checked_at), jobs by due/state and unique active proxy/kind.
- Encrypt proxy passwords, Telegram token and geo key with AES-256-GCM. Persist a separate 0600 `/data/secrets.key`; backups must include this key. API DTOs return has_secret booleans rather than secrets/ciphertext.
- Default history retention 30 days, configurable 1–365. Delete in bounded batches while preserving latest current state and incident tracking. Consistent SQLite backup, not raw copying a live db without its WAL.

## Telegram

Use a separate direct backend client to Telegram Bot API, independent of checked proxies. Settings: bot token, chat ID, test notification and delivery status.

Default down threshold: 3 consecutive definite failures, configurable 1–10. Indeterminate neither counts as a failure nor recovers a down incident. Recovery follows a recorded down incident and successful availability result. Persist incident/event/outbox atomically, group changes over 10 s. Maximum 5 delivery attempts with delays 30, 60, 120, 240 s; Telegram retry_after takes priority. Delivery is at-least-once; an ambiguous lost response can cause a duplicate. Optional country/IP-change notifications and daily summaries are outside the first release.

## UI and operations

- English only: labels, validation, errors, accessible names, comments, tests and documentation. Use en-US numeric/date formatting with the local timezone. Units: Mbps, ms, MiB.
- Restrained Apple/macOS-inspired styling: neutral surfaces, system sans-serif typography, subtle rounding, clear textual states; local resources without CDN fonts. Light/dark/system theme, keyboard accessibility, reduced motion and 375 px mobile layout.
- Screens: login; searchable/filterable proxy table and bulk actions; add/import; proxy details with history/charts; events; settings. Missing speed is not zero. Mark stale geo and low-confidence samples explicitly.
- Serve the compiled frontend from Go. Poll every 5 s only in visible tabs. Paginate history, limit chart points and retain gaps. Uptime is the proportion of definite sampled checks, showing sample count, not an exact downtime-duration claim.
- Multi-stage build, one nonroot Debian slim runtime with CA certificates. Writable local /data volume. Compose binds to explicitly supplied LAN_IP, not 0.0.0.0; restart unless-stopped, local /healthz, graceful SIGTERM. No privileged mode or Docker socket mount.
- Health indicates app/DB readiness, not public-service uptime. Structured stdout logs with Docker rotation and database events; no raw upstream bodies or full credential-bearing proxy URLs.

## Acceptance

Normal tests are offline: local HTTP/HTTPS/SOCKS5 fixtures, fake clock and fake Telegram. Cover all three protocols with/without auth, wrong credentials, TLS, timeouts, no direct bypass, quotas, scheduler recovery and delivery retries. Browser QA covers desktop/mobile and both themes, including import, checks, charts and settings. Restore backups on a disposable volume.

Separate live acceptance on Debian requires supplied real proxies, geo key and Telegram credentials. Direct Windows probes have passed, but do not verify those integrations. See the [probe report](../../research/2026-10-19-public-services.md). Cloudflare remains a replaceable best-effort adapter without an established unlimited backend API guarantee.
