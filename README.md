# ProxyWard

Self-hosted monitor for up to 100 HTTP, HTTPS and SOCKS5 proxies on your local network, with or without authentication.

## Overview

- Manual and interval checks per proxy, with the failure reason and the observed exit IP.
- Download and upload speed over a bounded sample; a missing result is shown as missing, never as zero.
- Exit-IP country, city and ASN through an optional ipapi.is key (cached, daily budget respected).
- History with latency and speed charts, incidents, and Telegram notifications.
- Import proxies from a list, with a preview and duplicate detection.
- English-only web interface in a restrained macOS-like style: light, dark or system theme, usable on a phone.
- Single administrator, passwords hashed with Argon2id, stored proxy and bot secrets encrypted at rest, secrets never returned by the API.

Stack: Go, React/TypeScript and SQLite in one Docker container on Debian with a persistent volume. The Go module and the binary are still named `proxy-checker`.

## Install (Docker)

Requirements: Docker Engine with the Compose plugin, and a LAN address on the host.

```text
git clone https://github.com/thejohnd0e/ProxyWard.git
cd ProxyWard
cp .env.example .env                                        # set LAN_IP to this host's LAN address
docker compose up -d --build
docker compose run --rm proxy-checker admin set-password    # at least 12 characters
```

Open `http://LAN_IP:8080/` and sign in. The port is published only on `LAN_IP`. The interface is plain HTTP, so keep it on a trusted network or behind a TLS reverse proxy (`SESSION_SECURE=true`). Backup, restore, upgrade, firewall notes and verification status are in [docs/operations.md](docs/operations.md).

Docker, backup and restore commands:

```text
docker compose exec -T proxy-checker proxy-checker backup --output /data/backup.tar.gz
docker compose run --rm -T proxy-checker restore --input - --force < backup.tar.gz   (server stopped)
```

`docker compose down -v` deletes all data. The data volume holds `state.db` and `secrets.key`; back them up together.

## Status

Backend, web interface, container, backups and retention are implemented and tested locally and on a Debian test VM. Not yet verified with real services: monitoring of real proxies from Debian, a live ipapi.is key, a live Telegram bot and IPv6 egress. See [STATUS.md](STATUS.md).

## Develop from source

## Setup

Verified toolchain (Windows development machine, 2026-10-20):

- Go 1.27.0 (`go.mod` declares `go 1.27.0`), `CGO_ENABLED=0`.
- SQLite driver: `modernc.org/sqlite` v1.60.1 (pure Go, no C compiler needed). Versions are pinned by `go.mod` and `go.sum`.
- Node 22.14.0 and npm 10.9.2 build and test the web interface (React 19, Vite 7, Vitest 3, Playwright 1.56, all pinned). Docker is verified on a Debian 13 VM.

## Usage

Run the server (data is stored in `./data` by default):

```text
go run ./cmd/proxy-checker
```

Environment variables:

| Variable | Default | Meaning |
| --- | --- | --- |
| `PROXY_CHECKER_LISTEN` | `127.0.0.1:8080` | Listen address (`host:port`, port 1-65535) |
| `PROXY_CHECKER_DATA_DIR` | `data` | Directory for `state.db` |
| `PROXY_CHECKER_SESSION_SECURE` | `false` | Mark session cookies Secure (for HTTPS) |

`GET /healthz` returns 200 when the database is reachable. The web interface is served at `/` (sign in with the administrator password), the JSON API under `/api/`.

Set or reset the administrator password before first login (prompts on a terminal, or reads the first line of piped stdin; never pass it as an argument):

```text
go run ./cmd/proxy-checker admin set-password
```

The password needs at least 12 characters, and resetting it revokes all sessions. The data directory also holds `secrets.key`, which encrypts stored secrets: back it up together with `state.db`. If the key is lost while encrypted records exist, the server refuses to start rather than creating a new key. Session cookies are not marked Secure unless `PROXY_CHECKER_SESSION_SECURE=true`, so plain-HTTP LAN use works but sends the cookie unencrypted.

## Build and tests

```text
go build ./...
go test ./...
go vet ./...
```

Web interface (Node 22, npm 10; dependencies are pinned by `web/package-lock.json`):

```text
node scripts/build-web.mjs            build the interface into internal/webui/dist (embedded by Go)
npm --prefix web test                 unit and component tests (Vitest)
npm --prefix web run typecheck        strict TypeScript
cd web && npx playwright test         browser tests against the local stand (uses the installed Chrome)
node scripts/e2e-server.mjs           start the local stand by hand: app on 127.0.0.1:18080
```

The stand needs Go on the PATH. Its admin password is printed by `http://127.0.0.1:18081/__e2e/fixtures` (test-only values; the stand uses a temporary database and fake services, never the internet). After changing the web sources run `node scripts/build-web.mjs` before `go build`, otherwise the binary serves the previous build (or a "not built" notice).

## Development notes

See [AGENTS.md](AGENTS.md) for shared coding-agent instructions. Current project status, decisions, and pending work are tracked in [STATUS.md](STATUS.md), [DECISIONS.md](DECISIONS.md), and [TODO.md](TODO.md).

- [Design specification](docs/superpowers/specs/2026-10-19-proxy-checker-design.md)
- [Implementation plan](docs/superpowers/plans/2026-10-19-proxy-checker.md)
- [Public-service verification](docs/research/2026-10-19-public-services.md)

All project content is English-only. Development conversations with agents are in Russian.

## License

[MIT](LICENSE). Third-party notices: [docs/THIRD_PARTY.md](docs/THIRD_PARTY.md).
