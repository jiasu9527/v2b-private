# PostgreSQL Single-Command Workflow

This repository now uses a unified Go runtime entrypoint:

```bash
bash <(curl -fsSL <public-install-url>/install.sh)
wget -qO- <public-install-url>/install.sh | bash
./menu.sh
./scripts/appctl install-link
forest
forest install
forest install-legacy /path/to/legacy-v2board
forest update
forest start
forest status
./init.sh
./init.sh /path/to/legacy-v2board
./update.sh
./scripts/appctl install-legacy /path/to/legacy-v2board
./scripts/appctl prompt-db
./scripts/appctl migrate-mysql
./scripts/appctl build
./scripts/appctl migrate-config
./scripts/appctl run
./scripts/appctl start
./scripts/appctl stop
./scripts/appctl restart
./scripts/appctl status
./scripts/appctl test
./scripts/appctl create-admin
./scripts/appctl seed-demo
BASE_URL=http://127.0.0.1:8080 ./scripts/verify-demo-api.sh
BASE_URL=http://127.0.0.1:8080 ./scripts/verify-demo-payment-api.sh
BASE_URL=http://127.0.0.1:8080 ./scripts/verify-demo-payment-notify.sh
BASE_URL=http://127.0.0.1:8080 PAYMENT_GATEWAY=CoinPayments PENDING_TRADE_NO=seed-demo-order-cpay-pending-01 CALLBACK_NO=seed-demo-callback-cpay-01 ./scripts/verify-demo-payment-notify.sh
BASE_URL=http://127.0.0.1:8080 PAYMENT_GATEWAY=StripeCheckout PENDING_TRADE_NO=seed-demo-order-stchk-pending-01 CALLBACK_NO=seed-demo-callback-stchk-01 ./scripts/verify-demo-payment-notify.sh
SUMMARY_JSON=/tmp/soak.json BASE_URL=http://127.0.0.1:8080 DURATION_SEC=15 CONCURRENCY=8 MAX_P95_MS=50 MAX_RSS_DELTA_KB=2048 ./scripts/soak-demo-api.sh
./scripts/appctl env-file
./scripts/appctl doctor
./scripts/appctl service-template
BASE_URL=http://127.0.0.1:8080 SERVER_TOKEN='your-server-token' NODE_ID=1 NODE_TYPE=vmess ./scripts/smoke-node-api.sh
```

## Install with PostgreSQL

Dedicated install walkthrough: `docs/install.md`

Fast path:

```bash
bash <(curl -fsSL <public-install-url>/install.sh)
```

Or:

```bash
wget -qO- <public-install-url>/install.sh | bash
```

Local repo path:

```bash
./init.sh
```

If the old PHP project is in another directory:

```bash
./init.sh /path/to/legacy-v2board
./scripts/appctl install-legacy /path/to/legacy-v2board
```

Legacy path mode only requires:

- `legacy/.env` as the source MySQL connection file
- optional `legacy/config/v2board.php`
- optional `legacy/config/theme/*.php`

It does not need the legacy PHP runtime, Redis, Webman, PM2, `vendor`, or `node_modules`.

If you prefer an interactive numbered menu for manual ops:

```bash
./menu.sh
```

If `forest` is installed globally, you can also run commands from any directory without entering the project root:

```bash
forest install
forest install-legacy /path/to/legacy-v2board
forest update
forest start
forest status
```

If you need to edit the database or admin config before importing data:

```bash
./scripts/appctl init-env
vi .env.go
./init.sh
```

Minimum values:

```bash
POSTGRES_DSN='postgres://postgres:password@127.0.0.1:5432/forest?sslmode=disable'
ADMIN_EMAIL=admin@example.com
ADMIN_PASSWORD=your_admin_password
```

Optional:

```bash
DB_HOST=127.0.0.1
DB_PORT=5432
DB_DATABASE=forest
DB_USERNAME=postgres
DB_PASSWORD=your_password
DB_SSLMODE=disable
```

Then run:

```bash
./init.sh
```

## Update

Dedicated update walkthrough: `docs/update.md`

```bash
./update.sh
```

If you want to rewrite PostgreSQL connection info before the update:

```bash
./scripts/appctl prompt-db
./update.sh
```

If `./update.sh` is started from an interactive shell, it now asks whether you want to update PostgreSQL config first.
To force that prompt:

```bash
FORCE_INTERACTIVE_DB_CONFIG=1 ./update.sh
```

If this repository is still on the old PHP + MySQL stack and the legacy `.env` is still present, `./update.sh` now does one automatic migration pass:

- reads MySQL source config from the legacy `.env`
- prompts for PostgreSQL target config when it is still missing
- bootstraps PostgreSQL schema from `database/install.pgsql.sql`
- copies legacy MySQL table data into PostgreSQL
- skips the normal PostgreSQL `update.pgsql.sql` step on that first migration pass

Manual entrypoint for the same flow:

```bash
./scripts/appctl migrate-mysql
```

Minimum requirement for `./update.sh`:

```bash
POSTGRES_DSN='postgres://postgres:password@127.0.0.1:5432/forest?sslmode=disable'
```

Or:

```bash
DB_HOST=127.0.0.1
DB_PORT=5432
DB_DATABASE=forest
DB_USERNAME=postgres
DB_PASSWORD=your_password
```

Notes:

- `update` itself only needs PostgreSQL connection info for schema migration.
- `ADMIN_EMAIL` is not required for `update`.
- `APP_KEY` is not used by the SQL update step, but it must already exist before `./update.sh` restarts the Go service, otherwise login/session code will fail at runtime.
- On first creation of `.env.go` from a legacy MySQL `.env`, shared values such as `APP_KEY`, `APP_URL`, `ADMIN_EMAIL`, and mail settings are copied into `.env.go`.
- In BaoTa, you do not need PHP-FPM, Redis, Webman, or PM2 for this Go runtime. Keep PostgreSQL, run the Go service with `systemd`, and reverse-proxy your site domain to `127.0.0.1:8080`.
- BaoTa single-machine steps are documented in `docs/baota-go-single-machine.md`.

## Notes

- `install` and `update` execute PostgreSQL SQL files directly:
  - `database/install.pgsql.sql`
  - `database/update.pgsql.sql`
- Go runtime prefers `.env.go`; a legacy `.env` is ignored when it still looks like MySQL/Redis config.
- `./scripts/appctl doctor` prints the active env file and whether PostgreSQL/admin email are configured.
- `./scripts/smoke-node-api.sh` can do a read-only node API smoke test before cutover.
- `./scripts/verify-demo-api.sh` can verify the seeded admin/user/staff demo data against a running Go API.
- `./scripts/verify-demo-payment-api.sh` can verify seeded payment methods and real checkout response against a running Go API.
- `./scripts/verify-demo-payment-notify.sh` can verify seeded `EPay`, `CoinPayments`, and `StripeCheckout` notify callback handling against a running Go API.
- `./scripts/soak-demo-api.sh` can do a short read-only concurrent smoke load, write a JSON summary, and fail on p95/RSS delta thresholds for local regression checks.
- If `POSTGRES_DSN` is not set, `appctl` builds it from `DB_HOST/DB_PORT/DB_DATABASE/DB_USERNAME/DB_PASSWORD`.
- `install` auto-generates `APP_KEY` when it is empty.
- `install` also creates or updates the admin account using `ADMIN_EMAIL` and `ADMIN_PASSWORD`.
- Upgrades can import old config files into `config/admin.json` and `config/theme/*.json` with `./scripts/appctl migrate-config`.
- For a long-running single-machine deployment, generate a `systemd` unit with `./scripts/appctl service-template`.
- Go-live steps are documented in `docs/go-live-checklist.md`.
- BaoTa deployment steps are documented in `docs/baota-go-single-machine.md`.
- Install steps are documented in `docs/install.md`.
- Update steps are documented in `docs/update.md`.
