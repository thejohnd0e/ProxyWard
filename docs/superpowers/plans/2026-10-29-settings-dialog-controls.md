# Settings Dialog Controls Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Move theme selection and sign out from the top bar into the Settings dialog and replace its unusual icon with a recognizable gear.

**Architecture:** Keep the current top-bar/dialog structure. Put the existing persisted theme control in an Appearance card and the existing authentication sign-out action in an Account card within `SettingsPage`; simplify `Shell` to navigation and the settings trigger.

**Tech Stack:** React 19, TypeScript, React Router 7, Vite, Vitest, Testing Library.

**Spec:** `docs/superpowers/specs/2026-10-29-settings-dialog-controls-design.md`

## Global Constraints

- Preserve the existing Settings dialog and its open/close/accessibility behavior.
- Preserve theme choices and local-storage persistence through `useTheme`.
- Use the existing `useAuth().signOut` behavior and do not change auth APIs.
- Keep project UI content in English; use inline SVG, adding no icon dependency.

## Review Focus

- Settings affordance stays accessible by its `Settings` label and recognizable gear; test the button remains present and opens the dialog.
- Theme setting remains persisted when moved; test Dark and System behavior inside the dialog.
- Sign out closes the dialog through auth state change and shows login; test logout API invocation and resulting login form.
- The top bar no longer exposes theme or sign-out controls; assert their absence outside the dialog.

---

### Task 1: Relocate appearance/account controls and simplify the settings icon

**Files:**
- Modify: `web/src/app/App.tsx`
- Modify: `web/src/features/settings/SettingsPage.tsx`
- Modify: `web/src/styles/global.css` (gear icon style only if needed)
- Test: `web/src/app/App.test.tsx`

**Interfaces:**
- Consumes existing `useTheme()` and `useAuth().signOut` hooks.
- Produces Appearance and Account cards in the Settings dialog; top bar retains brand, primary navigation, and a `Settings` gear button.

- [x] **Step 1: Add App tests asserting Theme and Sign out are absent from the top bar but available inside the Settings dialog; verify Dark/System persistence and Sign out returns to login.**
- [x] **Step 2: Run `npm --prefix web test -- src/app/App.test.tsx` and confirm the new assertions fail against the current top bar.**
- [x] **Step 3: Move the existing theme selector into an Appearance card, move the existing sign-out action into an Account card, remove those controls and their state from `Shell`, and replace the custom gear path with a conventional outlined gear SVG.**
- [x] **Step 4: Re-run focused tests and verify they pass; retain existing modal focus/close behavior.**
- [x] **Step 5: Run `npm --prefix web test`, `npm --prefix web run typecheck`, `npm --prefix web run build`, `node scripts/build-web.mjs`, and `npx playwright test` from `web/`; all pass (139 Vitest tests; 20 Playwright tests).**
