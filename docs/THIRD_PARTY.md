# Third-party components

The web interface bundles these packages. Versions are pinned in `web/package-lock.json` and `go.sum`.

## Country flags

- [flag-icons](https://github.com/lipis/flag-icons) 7.6.0, MIT License, copyright (c) 2013 Panayiotis Lipiridis. The 4x3 SVG flags are bundled with the application and loaded from the same origin; nothing is fetched from a CDN.

## Frontend

- React, React DOM (MIT), React Router (MIT), Vite (MIT), TypeScript (Apache-2.0), Vitest, Testing Library, jsdom and Playwright (MIT / Apache-2.0) are used to build and test the interface. Only React, React DOM, React Router and flag-icons end up in the shipped bundle.

## Backend

- `modernc.org/sqlite` (BSD-3-Clause), `golang.org/x/crypto` and `golang.org/x/term` (BSD-3-Clause), and their transitive modules listed in `go.sum`.

## Fonts

- None are bundled: the interface uses the system font stack.
