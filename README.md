# overlite

**Speak PostgreSQL, store SQLite.** overlite is a lightweight server that talks
the PostgreSQL wire protocol on the front and keeps all your data in a single
SQLite file on the back.

## Why

SQLite is a fantastic storage engine, but it isn't a server: no network access,
brittle locking over network filesystems, and you have to use SQLite-specific
tooling. overlite puts a PostgreSQL-speaking server in front of it so you get:

- **Real network access & concurrency** — one process owns the file and
  serializes access, so the multi-access and network-lock problems disappear.
- **The entire Postgres ecosystem for free** — connect with `psql`, DBeaver,
  and any PostgreSQL driver (pgx, JDBC, …). `\dt`, `\d`, `\copy` all work.
- **Dead simple to run** — a single static binary (pure-Go SQLite, no CGO), no
  server to provision, no data directory. Point it at a file and go.

## Quick start

```sh
make build
./bin/overlite                 # creates ./postgres.db, listens on :5432
./bin/overlite shop.db         # or point it at a file (same as --db shop.db)
```

Connect with any Postgres client:

```sh
psql "postgresql://postgres@127.0.0.1:5432/postgres?sslmode=disable"
```

```sql
CREATE TABLE users (id SERIAL PRIMARY KEY, name TEXT NOT NULL, profile JSONB);
INSERT INTO users (name, profile) VALUES ('ada', '{"role":"admin"}');
SELECT id, name, profile ->> 'role' AS role FROM users;
```

## Configuration

overlite follows PostgreSQL conventions, so it drops into the same tooling and
container setups.

| Flag | Env | Default | Description |
|---|---|---|---|
| `--driver` | `OVERLITE_DRIVER` | `postgres` | wire protocol to speak |
| `--host` | — | `127.0.0.1` | listen address (`0.0.0.0` to expose) |
| `--db` | — | `postgres.db` | SQLite file; its name becomes the database name |
| — | `POSTGRES_PORT` | `5432` | listen port (the driver's default, e.g. postgres = 5432) |
| — | `POSTGRES_USER` | `postgres` | role shown as owner |
| — | `POSTGRES_PASSWORD` | *(unset)* | when set, requires password auth (else trust) |
| — | `POSTGRES_SSL` | *(unset)* | `on` enables TLS with a self-signed cert (clients use `sslmode=require`) |
| — | `POSTGRES_SSL_CERT` / `POSTGRES_SSL_KEY` | *(unset)* | PEM cert/key to serve instead of self-signed |

The port belongs to the driver — postgres defaults to 5432 — and is only
overridden if you set `<DRIVER>_PORT` (e.g. `POSTGRES_PORT`).

```sh
POSTGRES_PASSWORD=secret POSTGRES_PORT=5544 ./bin/overlite --db shop.db
# -> database "shop", user "postgres", password auth, on :5544
```

## Schemas map to files

The file you point at is the `public` schema. Creating a schema creates a
sibling file, and pointing at a database re-discovers them automatically:

```sql
CREATE SCHEMA sales;            -- creates shop.sales.db, attaches it
CREATE TABLE sales.orders (...);
SELECT * FROM sales.orders;     -- cross-schema queries just work
```

So `shop.db` with `shop.sales.db` and `shop.audit.db` next to it exposes three
schemas: `public`, `sales`, `audit`.

## Status

High level, at a glance — including what's still needed to be **Postgres-ready
for a real production system**. ✅ done · 🟡 partial · ⬜ not yet.

| Area | Status | Notes |
|---|---|---|
| Wire protocol | ✅ | simple & extended, prepared statements, text/binary |
| SQL (DML) | ✅ | CRUD, joins, CTEs, window functions, upsert, `RETURNING` |
| DDL | ✅ | tables, indexes, views, foreign keys |
| Transactions | ✅ | real `BEGIN`/`COMMIT`/`ROLLBACK` with aborted-tx semantics |
| Schemas | ✅ | one file per schema, `CREATE`/`DROP`, auto-discovery |
| Core types | ✅ | int/text/real/bool/bytea, date/time, `SERIAL`, `json`/`jsonb` |
| JSON | ✅ | `->` `->>` `#>` `#>>` and the `json1` functions |
| COPY / bulk load | ✅ | `FROM STDIN` / `TO STDOUT`, `\copy` (text & CSV) |
| Introspection | ✅ | `pg_catalog` + `information_schema` (psql `\dt`/`\d`/`\l`, GUIs) |
| Authentication | 🟡 | trust, `POSTGRES_PASSWORD`, and TLS (`sslmode=require`); no SCRAM-SHA-256 |
| Concurrency | ✅ | dedicated connection per client; reads run in parallel, writes serialize (SQLite single-writer) |
| Migrations (`ALTER TABLE`) | 🟡 | add/drop/rename column; no `ALTER COLUMN TYPE` / `ADD CONSTRAINT` |
| Numeric precision | 🟡 | SQLite affinity; not exact fixed-point (money) |
| Timestamps with time zone | 🟡 | stored as text; no real `timestamptz` / tz math |
| Backup / restore | 🟡 | `\copy` works; `pg_dump` dumps data but not full schema |
| Roles & permissions | 🟡 | `CREATE`/`ALTER`/`DROP ROLE`/`USER` in `\du`; `GRANT`/`REVOKE` accepted as no-ops; no enforcement or row-level security |
| Server-side logic | ⬜ | PL/pgSQL functions & procedures (triggers only in SQLite syntax) |
| Sequences | ✅ | `CREATE`/`ALTER`/`DROP SEQUENCE`, `nextval`/`currval`/`setval`/`lastval`, `\ds`; `DEFAULT nextval()` in DDL not supported (use `SERIAL`) |
| Enum types | 🟡 | `CREATE`/`ALTER`/`DROP TYPE … AS ENUM`, `\dT`; enum columns become `TEXT` + a `CHECK`; no enum ordering semantics |
| Rich types | ⬜ | arrays, `uuid`, ranges, `interval` arithmetic |
| Extensions | ⬜ | `CREATE EXTENSION` (uuid-ossp, pgcrypto, postgis, …) |
| `LISTEN`/`NOTIFY`, query cancellation | ⬜ | |
| Replication / HA | ⬜ | |

Everything marked ✅ is exercised end-to-end against real `psql` and pgx
(`make test`). The 🟡/⬜ rows are the gap between "runs your app in dev" and
"drop-in Postgres for production".
