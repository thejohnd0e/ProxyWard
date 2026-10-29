# Import Fragment Proxy Name Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Import proxy URLs with `#name` and use the fragment as the proxy display name.

**Architecture:** Extend the existing Go `ParseLine` parser to separate and decode a URL fragment, validate it with `ValidateName`, and set `domain.Proxy.Name`. The UI continues to use server previews and maps stable parse error codes to English explanations.

**Tech Stack:** Go, React, TypeScript, Vitest.

**Spec:** `docs/superpowers/specs/2026-10-29-import-fragment-proxy-name-design.md`

## Global Constraints

- Project content and validation messages remain English.
- Parse errors must not contain input text or leak credentials.
- No new dependencies; preview and import share `ParseLine` behavior.
- No fragment preserves the existing `host:port` name.

## Review Focus

- Invalid percent-encoding in a fragment must return a safe parser error; test malformed `%` escapes.
- Empty or overlong/control-character names must be rejected by existing name validation; test empty and invalid names.
- Credential percent-decoding must remain unchanged when a valid name fragment is present; test credentials alongside `#name`.
- Fragment absence must preserve the existing default name; retain/add a regression assertion.
- Import preview must explain the fragment-specific validation error without exposing input; test UI error mapping.

---

### Task 1: Parse named proxy URLs

**Files:**
- Modify: `internal/proxy/parse.go`
- Test: `internal/proxy/parse_test.go`

**Interfaces:**
- Consumes: existing `ParseLine(line string, defaultProtocol domain.Protocol) (domain.Proxy, error)` and `ValidateName(name string) (string, error)`.
- Produces: `ParseLine` assigns a valid decoded fragment to `domain.Proxy.Name`; invalid fragment values return a stable `ParseError` code.

- [x] **Step 1: Add failing parser tests** for a normal `#proxy_US` name, a percent-encoded name, credentials plus a name, no fragment retaining the address-derived name, and malformed/empty/invalid names being rejected without echoing input.
- [x] **Step 2: Run `go test ./internal/proxy -run 'TestParseLine.*Fragment|TestParseLine.*Name' -count=1`**; confirm the new behavior fails before implementation.
- [x] **Step 3: Implement fragment extraction, percent-decoding, and `ValidateName` integration in `ParseLine`**, mapping failures to a stable parse error code.
- [x] **Step 4: Run `go test ./internal/proxy -count=1`**; confirm all parser tests pass.

### Task 2: Explain fragment-name validation in the UI

**Files:**
- Modify: `web/src/lib/format.ts`
- Test: `web/src/lib/format.test.ts`

**Interfaces:**
- Consumes: the stable parser error code added in Task 1 and existing `describeImportError(code: string): string`.
- Produces: plain-English explanation directing the user to provide a valid name after `#`.

- [x] **Step 1: Add a failing test** asserting the fragment-name error message explains the valid `#name` format and does not suggest removing a valid name fragment.
- [x] **Step 2: Run `npm --prefix web test -- src/lib/format.test.ts`**; confirm the test fails.
- [x] **Step 3: Update the relevant `IMPORT_ERRORS` entry** to describe invalid fragment names.
- [x] **Step 4: Run `npm --prefix web test -- src/lib/format.test.ts`**; confirm the test passes.

### Task 3: Verify end-to-end checks

**Files:**
- No source changes expected.

- [x] **Step 1: Run `go test ./...`**; confirm all Go packages pass.
- [x] **Step 2: Run `npm --prefix web test` and `npm --prefix web run typecheck`**; confirm tests and typecheck pass.
