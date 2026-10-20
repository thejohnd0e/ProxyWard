# Project status

## Completed

- Shared agent documentation scaffold; agreed scope and architecture; specification and ten-task implementation plan.
- 2026-10-19 bounded public-service probes (see `docs/research/2026-10-19-public-services.md`).
- 2026-10-20 **Task 1: Persistent foundation** (Go 1.27.0, `modernc.org/sqlite` v1.60.1, config, SQLite store with embedded atomic migrations, `/healthz`, graceful shutdown).
- 2026-10-20 **Task 2: Admin sessions and encrypted secrets** (race run passed on the Linux VM):
  - `internal/auth`: Argon2id (64 MiB, 3 iterations, p=1, 32-byte key) with bounded parsing of stored hashes, constant-time verify, 12-character minimum; opaque 256-bit session tokens stored as SHA-256 digests; CSRF token derived by HMAC from the session token; Origin check; login limiter (5/min per TCP peer, 20/min global, at most 4096 tracked peers); `Require` middleware.
  - `internal/secrets`: persistent 32-byte `secrets.key` created atomically (temp file plus hard link, 0600), AES-256-GCM `Seal`/`Open` with random nonces.
  - `internal/store`: `auth.go` (admin hash, sessions, revoke), `settings.go` (settings with a secret flag, `HasEncryptedRecords`).
  - `internal/httpapi`: `POST /api/auth/login`, `GET /api/auth/session`, `POST /api/auth/logout`, `PUT /api/auth/password`, JSON 404 for unknown `/api/` routes. `internal/apierr` writes the JSON error format.
  - `internal/app`: refuses to create a new key when encrypted records exist and the key is missing; `SetAdminPassword`.
  - CLI: `proxyward admin set-password` reads a prompt (TTY, no echo, confirmed) or the first stdin line; the password is never an argument.
  - `internal/testkit`: fake clock.

- 2026-10-20 **Task 3: Proxy management and list import**:
  - `internal/proxy`: `ParseLine`, `ParseList`/`Preview`, `NormalizeHost`, `ValidateName`, `ValidateTags`, masking. Typed `ParseError` codes never contain input text.
  - `internal/store/proxy.go`: encrypted credentials, keyed duplicate fingerprint, `InsertProxy`/`ImportProxies` (atomic, max 100 enforced in the transaction), `UpdateProxy` with `CredentialUpdate`, `DeleteProxy` (cascades jobs/results/incidents, keeps events), `ProxyExists`, `CountProxies`, schedule override columns.
  - `internal/httpapi/proxies.go`: `GET/POST /api/proxies`, `GET/PUT/DELETE /api/proxies/{id}`, `POST /api/proxies/import/preview`, `POST /api/proxies/import`; secret-free DTOs (`has_password`), 256 KiB body cap (413), field-level 422 errors, 409 duplicates, 422 `record_limit`.
  - Tests written first for the parser (red, then green). Mutation checks (no limit, password leak in mask, stored duplicates unflagged, credentials without auth) each failed the tests. Race run on the Linux VM passed for all packages.

- 2026-10-20 **Task 4: Explicit proxy transports and availability**:
  - `internal/proxy/{transport,socks5}.go`: `NewClient` (+ `WithRootCAs` for tests), typed `StageError`, `ErrAuthRequired/ErrAuthFailed/ErrTunnelFailed/ErrProxyProtocol`, `TunnelError`.
  - `internal/testkit`: throwaway CA, HTTPS target with request capture, CONNECT proxy (optionally TLS), SOCKS5 proxy, stall and reply-code switches.
  - `internal/providers`: ipify and AWS providers, public-IP validation, `ParseRetryAfter`.
  - `internal/check`: `Classify` and `Checker.Check` implementing the state rules in DECISIONS.md.
  - `internal/store/history.go`: `AddAvailability`, `LatestAvailability`, `ListAvailability`.
  - Tests: 3 protocols x with/without auth matrix, wrong/missing credentials, refused port, proxy and target TLS, stalls, cancellation, poisoned HTTP(S)_PROXY/ALL_PROXY, no direct fallback, redirects not followed, byte-exact tunnel buffers. Transport mutations (direct fallback, environment proxies, dropped buffer, disabled TLS verification) and check mutations (fallback after definite failure, 502 as down, private exit IP accepted) each failed tests. Race run passed on the Linux VM.

- 2026-10-20 **Task 5: Keyed geolocation and cooldowns**: `providers/ipapi.go`, `providers/limits.go` (`RetryAt`, `Backoff`), `store/geo.go` + migration 002, `internal/geo` service. Tests include key-absent-from-URL/headers/logs, coalescing (20 concurrent callers, 1 lookup), shared exit IP, 24 h TTL, stale retention, persisted 1000/day budget with UTC reset, 429/403 pauses and key-change reset. Mutations (no coalescing, budget ignored, 403 pause expiring, lookup without key) each failed tests.
- 2026-10-20 **Task 6: Bounded bidirectional throughput**: `providers/cloudflare.go`, `check/speed.go` (`SpeedChecker.Measure`, `CalculateMbps`, `SpeedConfig`), `store/speed.go` (history, `provider_pause` via migration 003), `testkit/speedservice.go` (fixture that counts real wire bytes). Mutations (unverified ack, short body still rated, pause ignored, warmup not counted, upload buffered in memory) each failed tests. Race run for tasks 5-6 passed on the Linux VM.

- 2026-10-20 **Task 7: Persistent jobs, schedules and incidents**: `internal/store/{jobs,appsettings,incidents}.go` (+ migration 004), `internal/jobs` (`Queue`, `Scheduler`, `Runner`, `Service`), `internal/notify/incidents.go`, `internal/httpapi/jobs.go`, app wiring (job service started with the server, background geo lookups, jobs interrupted before the database closes). Mutations (ignored concurrency setting, claim without proxy exclusion, shutdown storing results, no jitter, disabling deleting manual jobs, canceled results counted) each failed tests; the Linux race run found and fixed a scheduler TOCTOU.

- 2026-10-20 **Task 8: Telegram delivery and events**: `internal/notify/{telegram,worker}.go`, `internal/store/outbox.go` (outbox bookkeeping, encrypted Telegram settings, event listing with delivery state), `internal/httpapi/{settings,events}.go`, app wiring (own notification worker). Mutations (retry_after ignored, token leaking into network errors, markup parsing enabled) each failed tests; the Linux race run passed, including three repetitions of the notify tests.

- 2026-10-20 **Task 9: English-only web interface and browser QA**:
  - Backend additions: dashboard fields on the proxy views, history and chart endpoints, `internal/webui` (embed, SPA fallback, CSP), `app.Options` for local fixtures.
  - `web/`: login, proxy table with search/filter/sort/bulk actions, add/edit dialog, import preview and selective import, proxy detail with availability strip, latency and speed charts and paged history, events, settings (monitoring, location lookup, Telegram, password), theme switch, 375 px layout.
  - Checks run: `npm --prefix web test` (121 tests, 13 files), `npm --prefix web run typecheck`, `node scripts/build-web.mjs`, `npx playwright test` in `web/` (18 tests: setup, auth, a 12-step scenario, 2 mobile). Screenshots were reviewed by eye at 1440x900 and 375x812, light and dark; this found and fixed a chart overflow at 375 px, stretched chart text and cramped table columns.
  - Bugs found by these checks and fixed: event filters waited for the next poll (hook now takes a reset key); the strip chart made the detail page wider than the phone.

- 2026-10-20 **Task 10: Debian container, retention, backups** (code and Docker acceptance done; live proxy/keyed/Telegram acceptance not possible without credentials):
  - `internal/store/prune.go` (bounded-batch retention that keeps latest results, open incidents and pending notifications; `Backup` via `VACUUM INTO`; `IntegrityCheck`), `internal/app/backup.go` (`CreateBackup`, `RestoreBackup`/`RestoreBackupFrom` with archive validation, `HealthCheck`, hourly retention goroutine joined on shutdown), CLI `serve`, `admin set-password`, `backup`, `restore --input <file|-> [--force]`, `healthcheck`.
  - `Dockerfile` (pinned Node/Go/Debian bookworm images, `CGO_ENABLED=0`, uid 10001), `compose.yaml` (LAN_IP-bound port, named volume, read-only root, dropped caps, healthcheck, capped logs), `.dockerignore`, `.env.example`, `docs/operations.md`.
  - Docker acceptance on VM (image 148 MB, about 70 MiB RAM): required LAN_IP error, LAN-only socket, healthy, headers, login/create proxy (password never returned), persistence across recreate, key mode 0600, backup while running, restore into a disposable project (login and stored proxy password OK), stop in 1 s with exit 0.
  - Found by acceptance: a 0600 root-owned archive mounted into the container (uid 10001) cannot be read; `restore --input -` reads the archive from stdin and is the documented method.

## What currently works

- `go build ./...`, `go vet ./...`, `gofmt -l .` and `go test ./...` pass on Windows (Go 1.27.0).
- Mutation checks: temporarily disabling the CSRF check, session expiry, and session revocation on password change each made the relevant tests fail; code was restored afterwards.
- Manual run on Windows: `set-password` rejects a short password and accepts a long one; the server answered `/healthz` 200, a wrong login 401, a correct login 200 with an HttpOnly, SameSite=Strict cookie. The data directory held `secrets.key` (32 bytes) and `state.db`.
- Tests assert passwords, session tokens, CSRF tokens and the password hash do not appear in API responses or captured logs.

## In progress

- Nothing active. Task 3 (proxy management and list import) has not started.

## Known issues / unknowns

- Race detector: `-race` cannot run on Windows (no cgo). It was run on the Debian 13 test VM (Go 1.24.4 system package; `GOTOOLCHAIN=auto` fetched 1.27.0 into the user module cache; gcc 14.2). `go test -race -count=1 ./...` and `go vet ./...` passed for all packages, including the key-file 0600 check that Windows skips. The copied tree was removed afterwards. The first race build takes many minutes on that 1-CPU VM; later runs reuse the cache. Docker has still not been exercised.
- TDD deviation: for Task 2 the implementation files were written before the tests, so the plan's "see FAIL first" step was replaced by mutation checks (above).
- Edited `001_initial.sql` in place (removed `sessions.csrf_digest`) because no database had been deployed. After the first release, schema changes must be new migrations.
- Secure cookies are off by default (`PROXYWARD_SESSION_SECURE=false`): plain LAN HTTP; documented in `docs/operations.md`.
- Not verified: real HTTP/HTTPS/SOCKS5 proxies from Debian, real ipapi.is key, real Telegram bot, IPv6 egress, TLS reverse proxy. These need user-supplied credentials.
- Deleting a proxy removes its job rows by cascade; cancelling a job that is already executing arrives with the job runner (Task 7).
- Importing from the UI, tags UI and schedule semantics (inheritance from global settings) are not wired yet; global settings API is Task 5/7/8.
- No API to read settings yet; `store/settings.go` is storage only.
- Tests ran on Windows only; Linux/Docker runs are pending. Proposed defaults (8 MiB sample, 30-day retention, etc.) are still unreviewed recommendations.
- Real proxy/Debian matrix, IPv6 egress, keyed geo and Telegram remain untested.

## Exact next recommended step

1. Tasks 1-10 are implemented. Remaining: the implementation review gate and the live acceptance with user-supplied real proxies, an ipapi.is key and a Telegram bot (see the plan, Task 10, last-but-one step). Do not mark those done without evidence.
2. Linux checks: push the tree to the VM (tar over ssh from WSL, see the localnet skill) and run `go test -race ./internal/...`.
3. PowerShell sessions need `$env:Path` refreshed from the Machine/User values to find `go`.

## Important files

- `AGENTS.md`, `DECISIONS.md`, `TODO.md`, `README.md` (verified commands).
- `docs/superpowers/specs/2026-10-19-proxy-checker-design.md`, `docs/superpowers/plans/2026-10-19-proxy-checker.md`, `docs/research/2026-10-19-public-services.md`.
- `internal/store/migrations/001_initial.sql`: the schema.
