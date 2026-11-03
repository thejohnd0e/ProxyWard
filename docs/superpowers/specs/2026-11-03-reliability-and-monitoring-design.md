# Reliability and Monitoring Expansion Design

## Status and intent

Approved for implementation by the user on 2026-11-03 together with the linked staged plan. The user selected Operations Workspace (A), accepted its dark palette and desktop sidebar, and approved the overview/proxy list/detail/events/settings visual direction. Mobile navigation remains a separate UX approval gate. No deployment or live provider traffic is authorized; implementation tests remain offline fixtures.

Current baseline: commit `bd18be8`; Go/React/TypeScript/SQLite; one administrator; at most 100 proxies; HTTP, HTTPS and SOCKS5. The review ran Go build/test/vet, frontend build/type checking, 149 Vitest tests and 22 Playwright tests successfully. These are baseline results, not verification of this design.

Read `AGENTS.md`, `STATUS.md`, `DECISIONS.md`, `TODO.md`, and the original design. Later recorded decisions supersede the original design. In particular: keyless geolocation, IPv4-first primary availability, all-provider timeouts, and persistent monitoring pause already exist.

## Approaches considered

1. **Recommended: incremental releases in the current application.** Fix lifecycle correctness first, add observation primitives, then configurable checks, policies and integrations. One local database and bounded workers remain sufficient for 100 proxies. Each release remains usable independently.
2. **One combined rewrite.** Easier to redesign every interface together, but creates a large migration and regression surface and delays the fixes. Not recommended.
3. **External monitoring stack as the primary product.** Reuse Prometheus/alerting systems for most features, but introduce required services and fragment the current single-container workflow. Keep those integrations optional instead.

The user approved incremental implementation in the existing application on 2026-11-03. Contracts and release boundaries below are the implementation authority; conflicts with existing durable decisions must be resolved in favor of the existing decision and recorded.

## Goals, constraints and non-goals

- Preserve existing proxies, encrypted credentials, sessions, schedules, results and backups through forward migrations.
- Go 1.27.0, React 19, strict TypeScript, Vite, SQLite and one nonroot Debian container; no required external broker/database/service.
- English-only UI, documentation, identifiers, comments, tests, validation and errors. Conversation remains Russian; user data remains unrestricted.
- Up to 100 proxies. Independent manual/scheduled availability and speed checks remain supported.
- All monitored requests use the explicit selected proxy, ignore environment proxy variables, validate TLS, and never fall back directly.
- Existing primary availability and its incidents keep their meaning. Optional family/site failures do not silently change the primary state.
- No unlimited probes, hidden speed tests, automatic proxy switching, VPN configuration changes, traffic routing, browser impersonation or attempts to bypass provider limits.
- No public dashboards, additional user roles, arbitrary scripts, arbitrary request headers, POST bodies, target credentials or regex body assertions in this expansion.
- Tests are offline fixtures by default. Live acceptance is separate, bounded and explicitly authorized.

## Release sequence

| Release | Deliverable | Tasks in implementation plan |
| --- | --- | --- |
| A | Correct schedules/cancellation, visible request errors, cooldowns, safer setup, CI and atomic outcome recording | 1-8 |
| B | Trace diagnostics, family observations, IP history, downtime/freshness views and export clarification | 9-13 |
| C | Tags/groups, bulk control, speed budgets, custom target checks | 14-18 |
| D | Notification policies, comparison, read-only tokens, webhooks and Prometheus | 19-24 |
| Gate | Upgrade/restore/load/security/live acceptance | 25 |

## A. Reliability and security

### Scheduling

Move effective-schedule evaluation and updates into serialized store transactions. Scheduled enqueue and claim must re-check current pause/enabled/due state, never trust a scheduler snapshot. A stale tick must not enqueue work or erase a newer next-run value. Use the same transaction helpers from global settings, per-proxy overrides and bulk changes.

Proposed interval-change rule: an already-enabled effective interval change sets next run to `change_time + new_interval` and removes/replaces pending scheduled work of that kind. Unchanged effective settings preserve next run. Newly enabled/resumed monitoring uses existing 0-30 second jitter. Disabling or pausing atomically clears next run and pending scheduled work; running jobs may finish; manual jobs survive. Global changes do not disturb proxies whose overrides make the effective schedule unchanged.

Persist a per-kind schedule revision and capture it on claim. Completion/cancellation may advance the next run only when the revision is unchanged; otherwise preserve the newer configuration's next run. Pending manual cancellation leaves the schedule unchanged. Canceling a scheduled occurrence, or finishing an accepted running cancellation, advances an unchanged enabled schedule to cancellation completion plus its interval, preventing immediate re-enqueue; disabled/paused remains unset.

### Cancellation and committing outcomes

Persist `cancel_requested_at` for running jobs. The request is durable even before a runner registers its cancel function. Claim/execute and final outcome commit re-check it. The write transaction determines the race: cancellation committed first prevents results/incidents; completed outcome committed first makes a subsequent cancel return 409. Shutdown remains interrupted, not down.

Record a completed availability result, incident transition, event/outbox, job completion and next run in one transaction. A unique nullable result `job_id` makes retries idempotent; old results retain null. Keep existing low-level store helpers usable for fixture/history tests, but production runners use the atomic completion path. Speed completion follows the same lifecycle rule; accounting of consumed traffic is allowed for canceled/interrupted jobs, but successful measurement history is not.

Cancellation and atomic completion are one release gate: do not ship the cancellation change alone with the old split-write runner. A failed outcome transaction never marks the job done. Retry the idempotent transaction at most three times with bounded backoff; persistent failure stops claims and surfaces an unhealthy service error, leaving durable running work for normal interrupted recovery. Store transaction helpers cannot depend on `notify`; rule/delivery packages may depend on store, not vice versa.

### Provider cooldown

Reuse `provider_pause` with quota-identity keys such as `availability:ipify`. Endpoint IDs and quota identities are distinct: `ipify`, `ipify-dual` and any ipify family-only endpoint share `availability:ipify`; AWS is independent. Persist Retry-After (seconds/date), or exponential 60-second to 1-hour fallback. Pauses are provider-wide; manual requests and different exits do not bypass them. Concurrent updates only extend a pause, never shorten it; late successful responses never clear a newer pause. A fallback from a different quota identity remains eligible. A skipped paused provider is not a timeout; an entirely paused provider set returns indeterminate/rate-limited, not down. Expose cooldown status without probing from GET endpoints.

### UI races/errors

Events pagination is bound to a filter generation and AbortController; changing filters/unmounting invalidates old responses and cursors. Polling and loaded pages must not leave a hole when new events push rows out of the first page: merge by stable event ID and cursor boundaries, or explicitly reset pagination with a visible notice. Settings polling handles failures explicitly, retains last good data, exposes retry, and never overwrites edits. Chart errors become visible rather than disappearing silently.

### First-run setup

Proposed bootstrap: `proxyward admin setup-token` creates a random 256-bit token, valid for 30 minutes, stored as a SHA-256 digest and displayed only by that CLI command. The first-run form requires it; request bodies and logs never echo it. Token verification and administrator creation are atomic, single-use, rate-limited and unavailable after initialization. The CLI password-reset path remains available. `start.sh` prints instructions to obtain a token, never prints it automatically into service logs.

Keep existing explicit `BIND_ADDRESS` values unchanged. Correct the misleading Compose comment, document the all-interface default, and show a prominent first-start warning about exposed HTTP. Do not silently change a working installation's address. A stricter default binding can be approved separately.

## B. Observation model

### Trace diagnostics

Collect structured monotonic stage durations: proxy DNS/TCP, proxy TLS, tunnel negotiation, target TLS, first byte, response body and total. Unsupported/unobserved stages are null, never zero. HTTP CONNECT authentication is part of tunnel negotiation; do not invent a separately measurable authentication duration. SOCKS5 may expose its actual auth stage. Record each provider attempt separately, with provider ID, status/failure code and stages; never raw URLs with secrets, headers or bodies.

The proxy-side DNS stage describes resolving the proxy host on the monitor; target DNS handled remotely by CONNECT/SOCKS5 is not measured locally. Use transport callbacks and `httptrace` together; `httptrace` alone cannot see custom dialer internals. Normal checks supply diagnostics, so opening a screen generates no traffic.

### IPv4/IPv6

Keep primary `status` and historical rows unchanged. Add optional `ipv4` and `ipv6` observations to an availability job, run sequentially under the same per-proxy exclusion. Independent family probes start disabled on upgrade; a UI action enables them and states the additional request volume. Explicit family-only provider endpoints must be verified before enabling production defaults; a dual-stack hostname is not proof of IPv6. A result only succeeds when its returned public IP matches the requested family.

Store family results with their own observation time/provider/failure, linked to the parent job. Existing primary observations are not retroactively presented as deliberate family tests. With only one independent family provider, ambiguous provider/tunnel failures remain indeterminate; explicit proxy failures may still be down. Family state is evidence about the tested path, not proof that every site supports that family.

### Exit changes

Persist exit-IP observations per proxy and scope (`primary`, `ipv4`, `ipv6`). Compare successful observations within the same scope only. First observation establishes a baseline; unchanged IP emits nothing; failed checks never replace it. `exit_ip_changed` has old/new address and observation times. Bounded repeated changes remain in history; notification deduplication is a separate policy.

Country/ASN enrichment records `pending`, `resolved` or `unknown` independently. A cache/provider change is not an IP change. Country/ASN-change events require comparable confirmed old/new enrichment for consecutive addresses, never incomplete/stale or cross-family attribution; they do not alter availability. Rapid IP rotation is labeled as observed rotation, not automatically a VPN fault.

Persist geolocation provenance (`requested_ip`, `returned_ip`, provider, observation time and exact-match flag). Existing cross-family display enrichment remains usable for display but is ineligible for country/ASN-change events. Old cache rows without provenance are unknown for this purpose; do not infer exact matching retroactively.

### Downtime and unknown periods

Distinguish threshold-confirmed incidents from sample-derived failure episodes. V2 episodes expose `first_failed_at`, `last_failed_at`, nullable `recovered_at`, `left_censored`, `right_censored`, states and codes. Include a bounded predecessor/successor context read from the same transaction; unavailable retained context is explicitly unknown, not invented.

Timeline segments are up/down/indeterminate/paused/no-observation. Store monitoring pause/resume and service-session boundaries prospectively. Historical segments without those boundaries stay uncertain. For enabled schedules, a gap larger than `max(2 * effective_interval, effective_interval + 60 seconds)` is no-observation; manual-only samples do not imply continuous coverage. Interval changes have dated schedule boundaries. Do not paint an outage or recovery across an unknown gap.

Summaries show sampled availability, sample counts, observation coverage and observed failure-window durations. The exact instant between the last success and first failure is unknown. No chart or summary may advertise exact downtime/SLA from sparse samples. Freshness is independent of last state; disabled/manual-only monitoring has no implied overdue deadline.

### Export compatibility

Keep schema v1 output/meaning available. Add explicit `schema_version: 2` selection to POST exports; upgrade the UI to request v2. V2 uses the clarified episodes and includes allowlisted family, trace, exit-change and target observations once each feature ships. All arrays use one snapshot and a combined 100,000 raw-record limit; contextual boundary rows are separately bounded to two per selected scope and disclosed as context, not extra selected history. No silent truncation, target query strings, secrets, body snippets or free-form messages. Unknown version is 422. Retention remains visible in metadata.

## C. Configurable monitoring

### Groups and bulk actions

Use existing tags as lightweight groups; do not create a second competing group entity. Add a tag filter, saved table view (search, tags, state, sort; no credentials), and bulk pause/resume/tag/schedule operations. Explicit selected IDs define the scope, not an implicit current filter. Preview count and changes; validate all items before one transaction, fail the batch on missing/conflicting items, and preserve manual work. Bulk check actions reuse existing queue semantics and report missing IDs.

### Speed traffic budgets

Per-proxy and global daily/monthly budgets are optional and disabled by default. UTC boundaries, MiB units in UI, integer bytes in storage. Account application payload transferred by the speed adapter, including warmups, failed/canceled/interrupted attempts; not TLS/TCP/IP overhead and not all network traffic. Keep this label explicit. Predictions show assumptions and include enabled per-proxy schedules.

Reserve the maximum planned payload before sending, checking all applicable budgets atomically. Refuse a job that cannot fit; no override for manual requests. Settle actual bytes and release unused reservations. Preserve durable progress checkpoints; a process crash leaves an uncertain reservation conservatively charged at its reserved amount until reconciliation/manual acknowledgment, not silently reset. Retain ledger totals independently of result retention. A budget-blocked speed job does not block availability/target jobs, spin, or create an unlimited backlog; expose reason and next eligible reset. Period transitions do not erase unresolved reservations.

Proposed accounting policy: assign a job's complete payload to the UTC day/month in which its reservation was created, even if it finishes after midnight. UI/docs call this job-start-attributed payload, not exact calendar-period wire usage. Uncertain charges stay in that original period; a new period starts its own allowance without deleting old obligations. Download reads are capped to expected bytes plus one validation byte per download; upload acknowledgment bodies are capped at 4 KiB each and included in usage/reservation. Maximum reservation is `2*(warmup+sample) + 2*4096 + 2` bytes for the normal four-request measurement. Never read an unrestricted 32-KiB chunk past the reserved validation allowance. Bytes mean payload observed by the application stream, not hidden transport prefetch or link overhead.

### Custom targets

At most 20 target definitions application-wide and 10 assigned targets per proxy; checks start disabled. HTTPS GET only, port 443; maximum URL length 2048 bytes, no userinfo, query string or fragment. Expected final HTTP status 200-599, default 200; optional literal UTF-8 body substring at most 256 bytes; no regex or stored response body. Maximum inspected body 64 KiB; timeout 1-60 seconds, default 10; interval 1-10080 minutes, suggested 15. Redirects are not followed and TLS verification cannot be disabled. Cookies are not retained.

Private proxy endpoints remain valid, but custom target URLs must not create an internal-service scanning feature. Require public literal IPs/public-domain names and restrict non-public/special-use addresses. Because remote proxy DNS can resolve a hostname differently, local DNS validation alone is insufficient. Resolve and validate target addresses, then tunnel to the validated public IP while retaining the original HTTP Host and TLS ServerName for custom-target probes only. This is an explicit remote-DNS exception for this new feature; existing availability/speed transports retain their behavior. Validate each attempt; no redirect bypass. Block loopback, link-local, multicast, CGNAT, private, unspecified, documentation and other special-use ranges for both families. Test using injected resolvers/CAs, not production bypass flags.

Pinned clients are origin-bound: reject any request with a different scheme/host/effective port; do not return a general-purpose client that can be repurposed to a second URL. The total deadline includes DNS resolution and is propagated through connect/TLS/read. Re-resolve/revalidate on every new attempt/connection. Verify certificates for the original DNS hostname; for IP literals validate IP SANs and do not require an SNI DNS extension. The same origin/deadline/certificate rules apply to direct webhooks.

Target results have independent healthy/unhealthy/indeterminate state, expected/actual status, timings and sanitized failure code. A target failure never marks the whole proxy down. 429 pauses that target definition globally using Retry-After; no retry through other exits to evade it.

Extend jobs with `target_id` and a third kind `target`; active identity is `(proxy, kind, target)` for target checks and the current `(proxy, kind)` for other kinds. Maintain one running job of any kind per proxy. At most one active job per assignment, with bounded target workers (default 2, range 1-5). Order eligible work by due time without starvation across kinds. Monitoring pause suppresses all scheduled kinds; manual target checks remain allowed. Deleting/disabling assignments removes pending scheduled jobs and safely invalidates running completion.

## D. Policies, comparison and automation

### Notification policies

Global policy with optional per-proxy override; no group precedence in this release. Conditions: existing primary down/recovery, sustained down duration, target failure/recovery, family failure/recovery, latency threshold, valid speed threshold, IP/country/ASN change. New conditions default off, preserving existing Telegram behavior.

Numeric thresholds are explicit per rule, not guessed automatically. Latency/speed/target/family alerts default to three consecutive eligible bad samples and two eligible good samples for recovery; low-confidence speed samples are ineligible. Unknown/stale/paused observations neither assert health nor satisfy a sustained condition. Hysteresis and repeat cooldown (default 30 minutes, minimum 1 minute) prevent flapping; reminder repeats default off.

Quiet hours: one named IANA timezone, optional daily start/end, cross-midnight supported. Suppress sends, not events/history; at the end send one current-state summary per affected proxy rather than stale transitions. Persist evaluation state, pending deadlines and last sent IDs across restart. Duration rules require continued evidence within the freshness bound. Suppression does not consume delivery attempts. Editing/disabling a rule reconciles pending notifications. Alert policies control delivery, not whether primary incidents/history are recorded.

Separate rule evaluation from delivery. Logical alert IDs and event IDs are stable. Delivery remains at-least-once; no exactly-once claim.

### Comparison

Select 2-20 proxies and a period up to 365 days, bounded by retained data. Show sampled availability, eligible sample counts, median/p95 successful complete-check duration, speed median by direction, incidents, observations/gaps and payload traffic. Use nearest-rank quantiles (`ceil(p*n)`, 1-based). Empty sets are null; do not show zero for no samples. Separate method/provider/sample-size cohorts; exclude low-confidence/partial speed samples from aggregates but show exclusions. No universal hidden score or claim of exact uptime. A default comparison is a table; charts are secondary.

Use the new trace total for complete-check latency, including failed fallback attempts. Existing successful `Availability.Duration` is the successful-attempt duration only; label it as a legacy cohort and do not combine it with full-check totals. Show cohort/sample exclusions explicitly.

### Read-only tokens

Administrator creates at most 20 tokens with name, expiry (1-365 days) and exactly five allowed scopes: `read:proxies`, `read:history`, `read:events`, `read:metrics`, `read:export`. Random 256-bit secret shown once; digest-only storage, expiry and immediate revocation, last-used time. Bearer auth only; never query parameters. Explicit read-route allowlist; no settings/auth/backup/token-management/mutation access and no access by merely using GET. Export POST requires `read:export` and is the only allowed read-only POST; response uses the normal export allowlist.

Session-based mutations keep CSRF/Origin protection. Tokens do not replace sessions on administrator routes. Changing/resetting the administrator password revokes API tokens as well as sessions; the UI and CLI must explain this consequence. API limit per token: 60 requests/minute, burst 10; at most one concurrent export per token. Return 429/Retry-After. Exports and history retain existing caps. Document that proxy names/addresses/IPs remain sensitive read data.

### Webhooks

At most five destinations, disabled by default. HTTPS public URLs only, port 443, no credentials/query/fragment, same DNS pinning/special-address protection as custom targets; direct delivery is explicit and independent of monitored proxies. Secret signing key is encrypted at rest, never returned after creation. Payload uses a versioned structured allowlist, never arbitrary event messages.

Headers: stable event ID, delivery timestamp, `X-ProxyWard-Signature: sha256=<hex HMAC-SHA256(timestamp + '.' + raw_body)>`. Preserve event ID/body across retries, refresh delivery timestamp/signature per attempt. Receivers should reject signatures older than five minutes and deduplicate event IDs. Timeout 5 seconds, response body cap 4 KiB, no redirects; any 2xx succeeds. Retry network/408/429/5xx up to five attempts using existing backoff and Retry-After; other 4xx are final. Delivery rows are per `(event, destination)` so one blocked webhook cannot block Telegram or another destination. Deletion disables/cancels unsent deliveries; key/URL changes invalidate old queued destination versions.

### Prometheus

Optional `/metrics`, disabled by default, protected by `read:metrics` bearer token. Serve cached/database observations only, never generate checks on scrape. Bounded labels: proxy ID, kind, family, state; no names, addresses, URLs, tokens or error messages. Provide last observed timestamp, state one-hot, last valid duration/speed, queue size, worker activity, observed traffic totals and delivery counts. Missing measurements are omitted and accompanied by observation timestamps, not fabricated zeros. Limit concurrent scrapes to two and fail quickly if the snapshot is unavailable. No required Prometheus deployment.

Counters are durable monotonic ledger/delivery totals, not counts of retained history rows. Define gauges for current/last-known state and freshness timestamps; never export last-known health as proof of current freshness. API read auth can accept sessions on approved JSON routes, but metrics explicitly uses bearer-only middleware. Application readiness includes persistent worker failure, not just a reachable database.

## UX structure

### User, job and interface promise

The administrator needs to notice a broken route, locate its cause, compare usable alternatives and change monitoring without accidentally spending traffic or losing context. Success is a quick, explainable answer to "what is broken, since when, and what should I inspect next?" More visual richness must come from useful hierarchy and observations, not decorative charts or fabricated statistics.

The existing interface uses a compact top bar, a full-width table, sparse detail facts, small SVG charts and a growing settings modal. The user has explicitly rejected the current visual result. This design therefore does not constrain the redesign to reskinning the current shell. The earlier compact-navigation decision is not silently superseded: a new navigation choice needs approval.

### Directions to present before implementation

1. **Recommended: Operations Workspace.** A composed overview with a health band, a dominant incident/coverage timeline, actionable problem list and compact proxy table. On wide screens evaluate a quiet grouped navigation rail; on narrow screens use a small primary navigation plus an explicit More destination. Strong information hierarchy and layered slate/blue surfaces; no neon terminal aesthetic. Best for diagnosis and future feature growth; costs more layout work.
2. **Analytics Console.** Keep top navigation; lead with comparable period summaries, richer charts and comparison views. Better for trends; risks hiding the immediate failing proxy behind analytics.
3. **Refined macOS Utility.** Preserve the current compact navigation and familiar table workflow, but substantially improve typography, row composition, surfaces, density and contextual details. Lowest retraining cost; less structural change than the user may want.

Show the same realistic data in all concepts so visual preference is not confused with different features. **User selection:** on 2026-11-03 the user chose concept A (Operations Workspace), confirmed the dark theme shown in the preview is acceptable, explicitly confirmed the desktop sidebar, and accepted the general direction of the overview, proxy list, proxy detail, events and settings screen previews. Preserve light/dark/system theme choices; mobile navigation still needs visual review. The user accepted the shown visual direction, not the complete design specification or implementation plan; execution has not been approved.

### Information architecture and screen inventory

- Primary: Overview, Proxies, Targets, Events/Incidents and Compare. Use no more than these five primary destinations; Settings is a clearly labeled secondary destination.
- Overview: current counts including paused/stale/unknown, active incidents, observation coverage, recent exit changes, queue/provider/budget restrictions. Counts filter the relevant list; no nonfunctional decorative tiles.
- Proxies: dense or comfortable row spacing, pinned identity/status where needed, column visibility, tag/saved-view filters, short history strips, family indicators, freshness and contextual bulk actions. Load mini-history in one bounded batch endpoint, never one request per row.
- Proxy detail: identity/status/freshness header and Overview/History/Diagnostics/Targets tabs; exit changes, timeline and next-run reasoning close to actions. Advanced values are secondary, not all hidden in tooltips.
- Events/Incidents: timeline and list views with type/proxy/time filters, recovery linkage, delivery status and export. Keep sample-derived episodes visually distinct from threshold incidents.
- Targets: definition/assignment management and a proxy-by-target status matrix with accessible table/list fallback.
- Compare: side-by-side evidence and explicit sample/cohort caveats; no hidden overall score.
- Settings: grouped Monitoring, Notifications, Budgets, Integrations, Appearance and Account sections. Recommend a full settings page now that content is much larger, with deep links and unsaved-change protection; confirm this instead of silently restoring the old settings route.

### Flow and state model

Overview -> actionable problem -> filtered proxies/incident -> detail diagnostics -> manual check or configuration -> immediate queued/saved feedback -> refreshed evidence. Preserve filter, range, selection and scroll context when returning. Destructive and high-traffic actions show scope/consequences before confirmation; editing survives a failed request.

Every new screen has loading, empty, partial, error/retry, permission-denied, stale, offline, interrupted and overflow states. A paused proxy keeps its last result, explicitly labeled as historical. Auto-refresh must not steal focus, reset selections, collapse expanded rows or move the row currently being acted on; a visible update indicator is preferable to disruptive resorting.

### System and visual requirements

- Extend the existing token system rather than maintaining a parallel one. Primitive/semantic colors, spacing, typography, radii, elevations, chart series, focus and motion are documented before screen styling.
- Spacing ladder: 4/8/12/16/24/32/48/64/96 px equivalents in rem. Control heights 32/40/48 px; primary touch targets at least 44 px. Body text 16 px; compact metadata at least 12 px and not the sole source of critical status.
- Preserve the existing system-font stack unless an approved concept justifies a locally bundled, licensed alternative. No CDN fonts/assets. Tabular numbers for changing measurements, monospace only where useful for addresses/codes.
- Neutral layered surfaces, one principal action accent and semantic status colors. A subtle atmospheric treatment is optional in shell chrome, not under dense data. One signature visual: a readable evidence/coverage timeline, not a marketing hero.
- Light and dark are equally complete. Body contrast at least 4.5:1 and essential non-text UI contrast at least 3:1. Status meaning always has text/shape as well as color.
- Shared Button, Field, Badge, Stat, Toolbar, DataTable, EmptyState, ErrorState, ChartFrame, Timeline, DetailHeader and navigation patterns; extract repeated patterns, not an abstract framework. Charts include textual summaries/table alternatives and distinguish gaps, zeros and missing values.
- Motion is purposeful and short (normally 150-250 ms); polling numeric changes do not bounce/flash, no fake progress, honor reduced motion. No decorative animations to make a sparse screen feel busy.
- Keyboard operation, visible focus, light/dark, 375 px width, 200% zoom, long names and 100-proxy datasets are acceptance gates. Add automated accessibility checks, but do not claim WCAG conformance from them alone.

### Design gates

UX1: rendered baseline audit and concept selection (concept A and desktop sidebar selected; complete remaining screen/mobile reviews). UX2: approved tokens/components and shell/overview. UX3: redesign each feature slice and final cross-screen verification. Keep reliability fixes independent so waiting for aesthetic approval does not delay correctness work. Record Before/After/Why and screenshots at each gate; mock data is labeled as mock data and unimplemented functionality is never presented as working.

No localization system is introduced. English-only project content remains a hard constraint.

## Migration and operational gates

- Never edit migrations 001-006. Each implementation task adds the next unused numbered migration; draft names in the plan are suffixes, not permission to collide with a concurrent migration.
- Rebuilding jobs tables must preserve IDs, FKs, indexes, running/pending states, cancel flags and backup compatibility. Run `foreign_key_check` and `integrity_check` on migrated fixtures.
- Retain completed job rows referenced by retained results; new result FKs use restrictive deletion, never CASCADE/SET NULL for idempotency identity. Prune results before unreferenced jobs; terminal job status prevents completion replay even after its result was pruned. Rebuild migrations must be tested with foreign keys enabled and populated child tables, avoiding DROP-triggered cascades (create/copy/validate in an explicitly controlled migration connection).
- Keep v1 export available and make all new DTO fields additive except explicitly versioned v2 payloads. Baseline null semantics remain intact.
- New features ship disabled or with conservative defaults; upgrade never schedules new external traffic without an explicit action.
- Update pruning/backup/restore for every new table. Preserve necessary policy/accounting/current-state data independently of result history.
- CI builds the embedded UI before browser tests, installs the matching Chromium, uses the existing browser-channel override and runs Linux race tests.
- Acceptance: migrate a representative v006 database, restore an old backup, verify secrets and existing schedules, run offline integration/load tests at 100 proxies/20 targets, then separately authorized bounded live tests.

## Approval questions consolidated for review

The plan supplies proposed answers so planning can proceed without a long questionnaire: incremental releases; reset changed intervals from change time; tags as groups; UTC job-start-attributed budgets for speed payload only; public HTTPS custom targets with DNS pinning; independent optional family probes; no automatic proxy switching; token-gated setup while preserving current bind configuration. Contract/release approval permits Release A without waiting for aesthetic selection. Concept A and the desktop sidebar were selected; mobile navigation, remaining screen designs and the proposed structured settings page still require review. Endpoint availability and real provider quotas require evidence during live acceptance, not an invented guarantee.
