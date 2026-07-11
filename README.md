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
| Authentication | ✅ | trust, `POSTGRES_PASSWORD` with SCRAM-SHA-256 (default), md5, or cleartext via `POSTGRES_HOST_AUTH_METHOD`; TLS |
| Concurrency | ✅ | dedicated connection per client; reads run in parallel, writes serialize (SQLite single-writer) |
| Migrations (`ALTER TABLE`) | 🟡 | add/drop/rename column; no `ALTER COLUMN TYPE` / `ADD CONSTRAINT` |
| Numeric precision | 🟡 | SQLite affinity; not exact fixed-point (money) |
| Timestamps with time zone | 🟡 | stored as text; no real `timestamptz` / tz math |
| Backup / restore | ✅ | `\copy` and `pg_dump` (schema + data: types, constraints, sequences) |
| Roles & permissions | 🟡 | `CREATE`/`ALTER`/`DROP ROLE`/`USER` in `\du`; `GRANT`/`REVOKE` accepted as no-ops; no enforcement or row-level security |
| Server-side logic | ⬜ | PL/pgSQL functions & procedures (triggers only in SQLite syntax) |
| Sequences | ✅ | `CREATE`/`ALTER`/`DROP SEQUENCE`, `nextval`/`currval`/`setval`/`lastval`, `\ds`; `DEFAULT nextval()` in DDL not supported (use `SERIAL`) |
| Enum types | 🟡 | `CREATE`/`ALTER`/`DROP TYPE … AS ENUM`, `\dT`; enum columns become `TEXT` + a `CHECK`; no enum ordering semantics |
| `uuid` type | ✅ | stored as text; `gen_random_uuid()`/`uuid_generate_v4()` |
| Query cancellation | ✅ | `CancelRequest` interrupts a running query |
| Rich types | ⬜ | arrays, `hstore`, ranges; `interval` arithmetic is partial |
| Extensions | 🟡 | `CREATE EXTENSION` accepted as a no-op; common functions provided directly |
| `LISTEN`/`NOTIFY` | 🟡 | accepted as no-ops (no delivery) |
| Replication / HA | ⬜ | |

Everything marked ✅ is exercised end-to-end against real `psql` and pgx
(`make test`). The 🟡/⬜ rows are the gap between "runs your app in dev" and
"drop-in Postgres for production".

## Limitations

overlite stores in SQLite, so some Postgres features either can't be expressed
faithfully or aren't modeled yet. The full, current breakdown:

### Inherent to the SQLite backend

These would require emulating features SQLite fundamentally lacks:

- **Arrays** (`int[]`, `text[]`), **`hstore`**, geometric / network / range types
  — SQLite has no array/composite storage.
- **Exact `numeric`/`decimal`** precision & scale, and **`money`** — SQLite uses
  type affinity, not fixed-point.
- **`timestamptz`** / real time-zone math — timestamps are stored as text.
- **`ALTER TABLE ALTER COLUMN TYPE`** and **`ADD CONSTRAINT`** — SQLite can't
  change a column's type or add a constraint to an existing table. (A `pg_dump`
  restore still loads data/tables/sequences; those constraint statements are
  accepted but not enforced.)
- **`DEFAULT nextval('seq')` in DDL** — SQLite rejects non-constant defaults;
  use `SERIAL` (which maps to `INTEGER PRIMARY KEY AUTOINCREMENT`).
- **`CREATE FUNCTION` / PL/pgSQL / stored procedures.**
- **`LATERAL` joins.**
- **`CREATE`/`DROP SCHEMA` inside a transaction** — schemas are attached files
  and `ATTACH` can't run in a SQLite transaction.
- **JSON key-existence operators `?`, `?|`, `?&`** — `?` collides with the
  parameter placeholder (containment `@>` / `<@` *is* supported).
- **`SERIAL` in `pg_dump`** dumps as plain `integer` — on disk it is
  `AUTOINCREMENT`, not a sequence.

### Not implemented yet (but feasible)

- **`CREATE TYPE`** range / base types (`… AS ENUM` is modeled and `… AS
  (composite)` is recorded in the catalog; range/base are accepted as a no-op).
- **Multiple users / `pg_hba`-style** host rules (one configured role/password).
- **Remainder of `pg_catalog`** (`pg_depend`, `pg_statistic`, …) — present but
  empty.
- No `LISTEN`/`NOTIFY` delivery, and no replication / HA.

### Accepted but not enforced (no-ops)

These run so migrations, ORMs, and dumps proceed, but have no real effect:

- **`GRANT` / `REVOKE`** and role attributes — no per-object privileges or RLS.
- **`COMMENT ON`** — accepted, not stored.
- **`CREATE`/`DROP EXTENSION`** — the common functions (e.g. `gen_random_uuid`)
  are provided directly.
- **`LISTEN` / `UNLISTEN` / `NOTIFY`** — no message delivery.
- **`SET`/`BEGIN ISOLATION LEVEL`** — SQLite serializes writes; levels have no
  effect.
- **`ALTER … OWNER TO`, `ALTER SCHEMA`** — ownership isn't modeled.

### Partial (works, with caveats)

- **Roles** — `CREATE`/`ALTER`/`DROP ROLE`/`USER` show up in `\du`, but
  attributes aren't enforced.
- **Enum columns** — enforced via `TEXT` + `CHECK` and `\dT+` lists the
  elements, but there's no enum ordering/comparison.
- **Composite types** — `CREATE TYPE … AS (…)` shows in `pg_type`/`\dT`, but the
  fields aren't modeled (no composite storage).
- **`DISTINCT ON (...)`** — rewritten to a `ROW_NUMBER()` window; needs an
  explicit column list (not `SELECT *`).
- **`interval`** — `ts ± interval '1 day'` and `age()` work; no bare `interval`
  value type.
- **`information_schema`** — constraints/columns/views/sequences/routines
  populated; `check_constraints` is empty and column precision is `NULL`.
- **Type OIDs** — common ones are faithful; all integers advertise as `int8`
  (catalog oids exceed int4) and `numeric` advertises as `float8`.
- **`SET search_path`** and **`COLLATE`** — accepted; unqualified names always
  resolve in `public`/`main`, and unsupported collations are dropped.
- **`ALTER TABLE`** — add/drop/rename column and rename table only.
- **DBeaver** — connects and browses/reads data; full validation is in progress.
