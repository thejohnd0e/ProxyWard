# Shared coding-agent instructions

## Before substantial work

- Read `AGENTS.md`, `STATUS.md`, `DECISIONS.md`, and `TODO.md` before starting substantial work.
- Inspect the relevant source, configuration, and tests before making changes. Do not assume a language, framework, or command that is not present in the repository.

## Working agreements

- Communicate with the user in Russian during development. Keep all project content in English: UI, documentation, identifiers, comments, test descriptions, validation messages, and errors. Do not introduce localization or a language selector. This does not restrict user-supplied data.
- Keep changes focused and consistent with the repository as it develops.
- Prefer existing project commands and patterns; document unknown setup or behavior as TBD rather than guessing.
- Run appropriate checks for changes and report what was run and any failures.
- Update `STATUS.md`, `DECISIONS.md`, or `TODO.md` when substantial work changes project status, establishes a durable technical decision, or completes/adds tasks.
