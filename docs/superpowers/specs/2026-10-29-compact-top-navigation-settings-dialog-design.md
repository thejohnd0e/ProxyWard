# Compact top navigation and settings dialog

## Intent

Remove the persistent desktop sidebar so the proxy table can use the freed horizontal space. Keep primary navigation and account controls reachable in a compact top bar, and let the settings icon open settings in a dialog.

## Goals and boundaries

- Replace the desktop sidebar with a responsive top bar containing the ProxyWard brand, Proxies and Events navigation, a settings icon, theme selector, and sign-out control.
- Reuse the existing settings UI and API behavior inside an accessible, scrollable dialog; do not create a second settings implementation.
- Display the application version beside the ProxyWard brand in the top bar. The version is a release identifier, not a per-build counter; increment it deliberately when application changes are made.
- Retain the current English-only UI and responsive behavior.
- Do not change backend APIs, settings behavior, or proxy/event page content.

## Approaches

1. **Top bar plus settings dialog (selected):** all global navigation is always visible; settings open in a dialog. This gains the sidebar's full width while keeping controls discoverable.
2. **Top bar plus profile menu:** puts theme and sign-out in a secondary menu, making the bar narrower but adding another interaction for common controls.
3. **Keep sidebar and add settings icon:** simplest, but does not address the lost horizontal space.

## Interaction and implementation

- Use one shared top-bar layout at desktop and mobile widths; active route styling remains on Proxies/Events links.
- The gear button has the accessible name “Settings” and opens a dialog that contains the existing settings sections, including account password controls. It does not navigate away from the current page.
- The dialog has a visible close control, closes on Escape and backdrop click, and keeps its contents scrollable on small screens. Follow existing dialog styles and accessible dialog conventions.
- Keep theme selection and sign-out directly available in the top bar. Adapt spacing/wrapping for narrow screens.
- Use the existing `web/package.json` version (`0.1.0`) as the current app version source, bump the patch component to `0.1.1` for this change, and expose that same value in the UI without maintaining a second hard-coded copy.
- Remove the standalone `/settings` page route or redirect it to the existing page context only if required by existing links; there are no planned navigation links to it.

## Verification

- Update app tests for top navigation, settings dialog open/close, settings controls, and existing route behavior.
- Run frontend tests, typecheck, build, and relevant Playwright coverage at desktop and mobile widths.

## Calls left to the implementer

- Choose the modal width and responsive max-height to fit the existing cards without making the dialog unusably cramped.
- Preserve focus access and scroll behavior using the repository's existing dialog patterns.
