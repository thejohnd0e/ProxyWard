# Compact Top Navigation and Settings Dialog Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the sidebar with a compact top bar, show the app version there, and open the existing settings UI in an accessible dialog.

**Architecture:** Keep the shell in `App.tsx`; reuse the existing `Dialog` component and `SettingsPage` content, widening and constraining the dialog for the settings form. Read the app version from `web/package.json` through Vite's define mechanism; bump the patch version once for this product change.

**Tech Stack:** React 19, TypeScript, React Router 7, Vite 7, Vitest, Testing Library, Playwright.

**Spec:** `docs/superpowers/specs/2026-10-29-compact-top-navigation-settings-dialog-design.md`

## Global Constraints

- Keep all project UI content in English; development communication may be Russian.
- Reuse existing settings UI and API behavior; do not change backend APIs or proxy/event page content.
- Keep primary navigation and the Settings trigger in the responsive top bar; place theme selection and sign out in Appearance and Account cards within the Settings dialog.
- Display app version `0.1.1` from the single `web/package.json` source; do not increment it for tests or repeated builds.
- Settings dialog closes with its close button, Escape, or backdrop click and retains existing accessible focus management.

## Review Focus

- Narrow viewport: top-bar controls must remain reachable and must not create horizontal overflow; cover in responsive Playwright checks.
- Settings form height: long cards must remain scrollable within the dialog; cover in dialog CSS and a mobile Playwright check.
- Dialog lifecycle: open/close must return focus to the gear and reopen without stale settings state; cover in App tests.
- Version drift: package and lockfile versions must agree, and the UI must render the package version; cover with app version assertion and frontend build.
- Legacy `/settings` deep link: it must not render a broken standalone page after settings move into the dialog; cover with route test.

---

### Task 1: Single application version source and visible version

**Files:**
- Modify: `web/package.json` and `web/package-lock.json` (set application/package version to `0.1.1`)
- Modify: `web/vite.config.ts` (define `__APP_VERSION__` from package metadata)
- Create: `web/src/app/version.d.ts` (declare `__APP_VERSION__`)
- Test: `web/src/app/App.test.tsx`
- Modify: `web/src/app/App.tsx` (render version beside brand as part of top-bar work in Task 2)

**Interfaces:**
- Produces: global compile-time constant `__APP_VERSION__: string`, derived from `web/package.json`.

- [ ] **Step 1: Add an App test asserting the app renders `v0.1.1` beside the ProxyWard brand.**
- [ ] **Step 2: Run the focused App test and verify it fails because the version is not yet rendered.**
- [ ] **Step 3: Set package and lockfile version to `0.1.1`; configure Vite to define `__APP_VERSION__` from package metadata and declare its TypeScript type.**
- [ ] **Step 4: Run the focused App test and verify it passes after App renders the version in the top bar.**
- [ ] **Step 5: Run `npm --prefix web run typecheck` and `npm --prefix web run build`; both must pass.**

### Task 2: Responsive top navigation and settings dialog

**Files:**
- Modify: `web/src/app/App.tsx`
- Modify: `web/src/styles/global.css`
- Modify: `web/src/features/settings/SettingsPage.tsx` (make its content suitable for embedding in a dialog without duplicate page framing)
- Test: `web/src/app/App.test.tsx`
- Modify: `web/e2e/mobile.spec.ts` and/or `web/e2e/flow.spec.ts` for responsive shell coverage

**Interfaces:**
- Consumes: `Dialog` from `web/src/components/Dialog.tsx`; `SettingsPage` existing settings form; `__APP_VERSION__` from Task 1.
- Produces: shell with `Main` navigation (Proxies, Events) and a `Settings` gear button opening a settings dialog with Appearance and Account controls.

- [ ] **Step 1: Add App tests for dialog open, close button, Escape, backdrop click, focus return, existing settings content, and direct `/settings` route behavior.**
- [ ] **Step 2: Run focused App tests and confirm the new dialog/navigation assertions fail.**
- [ ] **Step 3: Replace sidebar grid with a responsive top-bar shell; show active Proxies/Events links, `ProxyWard · v0.1.1`, and a Settings gear button. Render the existing SettingsPage inside the shared `Dialog` with a settings-specific wide/scrollable style. Preserve `/settings` compatibility by redirecting it to `/proxies`.**
- [ ] **Step 4: Add responsive styles: full-width content below the top bar; settings dialog width up to 1080px and max-height 88vh with internal vertical scrolling; wrap or compact controls on narrow screens without horizontal overflow.**
- [ ] **Step 5: Run `npm --prefix web test` and `npm --prefix web run typecheck`; all tests and type checking must pass.**
- [ ] **Step 6: Run `node scripts/build-web.mjs` and `npx playwright test` from `web/`; verify desktop dialog open/close and mobile shell/settings scrolling.**

### Task 3: Record the durable UI/version decision and verify final build

**Files:**
- Modify: `DECISIONS.md`
- Modify: `STATUS.md`

- [ ] **Step 1: Record top navigation/dialog behavior and the policy that app version increments with product changes, not each build.**
- [ ] **Step 2: Run `npm --prefix web test`, `npm --prefix web run typecheck`, `npm --prefix web run build`, and `npx playwright test` in `web/`; report any environment limitation rather than claiming an unrun check.**
- [ ] **Step 3: Update STATUS with the result and the checks actually run.**
