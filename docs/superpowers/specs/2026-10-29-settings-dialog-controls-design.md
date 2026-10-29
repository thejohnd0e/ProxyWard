# Settings dialog controls design

## Intent

Keep the ProxyWard top bar focused on navigation. Move theme selection and sign out into the existing Settings dialog, and make its trigger icon immediately recognizable as a gear.

## Goals and boundaries

- Remove the theme selector and Sign out button from the top bar.
- Add an Appearance card to the existing settings content, using the existing persisted theme preference and options (System, Light, Dark).
- Add a Sign out action in an Account card within the same dialog; use the existing authentication sign-out behavior.
- Replace the custom settings SVG with a conventional, simple outlined gear icon. Keep its accessible name and button behavior.
- Preserve the current settings dialog behavior, navigation, APIs, and password-management flow.
- Do not add icon dependencies or change the page/route architecture.

## Approaches

1. **Use the current Settings dialog (selected):** move theme and sign-out controls into Appearance and Account cards in the existing `SettingsPage`; replace the icon with a clear inline SVG gear. This preserves the compact header and current dialog workflow.
2. **Restore a standalone Settings route:** put the controls on a dedicated page. This changes navigation and conflicts with the chosen dialog-centered interface without adding necessary capability.

## Design decisions

- The user selected approach 1.
- Theme changes continue through the existing `useTheme` hook, preserving local-storage behavior.
- Sign out continues through `useAuth().signOut`; the dialog closes naturally when authentication state changes.
- Keep UI copy in English and use an inline SVG to avoid a new package dependency.

## Validation

- App tests verify no theme/sign-out controls remain in the top bar, both controls are available in the Settings dialog, selecting a theme persists it, and sign-out returns to the login screen.
- Existing dialog open/close and full frontend/browser checks continue to pass.
