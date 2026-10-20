# Proxy Checker Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use executing-plans to implement this plan task-by-task. Use subagent-driven-development only if the user explicitly chooses delegation. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a local-network monitor for up to 100 proxies, with availability, bidirectional speed, exit-IP geo/ASN, history, interval scheduling and Telegram.

**Architecture:** One Go process serves React and an authenticated API, manages bounded queues and persists state in SQLite. Explicit proxy test transports are separate from direct geo/Telegram service clients. Network operations never hold database transactions.

**Tech Stack:** Go standard net/http, modernc.org/sqlite, golang.org/x/crypto/argon2; React, strict TypeScript, Vite, Vitest, Testing Library, Playwright; local CSS/assets; multi-stage Docker with a Debian slim runtime.

**Spec:** [Design specification](../specs/2026-10-19-proxy-checker-design.md).

**Research:** [Observed public-service probes](../../research/2026-10-19-public-services.md).

## Global Constraints

- One Docker container on Debian: Go backend, React + TypeScript frontend, SQLite on a persistent local volume.
- One password-protected administrator. Only external public test services; a free geolocation API key is acceptable.
- Every protocol supports authenticated and unauthenticated connections. Accept IPv4, hostnames and bracketed IPv6; ports 1–65535.
- No direct connection fallback when the proxy fails; ignore environment proxy settings for test transports.
- User-defined intervals from 1–10080 minutes; UI supports minutes/hours. Independent enable switches for availability/speed; both start disabled.
- English only: labels, validation, errors, accessible names, comments, tests and documentation. No language selector, translation catalogs or i18n library. User-supplied data is not restricted.
- Development conversations with agents are in Russian; keep Russian text out of project deliverables.
- Cloudflare remains a replaceable best-effort adapter without an established unlimited backend API guarantee.
- Never log secrets, raw upstream response bodies or credential-bearing URLs.
- Do not initialize Git, commit or push without a separate user request. If later initialized, use master as the primary branch.
- This is an unexecuted implementation plan. Completed direct endpoint probes are recorded separately; application checks have not run.

## Starting state and execution

Only project documentation exists. There are no sources, manifests, Docker configuration or application tests. Before work read AGENTS.md, STATUS.md, DECISIONS.md, TODO.md and the spec. Exact supported toolchain/dependency versions must be selected, documented and pinned during task 1 rather than guessed here.

Record `go version`, `node --version`, `npm --version`, `docker version`, and `docker compose version` on the implementation machine. Consult official current documentation through Context7 before library-specific setup. Do not silently install system tools. Keep go.sum and package-lock.json. All normal tests use local fixtures, not internet endpoints. Run a meaningful failing test before implementing its behavior, then prove it passes. Each task is a checkpoint; update project status after a completed stage.

## File map

```text
cmd/proxy-checker/main.go                 server/admin/backup/healthcheck CLI
internal/app/app.go                       wiring and graceful shutdown
internal/config/config.go                 environment parsing and bounds
internal/domain/{proxy,result,settings,job,geo}.go
internal/store/{store,migrate,proxy,history,jobs,settings,auth,geo,outbox,backup}.go
internal/store/migrations/001_initial.sql  embedded versioned schema
internal/auth/{password,session,middleware}.go
internal/secrets/{key,cipher}.go           persistent AEAD key and encryption
internal/proxy/{parse,transport,socks5}.go  import and explicit connections
internal/check/{availability,speed,errors}.go
internal/providers/{ipify,aws,cloudflare,ipapi,limits}.go
internal/jobs/{queue,scheduler,runner}.go
internal/notify/{incidents,telegram,worker}.go
internal/httpapi/{router,auth,proxies,jobs,history,settings,events}.go
internal/testkit/{clock,connectproxy,socksproxy,services}.go
internal/webui/{handler.go,dist/}          real frontend build embedded by Go
web/src/{app,api,styles}/
web/src/features/{auth,proxies,history,events,settings}/
web/e2e/                                 browser integration tests
web/{package.json,package-lock.json,tsconfig.json,vite.config.ts}
scripts/build-web.mjs                     frontend build into webui/dist
Dockerfile, compose.yaml, .dockerignore, .env.example
docs/operations.md                       setup/reset/backup/restore
```

Colocate Go tests as `*_test.go`; use testkit only for fixtures. Do not introduce Go embed until a real frontend build exists in task 9. Core packages remain independently testable beforehand.

## Shared contracts

Define domain names in task 1; later tasks extend the same models rather than inventing competing types. Examples below belong to package domain and import context/time as required. JSON uses snake_case and explicit secret-free DTOs, never direct Proxy serialization.

```go
type ProxyID int64
type Protocol string // "http", "https", "socks5"
type Credentials struct { Username, Password string }
type Proxy struct {
    ID ProxyID
    Name string
    Protocol Protocol
    Host string
    Port uint16
    Auth *Credentials // runtime only; nil disables authentication
    Tags []string
}
type CheckKind string // "availability", "speed"
type State string // "unknown", "up", "down", "indeterminate"
type Failure struct { Code, Message string } // sanitized English message
type Availability struct {
    ProxyID ProxyID
    CheckedAt time.Time
    State State
    ExitIP, Endpoint string
    Duration time.Duration
    Failure *Failure
}
type Transfer struct {
    Bytes int64
    Duration time.Duration
    Mbps *float64 // nil means no completed sample
    LowConfidence bool
    Failure *Failure
}
type Speed struct {
    ProxyID ProxyID
    CheckedAt time.Time
    Provider, Method string
    Download, Upload Transfer
    TotalBytes int64 // includes warmup and failed transfers
}
type Geo struct {
    IP, CountryCode, Country, City, Organization string
    ASN *uint32
    FetchedAt time.Time
}
type Schedule struct { Enabled bool; Interval time.Duration }
type Clock interface { Now() time.Time; After(time.Duration) <-chan time.Time }
```

Store methods introduced in task 1: `Open(ctx context.Context,path string) (*Store,error)`, `Close() error`, `Migrate(ctx context.Context) error`, `InsertProxy(ctx context.Context,p domain.Proxy) (domain.ProxyID,error)`, `GetProxy(ctx context.Context,id domain.ProxyID) (domain.Proxy,error)`, `ListProxies(ctx context.Context) ([]domain.Proxy,error)`. Encrypt auth before authenticated CRUD is enabled in task 3. Never serialize runtime credentials to responses.

### HTTP contract

All API routes require authentication except login/session status. Mutations require CSRF protection. Health contains no secrets.

```text
POST /api/auth/login              {password}; sets session, returns csrf_token
GET  /api/auth/session            {authenticated,csrf_token when authenticated}
POST /api/auth/logout             revokes current session
PUT  /api/auth/password           {current_password,new_password}; revokes all sessions
GET,POST /api/proxies             list/create
GET,PUT,DELETE /api/proxies/{id}   detail/update/delete
POST /api/proxies/import/preview  {text,default_protocol}; masked rows/errors
POST /api/proxies/import          {text,default_protocol,selected_lines}; reparse
POST /api/jobs                    {proxy_ids,kind}; 202 with new/existing jobs
GET  /api/jobs                    active jobs
POST /api/jobs/{id}/cancel        cancel pending/running
GET  /api/proxies/{id}/history     kind,from,to,cursor,limit<=200
GET  /api/proxies/{id}/chart       kind,from,to; <=500 points with gaps
GET  /api/events                  filters,cursor,limit<=200
GET,PUT /api/settings             masked settings, secret replacement fields
POST /api/settings/telegram/test  enqueue test message
POST /api/settings/speed/resume   explicitly resume paused adapter
GET /healthz                     app/DB readiness without outbound checks
```

Errors: `{"error":{"code":"validation_failed","message":"...","fields":{}}}`. Use 400 malformed input, 401 unauthenticated, 403 forbidden/CSRF, 404 missing ID, 409 duplicate/conflict, 422 invalid field/record limit, 429 login throttling, 503 not initialized/provider unavailable. Never echo upstream raw errors or submitted secrets.

Proxy schedule override absent means inherit; explicit disabled overrides global enabled. Credential update: omitted field means keep; auth_enabled=false removes auth; password string, including empty, replaces password. Do not silently interpret null as empty. Settings mutations return masked settings only.

## Task 1: Persistent foundation

**Files:** Create go.mod/go.sum, cmd/proxy-checker/main.go, internal/app/app.go, config/config.go, domain files, store/{store,migrate,proxy}.go, migrations/001_initial.sql and tests. Update README toolchain details.

**Interfaces:** Shared contracts; `app.Run(ctx context.Context,cfg config.Config) error`. Config contains ListenAddr, DataDir and SessionSecure. Serve only after successful migrations.

- [x] Record installed tools, consult Go/SQLite documentation, choose supported compatible toolchains and pin dependencies. Document unavailable tools instead of claiming checks passed.
- [x] Write temporary-database persistence and migration tests before implementation:

```go
func TestProxySurvivesReopen(t *testing.T) {
    ctx := context.Background()
    path := filepath.Join(t.TempDir(), "state.db")
    db, err := Open(ctx, path)
    if err != nil { t.Fatal(err) }
    if err := db.Migrate(ctx); err != nil { t.Fatal(err) }
    id, err := db.InsertProxy(ctx, domain.Proxy{Name:"local",Protocol:"http",Host:"127.0.0.1",Port:8080})
    if err != nil { t.Fatal(err) }
    if err := db.Close(); err != nil { t.Fatal(err) }
    db, err = Open(ctx, path)
    if err != nil { t.Fatal(err) }
    defer db.Close()
    p, err := db.GetProxy(ctx, id)
    if err != nil || p.Host != "127.0.0.1" { t.Fatalf("host=%s err=%v",p.Host,err) }
}
```

- [x] Run `go test ./internal/store -run TestProxySurvivesReopen -v`, confirm expected FAIL, then implement opening, migration and basic unauthenticated CRUD. Schema includes all spec tables, typed timestamps/durations/bytes, persisted incident state, indexes and uniqueness constraints. Enable WAL/foreign_keys/busy_timeout on connections and serialize short writes.
- [x] Add migration atomicity/idempotence, foreign-key and config-boundary tests. Wire cancellation, health and graceful database close. Run `go test ./internal/store ./internal/config -v` and `go vet ./internal/...`.

**Checkpoint:** Persistent core works without claiming UI/container completion.

## Task 2: Admin sessions and encrypted secrets

**Files:** Create auth/{password,session,middleware}.go, secrets/{key,cipher}.go, store/{auth,settings}.go, httpapi/{router,auth}.go, tests; extend CLI.

**Interfaces:** `auth.Hash(password string) (string,error)`, `auth.Verify(encoded,password string) bool`; `secrets.Load(dataDir string) (*Cipher,error)`; Cipher `Seal(plain []byte) ([]byte,error)` and `Open(sealed []byte) ([]byte,error)`; `httpapi.New(db *store.Store,cipher *secrets.Cipher) http.Handler`. CLI `admin set-password` reads stdin/TTY.

- [x] Write password success/mismatch, ciphertext tampering, atomic key creation/reuse, session expiry, missing-CSRF, logout and password-change revocation tests. Use actual temp SQLite and local HTTP, with injectable clock.

```go
func TestCipherRejectsTampering(t *testing.T) {
    c, err := Load(t.TempDir())
    if err != nil { t.Fatal(err) }
    sealed, err := c.Seal([]byte("secret"))
    if err != nil { t.Fatal(err) }
    sealed[len(sealed)-1] ^= 1
    if _, err := c.Open(sealed); err == nil { t.Fatal("tampered ciphertext accepted") }
}
```

- [x] Run auth/secrets/httpapi tests to see FAIL. Implement Argon2id (64 MiB, 3 iterations, parallelism 1, 32-byte hash), constant-time verification and bounded malformed-hash handling. AES-GCM uses a persistent 32-byte key, random nonce and 0600 permissions; never regenerate a missing key over existing encrypted records.
- [x] Implement session digests/cookies/24h expiry, Origin and CSRF checks, login throttling (5/minute per peer and 20/minute global, bounded expiring state). Do not trust arbitrary X-Forwarded-For. Initial setup is CLI-only; unset admin blocks normal access.
- [x] Run `go test -race ./internal/auth ./internal/secrets ./internal/httpapi`. Capture output and prove passwords/token/ciphertext never appear in responses or logs.

**Checkpoint:** One protected admin and recoverable secret storage.

## Task 3: Proxy management and list import

**Files:** Create proxy/parse.go, httpapi/proxies.go and tests; extend store/proxy.go and domain/proxy.go.

**Interfaces:** `ParseLine(line string,defaultProtocol domain.Protocol) (domain.Proxy,error)`; `Preview(text string,defaultProtocol domain.Protocol) []ImportRow`; ImportRow fields: Line int, Masked string, Valid bool, Duplicate bool, ErrorCode string. `Store.ImportProxies(ctx context.Context,proxies []domain.Proxy) ([]domain.ProxyID,error)` enforces atomic max100.

- [x] Write parser tables for all schemes/auth modes, percent-encoded credentials, empty password, IPv6, bare host:port, invalid scheme/port/path/query and empty username. Assert preview redacts submitted secrets.

```go
func TestParseUnauthenticatedIPv6(t *testing.T) {
    p, err := ParseLine("socks5://[2001:db8::1]:1080", "http")
    if err != nil { t.Fatal(err) }
    if p.Auth != nil || p.Host != "2001:db8::1" || p.Port != 1080 || p.Protocol != "socks5" {
        t.Fatalf("unexpected endpoint: host=%s port=%d",p.Host,p.Port)
    }
}
```

- [x] Run parser tests to FAIL, then implement normalization and duplicate detection. Normalize DNS/scheme case and IPs; preserve credential case. Persist a keyed duplicate fingerprint derived separately from the encryption key, not a public password hash.
- [x] Implement CRUD/import routes, body cap 256 KiB, selected-row validation and reparse on save. Persist credentials encrypted. Different auth at one endpoint is allowed; imports exceeding max100 roll back. Validate tags and schedule overrides.
- [x] Run `go test ./internal/proxy ./internal/store ./internal/httpapi -v`. Cover concurrent imports, 99 existing +2 requested, secret-preserving edits, duplicate409 and deleting associated data/jobs. Assert GET DTOs contain no passwords.

**Checkpoint:** Safe individual and pasted-list management.

## Task 4: Explicit proxy transports and availability

**Files:** Create proxy/{transport,socks5}.go, check/{availability,errors}.go, providers/{ipify,aws}.go, testkit/{connectproxy,socksproxy,services}.go, store/history.go and tests.

**Interfaces:** `proxy.NewClient(p domain.Proxy,timeout time.Duration) (*http.Client,error)`; `AvailabilityChecker.Check(ctx context.Context,p domain.Proxy,client *http.Client) domain.Availability`; provider interface `Name() string`, `Lookup(ctx context.Context,client *http.Client) (netip.Addr,error)`. Fixture URL/CA injection is explicit and test-only.

- [x] Implement fixtures exposing endpoint, credentials, captured requests and Close: HTTP CONNECT; TLS-wrapped CONNECT; SOCKS5 with no-auth/username-password; HTTPS target with test CA. Never disable production TLS verification.
- [x] Write the 3x2 protocol/auth matrix: target receives the proxied request, proxy credentials do not reach target, remote SOCKS DNS, wrong auth, refused port, invalid cert, stalled handshake, cancellation and poisoned environment proxy variables.

```go
func TestDeadProxyCannotReachTarget(t *testing.T) {
    var hits atomic.Int32
    target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter,r *http.Request) { hits.Add(1); w.WriteHeader(200) }))
    defer target.Close()
    listener, err := net.Listen("tcp", "127.0.0.1:0")
    if err != nil { t.Fatal(err) }
    port := listener.Addr().(*net.TCPAddr).Port
    listener.Close()
    c, err := NewClient(domain.Proxy{Protocol:"http",Host:"127.0.0.1",Port:uint16(port)}, time.Second)
    if err != nil { t.Fatal(err) }
    response, err := c.Get(target.URL)
    if response != nil { response.Body.Close() }
    if err == nil || hits.Load() != 0 { t.Fatalf("direct bypass: err=%v hits=%d",err,hits.Load()) }
}
```

- [x] Run proxy/check tests to see FAIL. Implement context-aware proxy transports with deadlines and preserved CONNECT buffered bytes. Prefer documented standard-library support; add explicit SOCKS5 handshake only where needed. Disable inherited proxy settings, redirects and unsafe certificate bypasses.
- [x] Implement IP provider parsing capped at 4 KiB, reject invalid/private exit IP, fallback and typed errors. Distinguish explicit proxy failure from indeterminate provider/route failure; direct health diagnosis never counts as proxy success. Persist endpoint, observation timestamp and duration.
- [x] Run `go test -race ./internal/proxy ./internal/check ./internal/providers ./internal/store`. Cover truncated JSON, invalid AWS text, both providers failing, target TLS and cancellation.

**Checkpoint:** Offline evidence for no-direct-fallback and usable availability results.

## Task 5: Keyed geolocation and cooldowns

**Files:** Create providers/{ipapi,limits}.go, store/geo.go, domain/geo.go and tests; extend settings.

**Interfaces:** IPAPI method `LookupGeo(ctx context.Context,ip netip.Addr) (domain.Geo,error)` using a separate direct client; GeoService `Get(ctx context.Context,ip netip.Addr) (GeoView,error)`. GeoView contains Geo, Stale bool, Status and ErrorCode. `RetryAt(header string,now time.Time) time.Time` handles provider cooldowns.

- [x] Write local keyed-schema/null/missing-ASN/error fixtures. Inspect POST q/key and prove key absent from URL/logs. Test 24h cache, shared-IP single lookup, concurrent coalescing, stale retention, persisted 1000/day budget and UTC reset.

```go
func TestRetryAfterDate(t *testing.T) {
    now := time.Date(2026,10,19,12,0,0,0,time.UTC)
    got := RetryAt("Mon, 19 Oct 2026 12:01:00 GMT", now)
    if !got.Equal(now.Add(time.Minute)) { t.Fatalf("retry=%v",got) }
}
```

- [x] Run provider tests to FAIL; implement bounded 5s direct POST, 64 KiB response limit, no redirect, missing-key Not configured, nullable fields and response-IP validation. Enforce 403 pause, 429 Retry-After/date and fallback exponential backoff; no anonymous fallback or proxy-based quota evasion.
- [x] Persist cache/budget/provider pause, reset auth pause on changed key without clearing usage. Store ASN route organization separately from country metadata. Run `go test -race ./internal/providers ./internal/store ./internal/httpapi`, proving geo failure cannot change availability.

**Checkpoint:** Cached geo/ASN with explicit stale/quota states.

## Task 6: Bounded bidirectional throughput

**Files:** Create check/speed.go, providers/cloudflare.go and tests; extend result domain and history persistence.

**Interfaces:** `SpeedChecker.Measure(ctx context.Context,p domain.Proxy,client *http.Client,cfg SpeedConfig) domain.Speed`; SpeedConfig fields SampleBytes int64, WarmupBytes int64, DirectionTimeout time.Duration. `CalculateMbps(bytes int64,elapsed time.Duration) (float64,error)`. Cloudflare adapter builds requests and validates server-accepted bytes.

- [x] Write exact-rate, nonpositive-duration, low-confidence, oversized/short response, accepted-byte mismatch, canceled/timeout, 403/429 and partial-direction tests. Fixtures must count actual request bytes and inspect Content-Length, not merely echo client counters.

```go
func TestCalculateMbps(t *testing.T) {
    got, err := CalculateMbps(8_000_000, 2*time.Second)
    if err != nil || got != 32 { t.Fatalf("mbps=%v err=%v",got,err) }
    if _, err := CalculateMbps(1,0); err == nil { t.Fatal("zero duration accepted") }
}
```

- [x] Run speed tests to FAIL, then implement warmup/sample streaming with explicit timeout/size caps, no compression or redirects. Generate incompressible upload data with a bounded reader, known Content-Length and context cancellation. Count all transferred bytes, including warmups and failures.
- [x] Implement monotonic timings and exact acknowledgement validation; no arbitrary timing subtraction. Keep successful directions, withhold Mbps for incomplete samples, store endpoint/method and confidence. Apply provider-wide cooldown before processing another proxy.
- [x] Run `go test -race ./internal/check ./internal/providers ./internal/store`. Slow fixtures prove mid-stream cancellation and bounded memory independent of the 64 MiB maximum sample.

**Checkpoint:** Honest approximate throughput, separate from proxy availability.

## Task 7: Persistent jobs, schedules and incidents

**Files:** Create jobs/{queue,scheduler,runner}.go, store/jobs.go, testkit/clock.go, notify/incidents.go, httpapi/jobs.go and tests; extend schedule settings.

**Interfaces:** `Queue.Enqueue(ctx context.Context,ids []domain.ProxyID,kind domain.CheckKind,source string) (EnqueueResult,error)` with new/existing job IDs; `Scheduler.Tick(ctx context.Context,now time.Time) error`; `Runner.Run(ctx context.Context) error`; `IncidentTracker.Apply(ctx context.Context,result domain.Availability) error` writes incident/event/outbox atomically.

- [x] Write fake-clock cases: disabled/enabled/global/override, bounds and minute/hour conversion, repeated ticks, manual/scheduled collision, deletion/cancel, restart interruption, no catch-up storm. Concurrent tests assert per-kind limits and no same-proxy overlap using atomic counters.
- [x] Write incident sequences: initial up sends nothing; three definite failures create one down event; subsequent failures do not duplicate; indeterminate neither closes nor increments; success produces recovery only after down; cancel/interrupted never increment failures.
- [x] Run jobs/notify tests to FAIL. Implement atomic claim, partial unique active-job index, bounded FIFO queues, per-proxy exclusion, 5 default availability workers and one speed worker. Persist end-based next_run_at and source. Enqueue route returns202; duplicate job returns its existing ID.
- [x] Implement startup running-to-interrupted recovery, seeded injectable 0–30s jitter, disabling only scheduled pending jobs and deletion cancel. Run `go test -race ./internal/jobs ./internal/notify ./internal/store ./internal/httpapi` and restart against the same temporary DB to prove incident/outbox uniqueness.

**Checkpoint:** User-defined independent intervals and restart-safe manual jobs/incidents.

## Task 8: Telegram delivery and events

**Files:** Create notify/{telegram,worker}.go, store/outbox.go, httpapi/events.go and tests; extend settings handlers.

**Interfaces:** `Telegram.Send(ctx context.Context,chatID,text string) error`; `Worker.DeliverDue(ctx context.Context,now time.Time) error`; outbox persists event ID, attempts, next_attempt_at, delivered_at and last_error_code. Cursor pagination uses (created_at,id). Direct service client independent of checked proxies.

- [x] Write fake Bot API cases: success; HTTP200 with ok:false; 429 retry_after; 403; 500; response lost after accepting request; group longer than4096 characters; hostile proxy names. Use plain text or fully escaped markup and split long messages without dropping events.
- [x] Test 10s batching, test-message status, maximum5 attempts, restart persistence, final failure visibility and recovery grouping. Capture logs: Telegram token occurs in the upstream URL path and must never be logged.
- [x] Run notify tests to FAIL; implement outbox delivery and delays30/60/120/240s with provider retry_after priority. Settings test enqueues and returns message ID, not false immediate delivery success. Do not promise exactly-once.
- [x] Implement events filtering, masked settings replacement and paginated delivery states. Run `go test -race ./internal/notify ./internal/store ./internal/httpapi`; Telegram failure cannot block network workers or change proxy status.

**Checkpoint:** Configurable Telegram with observable, durable delivery.

## Task 9: English-only web interface and browser QA

**Files:** Create web manifests/config, api/{client,types}.ts, styles/{tokens,global}.css, app/App.tsx; features/auth/LoginPage.tsx; proxies/{ProxyPage,ProxyTable,ProxyDialog,ImportDialog,ProxyDetail}.tsx; history/{SpeedValue,HistoryTable,HistoryChart}.tsx; events/EventPage.tsx; settings/SettingsPage.tsx; component tests and web/e2e/{auth,proxies,checks,settings}.spec.ts; scripts/build-web.mjs; internal/webui/handler.go and history/chart API handlers.

**Interfaces:** `api<T>(path:string,init?:RequestInit):Promise<T>` attaches CSRF/same-origin credentials and returns typed ApiError; ProxyView/JobView/SettingsView mirror safe backend DTOs. `useVisiblePolling(load:()=>Promise<void>,intervalMs:number)` serializes loads, skips hidden tabs and aborts on unmount. Routes: /login, /proxies, /proxies/:id, /events, /settings.

- [x] At implementation time invoke frontend and visual QA skills, consult current React/Vite/Playwright documentation and pin compatible dependencies. Define scripts: test=`vitest run`, typecheck=`tsc --noEmit`, build=`tsc --noEmit && vite build`, e2e=`playwright test`. Bundle SVG flags locally and include license attribution.
- [x] Before component implementation write behavioral tests: authentication required, import row errors and selection, no prefilled stored password, nil speed distinct from zero, disabled active-job actions and preserved auth choice on protocol change. Use roles/labels, not snapshot-only assertions.

```tsx
it('does not show zero for unavailable speed', () => {
  render(<SpeedValue mbps={null} />);
  expect(screen.getByText('—')).toBeInTheDocument();
  expect(screen.queryByText('0 Mbps')).not.toBeInTheDocument();
});
```

- [x] Run `npm --prefix web test`, observe FAIL, then implement SpeedValue and screens. Use English labels: Proxies, Events, Settings, Add proxy, Import list, Check availability, Test speed, Unknown, Up, Down, Indeterminate, Not configured, Low confidence. English accessible names, validation and API messages; en-US formatting with local timezone. No translation system.
- [x] Implement neutral compact table, restrained accents, textual statuses, light/dark/system theme, local fonts/assets, chart gaps, side navigation and mobile layout. Focus management, keyboard controls and reduced motion. Implement search/filter/bulk actions, secret-preserving edits, queue/cancel/progress, provider pauses and independent interval settings.
- [x] Implement paginated SQL history and <=500-point chart queries, retaining gaps/sample counts instead of false precision. Poll visible tabs every5s. Show stale geo, low confidence and direction-specific failures; do not replace missing values with zero.
- [x] Build script writes real compiled frontend to internal/webui/dist, deleting stale output only inside that directory. Add Go embed after build; SPA fallback never masks missing /api routes. Run `node scripts/build-web.mjs`, `go test ./internal/webui ./internal/httpapi`.
- [x] Run typecheck, Vitest, build and Playwright scripts against disposable app/DB and injected local providers. Manually exercise real screens at1440x900 and375x812, light/dark, with screenshots. Verify login/import/manual checks/settings/charts/errors and English-only copy. Browser tests never depend on production data or public endpoints.

**Checkpoint:** End-to-end usable English UI, not just component demos.

## Task 10: Debian container, retention, backups and acceptance

**Files:** Create Dockerfile, compose.yaml, .dockerignore, .env.example, docs/operations.md, store/backup.go and tests; extend CLI/app lifecycle and retention; update README/STATUS/DECISIONS/TODO/.gitignore.

**Interfaces:** CLI commands: `serve`, `admin set-password`, `backup --output <path>`, `healthcheck`. `Store.Backup(ctx context.Context,destination string) error`; `Prune(ctx context.Context,before time.Time,batchSize int) (int,error)`. Backup archive contains a consistent DB snapshot and secrets.key with restrictive permissions; restore requires stopped server.

- [x] Write retention cutoff/batch/latest-state tests and backup-during-write test. Open the backup separately, check PRAGMA integrity_check and decrypt a proxy secret with the backed-up key. Test failed migration/startup and graceful cancellation.
- [x] Run backup/retention tests to FAIL, then implement the documented modernc SQLite backup API, atomic archive creation and bounded deletion preserving current snapshot/incident state. Do not copy only a live db file or expose backups through unauthenticated web routes.
- [x] Implement pinned multi-stage frontend/Go build, Debian slim with CA certificates and nonroot writable /data. Copy frontend build before Go embed compilation. .dockerignore excludes secrets, db/WAL, host dependencies and .git. Handle SIGTERM without recording canceled work as proxy failure.
- [x] Compose port binding: `${LAN_IP:?set LAN_IP}:8080:8080`; local named volume, readiness healthcheck, capped logs and restart unless-stopped. Document actual host binding, LAN HTTP/session implications and firewall configuration; no automatic server/router changes. .env.example contains no secrets.
- [x] Run `go test ./...`, `go test -race ./internal/...`, `go vet ./...`, frontend checks, `docker compose config --quiet`, `docker compose build`. Start a disposable compose project/volume, set admin password, create proxy, recreate container, prove persistence, inspect key permissions and restore backup. Record blocked checks accurately if Docker is unavailable.
- [ ] Separately run authorized live acceptance on Debian using supplied real proxies: each available protocol/auth pair, public exit-IP observation, keyed geo, small bounded Download/Upload, real Telegram notification, scheduled run and recovery. List missing matrix entries rather than declaring them tested; never use arbitrary public proxy lists as test credentials.
- [x] Document exact tested setup/reset/build/test/backup commands and observed resource use in English. Update status and run the implementation review gate before handoff. Do not mark real integrations complete without evidence. (Review gate still pending.)

**Checkpoint:** Reproducible deployment, recoverable state and explicit acceptance evidence.

## Decision gates and self-review

1. Before implementation, review technical defaults: 8 MiB speed samples, 30-day history, interval bounds. English-only project content is an explicit user requirement.
2. Cloudflare probes passed, but no scheduled-backend quota/SLA was established. Keep a replaceable adapter and expose provider-unavailable honestly if access changes.
3. Real Debian/proxy/keyed geo/Telegram acceptance requires separate evidence and supplied credentials. Local fixture tests remain independent.

| Requirement | Coverage |
| --- | --- |
| Go/React/SQLite, local single container | 1, 9, 10 |
| Single admin, reset, session protection | 2, 9, 10 |
| Three protocols, optional auth, no bypass | 3, 4 |
| Individual/list import, max100, duplicates | 3, 9 |
| Manual and independent user intervals | 7, 9 |
| Availability, reasons and observed IP | 4, 7, 9 |
| Flag/country/city/ASN, cache and quota | 5, 9 |
| Download/Upload, limits, partial results | 6, 7, 9 |
| History, charts, logs and retention | 1, 4–10 |
| Telegram and durable incidents/outbox | 7, 8 |
| English-only project, themes and mobile | Global constraints, 9, 10 |
| Public-only providers and honest limits | Research, 4–6, 10 |
| Persistent encryption/backup/restore | 2, 10 |

Next action after plan review: task1 using executing-plans in the current session. Delegate only if the user explicitly chooses that workflow. No implementation has started.
