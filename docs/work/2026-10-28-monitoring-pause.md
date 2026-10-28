# Per-proxy monitoring pause

- [x] Inspect storage, scheduler, API, and row actions.
- [x] Add failing coverage for persisted pause, scheduling, manual checks, and UI.
- [x] Implement a persistent pause independent of schedule overrides; remove pending scheduled checks, allow running checks to finish and manual checks to run.
- [x] Add Pause/Resume to More and gray paused rows without disabling their controls.
- [x] Run backend and frontend checks and update project status.

Verification: full Go suite and vet, 133 Vitest tests, TypeScript check, production web build, and all 20 Playwright tests passed on Windows. Browser coverage includes pause/resume across reload, enabled manual controls, computed grayscale style, and reviewed light/dark screenshots. Code review found no blocking issues; its test-coverage suggestions were addressed. Linux race checks were not run.

## Deployment — 2026-10-28

- Updated the existing Docker deployment, preserving its environment, Compose settings, published port, and persistent volume. Built on the server and recreated only the ProxyWard service.
- Preserved the previous image and root-only backups of the data/key, source, and configuration for rollback.
- Verified container and HTTP health, CLI health check, the new frontend asset, and the migrated `monitoring_paused` column. Live record counts matched the backup; no records were lost. Backup database integrity check returned `ok`.
- No production host was paused for testing. Linux race tests were not run.
