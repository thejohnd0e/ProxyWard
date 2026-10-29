# Import fragment as proxy name

- **Intent:** Allow pasted proxy URLs such as `http://host:port#proxy_US` to import successfully, using the fragment as the proxy's display name.
- **Goals:** Parse and percent-decode the fragment, validate it with the existing proxy-name rules, preserve current address-derived naming when no fragment is present, and show actionable English validation errors.
- **Non-goals:** Change the accepted proxy URL formats otherwise, alter credentials handling, or add import options/UI controls.
- **Approaches:** (1) Treat the fragment literally; minimal but does not support encoded spaces/special characters. (2) URL-decode then validate as a name; recommended because it composes with existing name validation and standard URL encoding.
- **Chosen behavior:** Use the fragment after `#` as the name, percent-decode it, and apply `ValidateName`; continue rejecting invalid/malformed or empty names. Without a fragment, keep the current `host:port` default. Both preview and import use the same parser.
- **Constraints:** Keep project content English; retain credential redaction and stable, input-free parse errors.
- **Implementation:** Add parser and UI tests for named URLs, percent-encoding, absent fragments, and invalid names; update the fragment error description to explain how to fix an invalid name.
