# Reliability, Monitoring Expansion and Redesign Implementation Plan

> **For agentic workers:** Use superpowers:executing-plans to implement this approved plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Fix the six review findings and deliver all ten functional additions plus a complete, user-approved redesign without losing existing data or increasing external traffic silently.

**Architecture:** Keep the current Go/SQLite service and React application. Establish transactional job lifecycle and observation primitives before adding bounded target jobs, policies and integrations. Ship four independently usable releases; UI redesign is a cross-cutting track with its own approval gates, not a rewrite prerequisite for bug fixes.

**Tech Stack:** Go 1.27.0, modernc SQLite, React 19, strict TypeScript, Vite, Vitest, Playwright, Debian Docker. Reuse current dependencies by default; any accessibility-test dependency must be explicitly pinned and reviewed during implementation.

**Spec:** `docs/superpowers/specs/2026-11-03-reliability-and-monitoring-design.md`

## Global Constraints

- **Execution approved (2026-11-03).** The user explicitly approved this staged plan. Offline fixture tests are authorized; deployment and real provider traffic remain out of scope without separate authorization.
- English-only UI, documentation, identifiers, comments, tests, validation and errors. Conversation remains Russian; user data remains unrestricted.
- Up to 100 proxies. One container, one administrator, SQLite, existing TLS/no-direct-fallback protections.
- New features ship disabled or with conservative defaults; upgrade never schedules new external traffic without an explicit action.
- Never edit migrations 001-006. Add the next unused number; migration suffixes below are proposed names, not fixed numbers.
- Keep v1 export available and make all new DTO fields additive except explicitly versioned v2 payloads.
- Tests are offline fixtures by default. Live acceptance is separate, bounded and explicitly authorized.
- Each task: red regression/contract tests -> smallest implementation -> green scoped checks -> review -> focused commit when authorized. Do not commit unrelated work.
- Update `STATUS.md`, `TODO.md`, relevant docs and application version for shipped behavior; planning alone does not change the application version.

## Review Focus

1. Concurrent settings/claim/cancel/outcome writes must not allow stale scheduled work, lost cancellation or duplicate incidents (Tasks 1-3, 8, 17).
2. Retention, sparse/manual observations and restart gaps must not fabricate recovery, precise downtime or family/IP transitions (Tasks 10-13, 20-21).
3. Remote DNS, redirects and special-use IPv4/IPv6 must not bypass target/webhook destination restrictions (Tasks 16, 23).
4. Cancellation/crash/month rollover must not erase payload usage, free an uncertain reservation or stall availability behind blocked speed work (Task 15).
5. Old frontend responses, polling, long names, narrow screens and expired permissions must preserve context and expose recovery (Tasks 4-5, UX1-UX3, 18, 22).

## Plan progress

- [x] Inspect current source, schema, tests and project decisions.
- [x] Capture full scope and proposed behavioral contracts in the linked design.
- [x] Write staged tasks, test gates and dependency map.
- [x] Incorporate design/buildability review, including cancellation release coupling, schedule revisions, quota identities, origin-bound transport, budget attribution, FK retention and metrics cohorts.
- [x] Obtain user approval of contracts and release approach (2026-11-03).
- [x] Select visual direction A, its dark palette and desktop sidebar (2026-11-03); remaining screen/mobile designs and complete specification still await approval.
- [x] Execute approved release tasks (in progress; see task checkboxes and SDD ledger).

## File/responsibility map

Existing owners remain: `internal/store` transactions/migrations/read models; `internal/jobs` queue/scheduler/runner; `internal/proxy` dialers; `internal/check` measurements; `internal/providers` adapters; `internal/notify` rules/delivery; `internal/httpapi` authenticated DTOs/routes; `internal/app` lifecycle; `web/src/features` screens.

New focused units are proposed in task file lists. Put shared structs in `internal/domain`; do not import `httpapi` into services or pass raw SQL transactions across package boundaries. New read DTOs belong in the store/domain read model with explicit secret-free HTTP mapping. Every persistent feature updates `internal/store/prune.go` and backup/migration fixtures in the task that adds it.

## Dependencies and release boundaries

```text
A: 1 -> 2 -> 3 -> 8; 4, 5, 6, 7 are independent after baseline
UX1: audit/concepts can accompany A; selection precedes UX2
B: 8 -> 9 -> 10 -> 11; 8 -> 12 -> 13
C: 1-3 -> 14; 8 -> 15; 9 + 16 -> 17 -> 18
D: 8 + 10-12 + 17 -> 19 -> 20
   9 + 12 + 15 -> 21; 7 -> 22; 16 + 19 + 22 -> 23; 15 + 22 + 23 -> 24
UX2 -> UX3 applied to each feature slice, not postponed to the end
25: all approved tasks and UI gates
```

Schema-changing tasks run sequentially even when their business logic is independent. Tasks 3 and 8 are one indivisible cancellation/correctness release gate; Task 3 must not ship alone against the split-write runner. Task 17's jobs-table rebuild must preserve Task 3/8 columns/indexes. Reliability fixes may ship after contract approval and before UX approval. Permission to create concept previews is separate from approval of the selected concept. No deadline/cost estimate is promised without measuring the first release.

## UX1: Baseline audit and visual concept approval

**Files:** Read `web/src/app/App.tsx`, `web/src/features/**/*`, `web/src/styles/{tokens,global}.css`; Create `docs/design/2026-11-03-redesign-brief.md`; approved preview/evidence artifacts under `docs/design/` or ignored `.e2e/` as appropriate.

**Interfaces:** Consumes actual monitoring states and the spec's UX structure. Produces a selected direction, navigation map, screen-state inventory, desktop/mobile wireframes and named token/component brief. This is a mandatory approval gate, not authorization to change screens.

- [ ] Capture the existing rendered Proxies, detail, Events and Settings at 1440x900 and 375x812, both themes; include a slow/error state and 100-proxy fixture. Record usability problems separately from aesthetic preference.
- [x] Show interactive browser previews of Operations Workspace, Analytics Console and Refined macOS Utility using labeled fixture data. User selected A, accepted its dark theme and confirmed desktop sidebar; these previews showed overview concepts only.
- [x] Show Proxies, detail, Events and structured settings in selected direction A; user accepted the shown direction (2026-11-03).
- [ ] Show mobile examples and confirm mobile navigation, density and remaining screen details with the user.
- [ ] Produce screen-state and interaction specifications; preserve no-observation/stale distinctions and real action availability.
- [ ] Obtain explicit design approval. Verify the brief names an approved direction and contains no unresolved product-semantic conflicts before UX2.

## Release A — Reliability first

### Task 1: Reconcile changed schedules transactionally

**Files:** Modify `internal/store/{appsettings,proxy,jobs}.go`, `internal/jobs/{scheduler,service}.go`, `internal/httpapi/{settings,proxies}.go`; Create `internal/store/schedules.go`, next `*_schedule_revisions.sql`; Tests `internal/jobs/scheduler_test.go`, `internal/store/jobs_test.go`, `internal/httpapi/proxies_test.go`.

**Interfaces:** Preserve existing `PutSettings`, `UpdateProxy`, `SetMonitoringPaused` callers; add `Store.ReconcileSchedules(ctx context.Context, now time.Time) error` and transaction-local effective-schedule helpers. Persist per-kind schedule revision and capture it in `domain.Job.ScheduleRevision int64` at claim. Inject clock/jitter as needed without adding network work to store operations. Global/per-proxy/bulk writers use the same helpers.

- [x] Add `TestIntervalChangeResetsNextRunFromChangeTime`: enabled 7-day interval -> 1-minute interval at time T yields next=T+1m; reverse change yields T+7d. `TestUnchangedOverridePreservesNextRun` asserts unaffected overrides/manual jobs stay unchanged. `TestOldCompletionPreservesNewScheduleRevision` changes the interval while a job runs and asserts its later completion cannot overwrite the newly chosen next run.
- [x] Run `go test ./internal/jobs ./internal/store ./internal/httpapi -run 'TestIntervalChange|TestUnchangedOverride' -count=1`; observe failure against the old behavior.
- [x] Implement the spec's interval-change, enable/disable/pause rules in short write transactions. Remove snapshot-based writes from scheduler paths; no stale tick may clear newer next runs.
- [x] Run scoped packages and `go test ./...`; pass both interval directions, inherit/null overrides and paused/resumed cases.
- [x] Review and commit only this slice, with durable schedule semantics recorded in `DECISIONS.md` after approval.

### Task 2: Prevent scheduled claim/enqueue after disable

**Files:** Modify `internal/store/jobs.go`, `internal/jobs/scheduler.go`; Tests `internal/store/jobs_test.go`, `internal/jobs/scheduler_test.go`, `internal/httpapi/proxies_test.go`.

**Interfaces:** Existing `EnqueueJobs`/`ClaimJob` signatures remain; scheduled operations consult current effective schedule in their transaction. Manual calls retain their behavior.

- [x] Add deterministic barrier tests `TestStaleTickCannotEnqueueDisabledSchedule`, `TestOverrideDisableRemovesPendingBeforeResponse`, and `TestStaleReconcileCannotEraseNewNextRun`. Assert zero scheduled claims after disable commits, while manual and previously running jobs survive.
- [x] Run `go test ./internal/store ./internal/jobs ./internal/httpapi -run 'TestStale|TestOverrideDisable' -count=1`; confirm old behavior fails without sleeps/probability loops.
- [x] Re-check effective enabled/pause/due inside enqueue and claim; atomically invalidate pending scheduled jobs when changing schedules. Replace stale reconciliation removal logic with current-state evaluation.
- [ ] Run those packages; on approved Linux CI run `go test -race -count=10 ./internal/jobs ./internal/store`. Windows full and scoped checks pass; Linux race remains pending.
- [x] Review and commit; document which running work may finish after disabling.

### Task 3: Durable cancellation intent

**Files:** Create next migration `*_job_cancellation.sql`; Modify `internal/domain/job.go`, `internal/store/jobs.go`, `internal/jobs/{queue,runner}.go`, `internal/httpapi/jobs.go`; Tests existing jobs/runner/API tests.

**Interfaces:** Add `CancelRequestedAt *time.Time` to `domain.Job`; `Store.RequestJobCancel(ctx context.Context, id int64, now time.Time) (domain.Job, error)`. Keep `CancelRunning` as best-effort immediate wakeup only, not the source of truth.

- [x] Add `TestCancelBetweenClaimAndExecute`, `TestCancelSurvivesRunnerRegistration`, `TestCancelAfterCompletionReturnsConflict`, `TestCanceledScheduledOccurrenceAdvancesUnchangedSchedule`, `TestCanceledManualPendingPreservesSchedule`, and restart tests. Assert accepted cancel creates no result or incident; finished outcome returns 409. Re-run final no-result assertions with Task 8 before releasing either task.
- [x] Run `go test ./internal/jobs ./internal/store ./internal/httpapi -run 'TestCancel' -count=1`; establish the missing-intent failure.
- [x] Persist cancellation before signaling; execute checks intent before network work. Add an outcome-commit guard used by Task 8. Preserve interrupted status on shutdown.
- [x] Run migration and job suites, including v006 upgrade and both transaction orderings (plus a concurrent race invariant test).
- [ ] Review and commit. No automatic retry or success history for canceled work.

### Task 4: Events pagination generations and continuity

**Files:** Modify `web/src/features/events/EventPage.tsx` and its tests; add `web/e2e/events-pagination.spec.ts`.

**Interfaces:** Keep `listEvents(filters, signal)` API. Bind head/tail/cursor/loading state to one immutable filter generation; pagination shares cancellation/invalidation semantics with polling.

- [x] Add deferred-response regressions for stale load-more after filter change, head/tail continuity, and a poll response racing a completed tail load.
- [x] Run the Events tests and reproduce all three old-behavior failures before implementing the guards.
- [x] Abort/invalidate old loads, reject stale results/cursors, preserve displaced events, and deduplicate IDs. Loading state is scoped to its request generation.
- [x] Run target tests (11 passing), full unit tests (152 passing with a temporary local Vite filesystem allowlist for the pre-existing external node_modules junction), typecheck and build. Playwright setup and UI fixtures launch; the isolated flow test is not independent and times out because earlier flow cases have not enabled “Check all proxies”. No pagination-specific Playwright scenario was run locally.
- [x] Review and commit Task 4 separately.

### Task 5: Recoverable settings/chart loading

**Files:** Modify `web/src/features/settings/SettingsPage.tsx`, `web/src/features/proxies/ProxyDetail.tsx`; Tests settings tests, Create `web/src/features/proxies/ProxyDetail.test.tsx`; browser error-recovery coverage.

**Interfaces:** Existing `getSettings(signal)`/chart endpoints remain. Each independent data region exposes loading/error/retry/last-good-data without resetting forms or other successful regions.

- [x] Add tests for initial Settings 500 -> visible alert/Retry -> success; polling 500 retains data and edits; one chart failure does not hide the other; range change cannot label the old chart as new.
- [x] Run `npm --prefix web test -- src/features/settings/SettingsPage.test.tsx src/features/proxies/ProxyDetail.test.tsx`; confirmed the missing error/retry behavior failed first.
- [x] Handle errors in polling callers, retry explicitly, and invalidate chart requests/data per proxy/range. Distinguish unavailable from a genuine empty history.
- [x] Run frontend tests/typecheck and browser 500/offline/recovery cases. Require no indefinite loading without recovery controls.
- [x] Review and commit.

### Task 6: Availability cooldowns

**Files:** Modify `internal/check/{availability,errors}.go`, `internal/providers/providers.go`, `internal/app/app.go`, `internal/httpapi/settings.go`; reuse `internal/store/speed.go` provider-pause primitives; Tests availability/provider/store/API suites.

**Interfaces:** Reuse the existing `check.PauseStore` read/type contract from `internal/check/speed.go`; add `check.AvailabilityPauseStore` embedding it with `ExtendProviderPause(ctx context.Context, provider, reason string, until time.Time) error`, implemented by store as monotonic max-expiry/serialized backoff update. Add `Pauses AvailabilityPauseStore` to `check.Checker` and explicit provider quota identity (ipify endpoint variants share `availability:ipify`). Do not clear availability pauses automatically on success or use endpoint IDs as quota keys.

- [ ] Add `TestAvailabilityRetryAfterSkipsProviderUntilDeadline`, `TestAllPausedIsNotDown`, `TestPausedProviderDoesNotTurnOtherTimeoutIntoAllProviderDown`, `TestIpifyVariantsShareQuota`, `TestConcurrentPauseCannotShortenDeadline`, `TestLateSuccessCannotEraseNewPause`, seconds/date/invalid header tests and reopen persistence.
- [ ] Run `go test ./internal/check ./internal/providers ./internal/store -run 'TestAvailabilityRetryAfter|TestAllPaused|TestPausedProvider' -count=1`; verify failure of the old implementation.
- [ ] Persist global provider pauses, skip ineligible sources, use injected time for Retry-After and fail conservatively on pause-store errors. Do not count skipped providers as timed-out attempts or bypass the pause manually.
- [ ] Run backend suites and add API assertions for secret-free cooldown/reason/expiry fields.
- [ ] Review and commit; preserve fallback and existing all-eligible-providers timeout semantics.

### Task 7: Bootstrap token and deployment truth

**Files:** Create `internal/auth/bootstrap.go`, `internal/store/bootstrap.go`, next `*_bootstrap_token.sql`; Modify `internal/httpapi/auth.go`, `cmd/proxyward/main.go`, `web/src/features/auth/SetupPage.tsx`, `start.sh`, `compose.yaml`, `.env.example`, `README.md`, `docs/operations.md`; tests in auth/store/httpapi/app and frontend setup tests.

**Interfaces:** CLI `proxyward admin setup-token`; `Store.CreateSetupToken(ctx context.Context, digest string, expiresAt time.Time) error`; atomic `CreateAdminWithSetupToken(ctx context.Context, tokenDigest, passwordHash string, now time.Time) (bool, error)`; setup body `{password, setup_token}`. CLI reset remains token-free and revokes bootstrap capability.

- [ ] Add tests for no/wrong/expired/reused token, two competing valid setup requests (exactly one winner), no token in logs/session/API responses, old initialized database unchanged, and CLI set-password invalidating setup.
- [ ] Run focused auth/setup tests; see unauthorized first-visitor setup fail the new expectations.
- [ ] Implement digest-only, 256-bit, 30-minute single-use token workflow and explicit setup instructions. Preserve BIND_ADDRESS; correct Compose claims and warn about exposed plain HTTP.
- [ ] Run backend/frontend suites and local fixture first-run acceptance. Validate read-only container permissions and upgrade behavior without opening a real deployment.
- [ ] Review and commit; update setup documentation and screenshots after UX approval.

### Task 8: Atomic outcomes and CI regression gate

**Files:** Create next `*_job_result_identity.sql`, `internal/store/completion.go`; Modify `internal/jobs/runner.go`, `internal/store/{history,speed,incidents,jobs,prune}.go`, `internal/notify/incidents.go`, app readiness handling and `.github/workflows/ci.yml`; Tests completion, runner, incident, retention and export suites.

**Interfaces:** Define `domain.AvailabilityCompletion` and `domain.SpeedCompletion` containing job ID/result/completion time/claimed schedule revision, not stale schedule settings. `Store.CompleteAvailability(ctx context.Context, c domain.AvailabilityCompletion) (IncidentOutcome, error)` and `Store.CompleteSpeed(ctx context.Context, c domain.SpeedCompletion) error`; read current effective settings in the write transaction and advance next run only for the matching revision. Idempotent by job ID and terminal job state. Transaction-local helpers prevent nested write locks and `store` must not import `notify`.

- [x] Add rollback/idempotence/cancel-order/retained-identity/failure/revision tests and a cross-table cancellation/completion race invariant. Inject failure at outbox and job-finalization points and assert complete rollback. After pruning an old terminal result and its job identity, replay cannot recreate it.
- [x] Run scoped completion/cancellation packages and the full Go suite; repeat store/jobs tests 10 times.
- [x] Switch production runner to one transaction for result/incident/event/outbox/job/next-run, guarded by cancellation and schedule revision. Keep referenced jobs until results are pruned (restrictive FKs); never delete identity via CASCADE/SET NULL. Retry failed commits at most three times with bounded backoff; persistent failure stops claims and exposes readiness failure, never marks done. Add local-fixture Playwright CI using the pinned Playwright package/Chromium and `E2E_BROWSER_CHANNEL=chromium`.
- [ ] Run final acceptance; Windows Go suite/build/vet and repeated store/jobs tests pass. Linux `-race` and browser CI have not run in this local session.
- [ ] Release A review/commit; reconcile stale STATUS/TODO claims, removing obsolete keyed-geo acceptance requirements without claiming live tests were done.

## UX2: Approved visual system, application shell and Overview

**Files:** Modify `web/src/styles/{tokens,global}.css`, `web/src/app/{App,App.test}.tsx`, `web/src/components/*`; Create `web/src/features/overview/{OverviewPage,OverviewPage.test}.tsx`, focused shared components under `web/src/components/`, `internal/store/overview.go`, `internal/httpapi/overview.go`; tests and approved visual baselines.

**Interfaces:** `GET /api/overview?window=24h` returns one bounded snapshot: counts, current incidents, queue/cooldowns and existing observation summaries; later tasks add fields. It never probes. Create reusable semantic UI patterns named in the spec; choose final layout only after UX1 approval.

- [ ] Translate the approved direction into token roles, component states and responsive layouts. Name every intentional off-grid exception; keep existing font/licensing constraints unless the user approves a change.
- [ ] Add failing component/API tests for counts that reconcile, actionable filters, empty/error/partial states, keyboard navigation, paused/stale distinction and zero hidden network probes. Add screenshot assertions using deterministic fixtures, not current timestamps.
- [ ] Implement approved shell/Overview and shared patterns; migrate existing screens incrementally, keeping deep links and functional controls. Do not display placeholders as implemented metrics.
- [ ] Run unit/typecheck/build/browser tests; inspect desktop/mobile/light/dark/200%-zoom screenshots and keyboard behavior. Use a pinned accessibility checker after dependency review; manual review remains required.
- [ ] Present Before/After/Why for user acceptance. Commit the approved slice; no unrelated backend refactor.

## Release B — Explain what happened

### Task 9: Structured stage/attempt diagnostics

**Files:** Create `internal/domain/trace.go`, `internal/proxy/trace.go`, `internal/store/traces.go`, next `*_check_traces.sql`; Modify `internal/proxy/{transport,socks5}.go`, `internal/check/availability.go`, `internal/httpapi/dashboard.go`; tests in transport/check/store and Create `web/src/features/diagnostics/*`.

**Interfaces:** `domain.CheckTrace` owns nullable stage durations and attempt metadata; `proxy.WithTrace(sink func(domain.CheckTrace)) Option`; completion bundles carry traces. `GET /api/proxies/{id}/diagnostics?cursor=&limit=25` is snapshot/history only.

- [ ] Add tests asserting measured connect/proxy-TLS/tunnel/target-TLS/first-byte/body/total stages are monotonic, omitted stages null, failed attempts retained, parallel traces isolated and credentials absent. Assert CONNECT auth is not a fabricated standalone duration.
- [ ] Run transport/check/trace tests and confirm new contract fails before implementation.
- [ ] Instrument custom dialers plus httptrace, persist allowlisted traces atomically, add read DTOs and detail diagnostics. No network call from GET; no local target-DNS timing claim.
- [ ] Run backend/frontend suites, injected stage stalls and large trace pagination/pruning/backup tests. Verify memory is bounded by attempt count/body caps.
- [ ] Review, UX3-check and commit.

### Task 10: Independent family observations

**Files:** Create `internal/domain/observation.go`, `internal/check/families.go`, `internal/store/families.go`, next `*_family_observations.sql`; Modify providers/app/jobs/DTOs and proxy detail/table; tests and E2E fixtures.

**Interfaces:** `type IPFamily string` values `ipv4`/`ipv6`; `domain.FamilyObservation{ProxyID, JobID, Family, CheckedAt, State, ExitIP, Endpoint, Failure}`; `FamilyChecker.Check(ctx context.Context, p domain.Proxy, family domain.IPFamily, client *http.Client) domain.FamilyObservation`. Settings add opt-in `family_checks: {ipv4: bool, ipv6: bool}` with per-proxy override.

- [ ] Test v4 up/v6 failure, v6-only success, wrong-family body, all family sources paused, one ambiguous source failure, primary incidents unchanged, old history not backfilled and upgrade generating no extra calls.
- [ ] Run new family/store/API tests; observe missing fields/behavior fail.
- [ ] Run optional probes sequentially under availability's proxy lock; persist family observations in completion, separate timestamps/provider health and render family status/freshness. Verify production family-only endpoint behavior in separately authorized bounded probes before enabling defaults.
- [ ] Run fixtures for HTTP/HTTPS/SOCKS5 with family responses and no-direct-fallback checks. Record unverified production IPv6 behavior explicitly, not as supported-by-evidence.
- [ ] Review, UX3-check and commit.

### Task 11: Exit history and enrichment changes

**Files:** Create `internal/domain/exits.go`, `internal/store/exits.go`, next `*_exit_observations.sql` including geo provenance, `internal/httpapi/exits.go`; Modify `internal/providers/geosources.go`, domain/geo/store geo models, completion/geo wiring/events UI; tests in store/geo/httpapi plus detail tests.

**Interfaces:** `Store.ListExitChanges(ctx context.Context, id domain.ProxyID, scope string, cursor string, limit int) (domain.ExitChangePage, error)`; `GET /api/proxies/{id}/exit-changes?scope=primary|ipv4|ipv6`. Transaction-local observation comparison emits stable structured change events once.

- [ ] Test first observation/no event, same IP/no event, A->B/one event, failure/no replacement, concurrent retry/no duplicate, separate family scopes, rapidly rotating exits, delayed unknown/stale geo and comparable country/ASN changes. `TestCrossFamilyGeoDisplayDoesNotProduceChangeAlert` preserves existing display behavior but rejects cross-family/legacy provenance as exact enrichment evidence.
- [ ] Run the focused exit/geo tests to establish absent history/event contract.
- [ ] Persist observations/change identity during completion; save requested/returned IP, provider and exact-match provenance on enrichment, without silently changing the existing cross-family display contract. Compare only confirmed adjacent same-scope exact enrichments; old unproven cache rows stay unknown for alert comparison. Render old/new addresses and dates; never treat a changed geo provider as an exit change.
- [ ] Run retention/reopen/export/secret-canary/API tests and a browser rotation scenario.
- [ ] Review, UX3-check and commit.

### Task 12: Incident/episode timeline, coverage and freshness

**Files:** Create `internal/domain/timeline.go`, `internal/store/timeline.go`, `internal/httpapi/timeline.go`, next `*_monitoring_boundaries.sql`, `web/src/features/history/Timeline.tsx` and tests; Modify scheduling/app lifecycle/dashboard/detail.

**Interfaces:** `Store.ProxyTimeline(ctx context.Context, id domain.ProxyID, from, to time.Time) (domain.Timeline, error)`; GET `/api/proxies/{id}/timeline?from=&to=` maximum 365 days, segments capped at 500 with explicit coalescing metadata. `domain.Timeline` distinguishes threshold incidents, observed failure episodes, gap/paused segments and sampled summaries.

- [ ] Test predecessor/successor boundaries, first/last retained row, indeterminate between down/up, pause/resume, service restart, interval changes, manual-only sparse checks, no samples and gap threshold `max(2*interval, interval+60s)`.
- [ ] Run timeline/store/API tests and assert old last-state-only presentation cannot meet the new coverage contract.
- [ ] Persist dated monitoring/service boundaries prospectively; derive segments/summary from one read snapshot. Expose freshness reason/expected time; use bounded queries and no fabricated exact SLA.
- [ ] Run backend/UI tests; inspect textual timeline alternative, 375px reflow, null/gap labeling and 100-proxy overview without N+1 requests.
- [ ] Review, UX3-check and commit.

### Task 13: Versioned export and stronger consistency tests

**Files:** Modify `internal/store/{export,export_test}.go`, `internal/httpapi/{export,export_test}.go`, `web/src/features/events/ExportDialog.tsx`, tests and README export section.

**Interfaces:** POST `/api/exports` accepts optional `schema_version` (omitted=1; 1/2 supported). V1 serialization/semantics unchanged. V2 adds spec episode fields, context/retention metadata and only feature tables already implemented; UI explicitly requests 2.

- [ ] Test byte/shape-compatible v1 fixtures, v2 recovered time versus last failure, censored boundaries, unknown version 422, combined mixed-table cap, equal-timestamp ID order, multi-table snapshot under writes and canary secrets/query strings excluded.
- [ ] Run `go test ./internal/store ./internal/httpapi -run 'TestExport' -count=1` and dialog tests; see missing version behavior fail.
- [ ] Implement explicit version-specific DTO builders sharing one snapshot/read model; cap raw records at 100,000 across arrays and disclose at most two contextual rows per selected scope. Do not alter v1 in place.
- [ ] Run Go/export/frontend/browser download tests, including real JSON inspection and retained-data boundaries.
- [ ] Review and commit. Extend v2 allowlist/tests again in later tasks that add target/accounting data.

## Release C — Control and validate routes

### Task 14: Tags as groups and safe bulk updates

**Files:** Create `internal/store/bulk.go`, `internal/httpapi/bulk.go`, `web/src/features/proxies/BulkEditDialog.tsx`; Modify ProxyPage/ProxyTable/endpoints/types; tests plus bulk E2E.

**Interfaces:** POST `/api/proxies/bulk` body `{proxy_ids, monitoring_paused?, add_tags?, remove_tags?, availability_schedule?, speed_schedule?}`; returns updated secret-free proxies. IDs unique, 1-100; omitted keeps, schedule null inherits. All-or-nothing validation/store transaction. Saved-view schema version 1 in localStorage contains only filter/sort/density/column choices.

- [ ] Test missing ID/invalid override rolls back every update; mixed overrides/pause preserve manual work; selected-hidden rows show exact preview count; tag filters and saved-view reload work; corrupted localStorage safely resets.
- [ ] Run store/API/component tests; establish missing bulk contract failures.
- [ ] Reuse Task 1 schedule helpers, explicit selection semantics and existing tag validation; add preview/confirmation and per-operation feedback. Never interpret an unselected filter as bulk authorization.
- [ ] Run concurrency and browser bulk/return-navigation tests with 100 proxies and long names.
- [ ] Review, UX3-check and commit.

### Task 15: Durable speed payload budgets

**Files:** Create `internal/domain/traffic.go`, `internal/store/traffic.go`, `internal/check/accounting.go`, `internal/httpapi/traffic.go`, next `*_traffic_ledger.sql`; Modify speed checker/runner/runnable logic/prune/settings; Create budget UI and tests.

**Interfaces:** `Store.ReserveSpeedTraffic(ctx context.Context, jobID int64, proxyID domain.ProxyID, plannedBytes int64, now time.Time) (domain.TrafficReservation, error)`; `CheckpointSpeedTraffic(ctx context.Context, reservationID, observedBytes int64) error`; `SettleSpeedTraffic(ctx context.Context, reservationID, actualBytes int64, now time.Time) error`. Add typed budget-blocked reason, GET `/api/traffic?from=&to=&proxy_id=` and validated budget settings.

- [ ] Test reservation `2*(warmup+sample)+8192+2`, overlong downloads capped at expected+one validation byte, upload acknowledgment body cap/accounting, partial transfers, cancellation, crash before checkpoint, crash after checkpoint/before settlement, duplicate settlement, concurrent global/proxy limits and blocked speed not blocking availability. Cross-midnight jobs and uncertain reservations remain charged to their original UTC reservation day/month; pruning never reduces the ledger.
- [ ] Run traffic/check/jobs tests; assert old history-only totals cannot enforce durable budgets.
- [ ] Reserve the complete spec payload allowance, enforce LimitedReader-style remaining+one-byte caps instead of full-chunk overread, account bounded acknowledgment bodies, use durable progress checkpoints, conservatively charge uncertain crash reservations and settle once. Use reservation-start UTC period attribution and disclose it in UI/docs/forecast. No manual bypass, busy-loop or unbounded pending jobs.
- [ ] Run fixture byte-count comparisons, reopen/restore tests, forecast override tests and offscreen UI recovery. Label usage as speed payload, not wire/all-traffic bytes.
- [ ] Review, UX3-check and commit.

### Task 16: Safe custom-target transport and validation

**Files:** Create `internal/domain/target.go`, `internal/check/target.go`, `internal/proxy/target.go`, tests; reuse injected fixture resolvers/CAs.

**Interfaces:** `domain.TargetSpec{Name, URL, ExpectedStatus, BodyContains, Timeout, Interval}`; `ValidateTarget(spec domain.TargetSpec) error`; `proxy.NewPinnedTargetClient(p domain.Proxy, target domain.TargetSpec, resolver domain.TargetResolver, opts ...Option) (*http.Client, error)`; `TargetChecker.Check(ctx context.Context, p domain.Proxy, t domain.TargetSpec, client *http.Client) domain.TargetResult`. `TargetResolver` is `LookupNetIP(ctx context.Context, network, host string) ([]netip.Addr, error)`.

- [ ] Test HTTPS-only/443/2048-byte URL/no userinfo-query-fragment, literal 256-byte assertion, status mismatch, exact 64KiB cap/over-limit behavior, DNS-inclusive timeout/cancel, all special-use ranges including IPv4-mapped IPv6, mixed public/private DNS answers, rebinding and redirects. Assert tunnel destination is validated public IP, original hostname certificate verification, IP SAN verification for literals and rejection of a second origin on the same client.
- [ ] Run target/transport tests and confirm missing validation/pinning contract fails.
- [ ] Implement bounded HTTPS GET and origin-bound pinned target override without changing legacy remote-DNS transports. Apply one context deadline including DNS and revalidate every connection/attempt. Reject any DNS answer set containing forbidden addresses; preserve original Host and DNS-hostname TLS verification (IP SANs/no required DNS SNI for literals). No cookies/body storage/secret-bearing errors.
- [ ] Run all three proxy protocol fixtures with TLS/Host assertions, poisoned environment proxies and direct-fallback canary. Keep test resolver injection out of production configuration.
- [ ] Security review and commit before target API is exposed.

### Task 17: Target storage, assignments and fair jobs

**Files:** Create next `*_targets_and_jobs.sql`, `internal/store/targets.go`, `internal/jobs/targets.go`, `internal/httpapi/targets.go`; Modify domain/job, store/jobs, queue/runner/scheduler/app/prune; migration and integration tests.

**Interfaces:** GET/POST `/api/targets`, GET/PUT/DELETE `/api/targets/{id}`, PUT `/api/proxies/{id}/targets` (complete validated assignment list), GET `/api/proxies/{id}/target-results`; POST `/api/jobs` accepts `kind:"target"` plus `target_ids` and explicit proxy IDs. Add nullable `Job.TargetID`, target schedule helpers and atomic `Store.CompleteTarget(ctx context.Context, c domain.TargetCompletion) error` guarded by assignment version/cancellation.

- [ ] Test 20 definitions/10 assignments per proxy caps, disabled-on-upgrade, one active job per assignment, one running job per proxy across all kinds, earlier due work not starved, scheduled pause/manual distinction, assignment edit/delete during running check, global 429 target pause and bounded backlog.
- [ ] Run target/store/jobs/migration tests; confirm old jobs CHECK/index constraints fail the new target contract.
- [ ] Rebuild jobs in a forward migration preserving IDs/FKs/cancel flags/result job references; add indexes and transactional assignment schedules. Share bounded measurement capacity (target workers default 2, range 1-5); claim oldest eligible work fairly across kinds without violating the single speed-worker rule.
- [ ] Run `foreign_key_check`/`integrity_check` on v006 and post-A fixtures with populated child tables and foreign keys enabled; prove no DROP/rebuild cascade loses results or outboxes. Test backup/restore, restrictive result-job retention, cancellation and Linux races. Verify existing IDs/history remain readable.
- [ ] Review and commit; no default assignments or automatic target traffic.

### Task 18: Targets UI, matrix and manual workflows

**Files:** Create `web/src/features/targets/{TargetPage,TargetEditor,TargetMatrix}.tsx` and tests, target-detail components, `web/e2e/targets.spec.ts`; Modify App/endpoints/types/proxy detail/export allowlists.

**Interfaces:** Consume Tasks 16/17 APIs and approved UX2 patterns. Add GET `/api/target-matrix` bounded to current 100x20 IDs/latest observations, one read request not N+1. Labels distinguish site health from primary proxy health.

- [ ] Write failing create/edit/assign/manual-check tests, invalid URL field errors, target matrix keyboard/table fallback, 429 cooldown, assignment deletion race, initial/partial/error/empty states and 375px overflow assertions.
- [ ] Run target component tests; verify new workflows fail before implementation.
- [ ] Implement approved pages, explicit traffic/timeout/interval controls and recovery actions. Preserve drafts on errors, show no status as Unknown, and expose assignment scope before enabling.
- [ ] Run Vitest/typecheck/build/Playwright; inspect matrix on desktop/mobile and verify v2 export strips target URLs/query/body data by allowlist.
- [ ] Release C review, UX3-check and commit.

## Release D — Policies, comparison and integrations

### Task 19: Persisted notification policy evaluation

**Files:** Create `internal/domain/alerts.go`, `internal/notify/policy.go`, `internal/store/alerts.go`, next `*_alert_policies.sql`; Modify completion/incident/outbox/app; tests with fake clock.

**Interfaces:** `notify.Evaluator.Evaluate(ctx context.Context, now time.Time) error`; store policy/global+proxy override read/write methods; stable logical alert/event IDs. Consume committed primary/family/target/trace/exit/speed observations. Delivery eligibility is separate from incident recording.

- [ ] Test three bad/two good eligible samples, unknown/stale not satisfying duration, low-confidence speed exclusions, sustained-down deadlines, restart idempotence, flapping repeat suppression, no default extra alerts and threshold incident history unaffected by notification disable.
- [ ] Run alert/store/notify tests; confirm missing persisted evaluator behavior fails.
- [ ] Implement bounded incremental observation cursors and deadline evaluation, default new conditions off, 30-minute repeat cooldown/default repeats off. Persist rule state and output events atomically; do not generate checks in the evaluator.
- [ ] Run canceled/interrupted exclusion, retention/current-state preservation, clock/restart and full notify suites. Verify cost is bounded by new observations/rules, not full-history rescans.
- [ ] Review and commit.

### Task 20: Policy editor, quiet hours and delivery reconciliation

**Files:** Create `internal/httpapi/alerts.go`, `web/src/features/notifications/PolicyEditor.tsx` and tests; Modify worker/outbox/settings/events; add quiet-hour/store tests.

**Interfaces:** GET/PUT `/api/notification-policies/global`, GET/PUT/DELETE `/api/proxies/{id}/notification-policy`; UTC-stored times with IANA quiet-hour zone/start/end, validated thresholds, bad/good counts 1-10, cooldown 1-1440 minutes, optional repeat; DELETE returns inheritance. Existing Telegram settings remain.

- [ ] Test cross-midnight/DST quiet hours, end-of-quiet current-state summaries, no obsolete down/recovery spam, suppression consumes no attempts, disable/edit while queued, inherited versus explicit overrides, empty/malformed rule input and delivery retry independence.
- [ ] Run policy editor/notify/API tests and see absent policy/reconciliation behavior fail.
- [ ] Implement policy UI and quiet-hours gate; reconcile pending delivery on changes, show suppressed/pending/failed/delivered separately. Ship Go timezone data via `_ "time/tzdata"` or equivalent verified container tzdata; do not assume Debian slim has IANA zones.
- [ ] Run fake-clock DST/restart tests and browser policy editing/recovery; require preserved unsaved values and clear inheritance.
- [ ] Review, UX3-check and commit.

### Task 21: Evidence-based comparison

**Files:** Create `internal/domain/comparison.go`, `internal/store/comparison.go`, `internal/httpapi/comparison.go`, `web/src/features/compare/*`; tests and comparison E2E.

**Interfaces:** POST `/api/comparisons` with `{proxy_ids, from, to}`; 2-20 unique proxies, range <=365 days; `Store.Compare(ctx context.Context, f domain.ComparisonFilter) (domain.Comparison, error)` uses one snapshot with bounded aggregation. Read-token access is not automatic for this POST; Task 22 explicitly decides/implements read scope if exposed.

- [ ] Test p50/p95 nearest rank on [10,20,30,40] => 20/40, no-sample nulls, unequal sample counts, method/provider/sample-size cohorts, low-confidence exclusions, retained-range gaps, timezone boundaries and comparison ordering stability. A fallback's failed-attempt time is included in Task 9 full-check total; legacy successful-attempt-only duration is a separate labeled cohort, never silently mixed.
- [ ] Run comparison/store/API/component tests; verify no existing aggregate silently satisfies the new cohort contract.
- [ ] Implement table-first comparison using timeline/traffic read models; include sample/coverage caveats, no overall score, separate measured versus uncertain durations and no raw-row export to browser for aggregation.
- [ ] Run bounded 100-proxy-history query fixture benchmarks, frontend and browser selection/error/empty/mobile scenarios.
- [ ] Review, UX3-check and commit.

### Task 22: Scoped read-only API tokens

**Files:** Create `internal/auth/apitoken.go`, `internal/store/apitokens.go`, `internal/httpapi/apitokens.go`, next `*_api_tokens.sql`, token UI; Modify auth routing without weakening existing Require; tests in auth/httpapi/store.

**Interfaces:** Session-admin-only GET/POST/DELETE `/api/tokens[/{id}]`; creation returns raw token once. Read middleware `RequireReadScope(scope string, next http.Handler) http.Handler` accepts valid session or allowed bearer token only on explicit JSON read routes; separate `RequireBearerScope(scope string, next http.Handler) http.Handler` forbids session-only metrics access. Define one enum/validator for exactly five spec scopes; `read:export` authorizes only POST exports, not comparisons or arbitrary POSTs.

- [ ] Test every route/verb in an authorization matrix: allowed reads, settings/auth/token management/backup/mutations denied, query tokens rejected, expiry/revoke/password-reset semantics, CSRF still required for session mutations, secret shown once and absent from logs/export/backup listing APIs.
- [ ] Run token/auth/API tests; establish unauthorized/absent bearer behavior failures.
- [ ] Implement digest storage, maximum 20 tokens, 1-365 day expiry, fixed scope allowlist, 60/minute burst-10 limiter and one export per token. Revocation immediate; password reset revokes all API tokens as well as sessions. Add last-used timestamp with bounded write frequency.
- [ ] Run limiter-bound/multi-token tests, secret canaries, auth regression suite and UI creation/revocation E2E. Document read data sensitivity and plain HTTP risks.
- [ ] Review and commit; no automatically generated broad token.

### Task 23: Signed webhooks with isolated delivery

**Files:** Create `internal/notify/webhook.go`, `internal/store/webhooks.go`, `internal/httpapi/webhooks.go`, next `*_webhook_delivery.sql`, webhook UI/tests; reuse Task 16 destination validation/pinning in a neutral internal outbound helper after focused extraction.

**Interfaces:** Session-admin GET/POST/PUT/DELETE `/api/webhooks[/{id}]`, POST `/api/webhooks/{id}/test`; `WebhookSender.Send(ctx context.Context, destination domain.WebhookDestination, event domain.WebhookEvent) error`; outbox identity `(event_id, destination_id, destination_version)`. Test delivery is visibly labeled.

- [ ] Test HMAC raw bytes/timestamp, stable event ID across retries, 2xx success, 408/429/5xx retry versus other 4xx final, DNS-inclusive 5s timeout/4KiB cap, special-address/rebinding/redirect/second-origin rejection, original certificate identity, encrypted signing keys, edit/delete queued destination and one failed webhook not blocking others/Telegram. Persistent delivery counters never decrease when delivery history is pruned.
- [ ] Run webhook/store/API tests and confirm new security/delivery contract fails without implementation.
- [ ] Implement max five opt-in HTTPS destinations, isolated bounded delivery workers and versioned allowlist payloads. Re-sign retries with new timestamp but same event/body; never store raw network error URLs/bodies.
- [ ] Run fake receiver, receiver dedupe/signature-age example, restart/retention/backup and secret-canary tests; browser test per-destination delivery status.
- [ ] Security review, UX3-check and commit.

### Task 24: Optional authenticated Prometheus metrics

**Files:** Create `internal/domain/metrics.go`, `internal/store/metrics.go`, `internal/metrics/handler.go`, `internal/metrics/handler_test.go`; Modify app/config/routes/settings UI and operations docs.

**Interfaces:** Disabled-by-default `/metrics`, bearer-only `read:metrics` via Task 22's `RequireBearerScope`; `metrics.NewHandler(source SnapshotSource) http.Handler`. Define `SnapshotSource` in `internal/metrics/handler.go` with `MetricsSnapshot(ctx context.Context, now time.Time) (domain.MetricsSnapshot, error)`; implement it in `internal/store/metrics.go`. Define `domain.MetricsSnapshot` in `internal/domain/metrics.go`: observation values/timestamps and queue/worker gauges, durable monotonically increasing Task 15 payload totals and Task 23 delivery counters, with bounded-label series from the spec. Last-known state and freshness are separate gauges; missing values are omitted. Text exposition uses `proxyward_*` names; session auth alone does not expose this endpoint.

- [ ] Test disabled=404, no/wrong scope=401/403, revoked/expired token, missing observations omitted, state/timestamps correct, labels lack addresses/names/URLs/secrets, scrape creates no network work and concurrent scrape cap=2.
- [ ] Run metrics/auth/API tests and see missing endpoint/auth/cardinality contract fail.
- [ ] Implement bounded cached/snapshot read with fast timeout and valid Prometheus HELP/TYPE/escaping. Reuse current observed counters/queue/ledger; do not claim exact wire bytes or exact downtime.
- [ ] Validate exposition parsing with a pinned test parser or external checker during implementation; run fixture scrape under load and full auth/security suite.
- [ ] Release D review, UX3-check and commit; include optional scrape configuration without requiring a monitoring stack.

## UX3: Continuous screen migration and rendered quality gates

**Files:** Feature screens/components changed by Tasks 9-24, `web/e2e/*`, approved screenshots and `docs/design/2026-11-03-redesign-brief.md`.

**Interfaces:** Consume approved UX1/UX2 patterns and actual implemented APIs; use shared components and semantic tokens. No fake summary values or dead actions to complete a mockup.

- [ ] For each feature slice, add failing component/browser checks for empty/loading/partial/error/offline/stale/permission states, keyboard flow and narrow-layout behavior before implementation.
- [ ] Migrate the slice to the approved design; preserve data semantics, routes, filters, drafts, selection and back-navigation context. Keep auto-refresh stable during interaction.
- [ ] Capture wide/narrow/light/dark evidence with realistic long names and extreme data. Inspect at 200% zoom and reduced motion; validate contrast and chart textual alternatives.
- [ ] Run approved automated accessibility checks and skill grid/craft audits where available; record deliberate exceptions. Manually exercise keyboard/screen-reader paths before accessibility claims.
- [ ] Present Before/After/Why, resolve major findings and obtain visual acceptance per release; do not postpone every UI decision to Task 25.

## Task 25: End-to-end migration, acceptance and release handoff

**Files:** Update fixtures/CI/e2e/README/operations/STATUS/DECISIONS/TODO and version; no new product functionality in this task.

**Interfaces:** All approved APIs/UI contracts; existing backup/restore CLI; four release gates and UX evidence. A backup made after schema expansion is not promised to restore into an older binary.

- [ ] Add upgrade fixtures starting at v006 and intermediate releases; include credentials, sessions, schedules, pending/running/cancel-requested jobs, incidents, outboxes and retention-edge history. Assert preserved IDs/state/secrets and successful old-backup restore into the new binary.
- [ ] Run complete offline checks below, then a bounded 100-proxy/20-target fixture scenario verifying queue caps, fairness, responsive UI, bounded query counts and retention. Add outage during write/restore checks; no actual Internet speed benchmark.
- [ ] Verify Docker nonroot/read-only operation, setup-token CLI, timezone behavior, graceful stop, migration integrity and disposable-volume restore. Rollback is restore of a pre-upgrade backup into the matching old version, not blind binary downgrade.
- [ ] Only with explicit live-test approval and supplied services/proxies, run bounded Debian HTTP/HTTPS/SOCKS5, family-only endpoints, one small speed sample, one Telegram and one webhook delivery. Record counts/bytes/time and failures; no automatic retries outside the budget.
- [ ] Final code/security/visual review, user acceptance and release notes; mark only evidenced checks complete. Document remaining unverified providers/rates and exact next step.

## Verification commands

Run the relevant subset after each task; run the complete set for each release:

```text
go build ./...
go test -count=1 ./...
go vet ./...
gofmt -l cmd internal
npm --prefix web test
npm --prefix web run typecheck
node scripts/build-web.mjs
```

From `web/`: `npx playwright test`. CI installs its pinned browser first and sets the supported channel override. Linux with cgo/toolchain: `go test -race -count=1 ./...`. Docker: `docker compose config --quiet` and `docker build -t proxyward:acceptance .`; these checks do not authorize starting/upgrading a real deployment. UI assertions are not replaced by screenshots, and passing screenshots do not replace manual rendered inspection.

## Coverage checklist

| Requested item | Owning tasks |
| --- | --- |
| Schedule interval/disable correctness | 1-2 |
| Cancellation race | 3, 8 |
| Events filter race | 4 |
| Settings error state; chart errors | 5 |
| Availability Retry-After | 6 |
| Safer setup and truthful binding docs | 7 |
| Atomic results/incidents; browser CI; stale docs | 8, 25 |
| Stage diagnostics | 9 |
| IPv4/IPv6 | 10 |
| Exit IP/country/ASN history | 11, 19 |
| Downtime timeline/freshness | 12 |
| Clear episodes/versioned export/consistency | 13 |
| Groups/saved views/bulk control | 14 |
| Speed payload budgets/forecast | 15 |
| Custom sites | 16-18 |
| Flexible alerts/quiet hours | 19-20 |
| Proxy comparison | 21 |
| API tokens/webhooks/Prometheus | 22-24 |
| Full redesign, not a color-only refresh | UX1-UX3 across all releases |
| Upgrade/restore/live acceptance | 25 |

## Handoff and approval

Review the spec's proposed consequential choices and approve the release contracts before Release A. Permission to create concept previews precedes their construction; concept selection approves UX2/redesign only and does not block approved correctness fixes. Recommended execution: subagent-driven, one implementer/reviewer per bounded task, because lifecycle, privacy and migration errors have lasting consequences; native execution is also supported if lower coordination cost is preferred. Do not dispatch implementation agents or deploy merely because a plan file exists.
