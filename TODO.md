# TODO

- [x] Define requirements, supported proxy types and architecture.
- [x] Probe public IP/geo/Download/Upload services and record evidence and limits.
- [x] Write the specification and implementation plan; apply the English-only project requirement.
- [x] Prepare the session handoff with explicit completion boundaries and next-agent reading order in STATUS.md.
- [x] Obtain implementation go-ahead (given 2026-10-20). Proposed technical defaults remain open for user review.
- [x] Execute task 1 (Persistent foundation): tools verified, Go 1.27.0 and modernc.org/sqlite pinned, tested config/SQLite/application core.
- [x] Execute task 2 (Admin sessions and encrypted secrets), except the race-detector run.
- [x] Run `go test -race` for auth/secrets/httpapi (and all packages) on the Linux test VM: passed 2026-10-20.
- [x] Execute task 3 (Proxy management and list import); race-tested on the Linux VM.
- [x] Execute task 4 (Explicit proxy transports and availability); race-tested on the Linux VM.
- [x] Execute task 5 (Keyed geolocation and cooldowns) and task 6 (Bounded bidirectional throughput); race-tested on the Linux VM.
- [x] Execute task 7 (Persistent jobs, schedules and incidents); race-tested on the Linux VM.
- [x] Execute task 8 (Telegram delivery and events); race-tested on the Linux VM.
- [x] Execute task 9 (English-only web interface and browser QA): 121 Vitest tests, typecheck, build, 18 Playwright tests (desktop and 375 px) against the local stand.
- [x] Execute task 10 (container, retention, backup/restore, operations docs); Docker acceptance on the Linux VM.
- [ ] Run the implementation review gate before handoff.
- [ ] Run Debian/proxy, keyed ipapi.is and real Telegram integration checks with supplied credentials.
- [x] Record verified Docker, frontend, backup/restore commands (README.md and docs/operations.md).
