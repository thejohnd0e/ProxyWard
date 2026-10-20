# Operations

How to run, back up, restore and upgrade Proxy Checker in Docker on a Debian host. Every command below was run on a Debian 13 test VM with Docker Compose unless marked "not verified".

## Requirements

- Docker Engine with the Compose plugin.
- A LAN address on the host for the web interface (for example `192.168.0.10`).
- Outbound internet access from the container: the proxies you monitor, `api.ipify.org`, `checkip.amazonaws.com`, `speed.cloudflare.com`, and optionally `api.ipapi.is` and `api.telegram.org`.

## First start

```sh
cp .env.example .env
# edit .env: set LAN_IP to this host's LAN address
docker compose up -d --build
```

`LAN_IP` is required. Compose publishes the port only on that address (`LAN_IP:8080`), never on `0.0.0.0`. Without it `docker compose` stops with an explicit error.

Set the administrator password (at least 12 characters; it is never a command argument):

```sh
docker compose run --rm proxy-checker admin set-password        # interactive prompt (TTY)
printf '%s\n' "$PASSWORD" | docker compose run --rm -T proxy-checker admin set-password   # from stdin
```

Then open `http://LAN_IP:8080/`. Check health with `docker compose ps` (status `healthy`) or `curl http://LAN_IP:8080/healthz`.

## Network exposure

- The interface is plain HTTP on the LAN. Session cookies are `HttpOnly` and `SameSite=Strict` but not `Secure` by default, so anyone who can observe LAN traffic can read the session. Use it only on a network you trust, or put a TLS reverse proxy in front and set `SESSION_SECURE=true` in `.env`.
- Sign-ins are limited to 5 per minute per client and 20 per minute globally; `X-Forwarded-For` is ignored.
- Restrict access with your host firewall (for example allow `8080/tcp` only from your management addresses). The application does not change firewall or router settings.
- Do not publish the port to the internet.

## Data and the secrets key

The named volume `proxy-checker-data` is mounted at `/data` and holds:

- `state.db` (SQLite, WAL mode): proxies, history, incidents, settings;
- `secrets.key` (32 bytes, mode 0600): encrypts proxy passwords, the Telegram bot token and the geolocation key stored in the database.

The database is useless for stored secrets without the key, and the key is useless without the database. Always keep them together. If `secrets.key` is lost while the database holds encrypted records, the server refuses to start rather than create a new key.

`docker compose down` keeps the volume. `docker compose down -v` **deletes all data**.

## Backup

Backups are consistent while the server runs (`VACUUM INTO` snapshot plus the key, in one `tar.gz` with exactly `state.db` and `secrets.key`). The archive contains the key: store it as carefully as a password.

```sh
docker compose exec -T proxy-checker proxy-checker backup --output /data/backup-$(date +%F).tar.gz
docker cp "$(docker compose ps -q proxy-checker)":/data/backup-$(date +%F).tar.gz .
docker compose exec -T proxy-checker rm /data/backup-$(date +%F).tar.gz
```

The command refuses to overwrite an existing file. Backups are never exposed through the web interface.

## Restore

Restoring needs the server stopped and replaces the database and key.

```sh
docker compose stop
docker compose run --rm -T proxy-checker restore --input - --force < backup-2026-10-20.tar.gz
docker compose up -d
```

- `--input -` reads the archive from standard input. This avoids permission problems: the container runs as uid 10001, so a root-only (0600) archive mounted with `-v` is unreadable inside it (observed: `permission denied`).
- `--force` is required when a database already exists. Without it the command refuses.
- The archive is validated before anything changes: exactly the two expected regular files, a 32-byte key and a database that passes `PRAGMA integrity_check`. Path tricks, extra entries, symlinks and duplicates are rejected.
- To test a backup without touching production, restore into a disposable project: `PORT=8081 docker compose -p pcrestore run --rm -T proxy-checker restore --input - < backup.tar.gz`, then `docker compose -p pcrestore up -d`, check it, and `docker compose -p pcrestore down -v`.

## Upgrade

```sh
docker compose build --pull
docker compose up -d
```

Database migrations run at startup in a transaction. Take a backup first. The base images are pinned by tag in the `Dockerfile` (`node:22.14.0-bookworm-slim`, `golang:1.27.0-bookworm`, `debian:bookworm-slim`); for reproducible production builds pin them by digest too (override with `--build-arg`).

## Logs, retention and resources

- Logs go to stdout as structured text and are capped by Docker (10 MB x 3 files): `docker compose logs -f proxy-checker`. Credentials and credential-bearing URLs are never logged.
- History older than the retention setting (default 30 days, Settings page) is deleted hourly in batches of 1000. The latest result of each proxy, open incidents and undelivered notifications are never pruned.
- Observed on the test VM: about 70 MiB RAM idle, image about 148 MB, `docker compose stop` finishes in about 1 second with exit code 0.
- The container runs as uid 10001 with a read-only root file system, `/tmp` on tmpfs, all capabilities dropped and `no-new-privileges`.

## Reset the administrator password

Run the same `admin set-password` command as for the first start. Stop and start the server afterwards if sessions from the old password should end (not verified how the CLI interacts with a running server).

## Verification status

Verified on the Debian 13 VM: image build, compose config and the required-`LAN_IP` error, LAN-only socket binding, healthcheck, security headers, password setup, proxy creation with a stored password (never returned by the API), persistence across `--force-recreate`, data file modes, backup while running, restore into a disposable volume (the restored instance accepted the password and kept the proxy and its stored password), graceful stop.

Not verified (needs supplied credentials or hardware): monitoring of real HTTP/HTTPS/SOCKS5 proxies from Debian, a real ipapi.is key, a real Telegram bot, IPv6 egress, behaviour behind a TLS reverse proxy.
