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
| — | `OVERLITE_HBA_DIR` | `.` | directory holding `pg_hba.conf` and/or `pg_hba.yaml` (see below); overrides the global auth method |

The port belongs to the driver — postgres defaults to 5432 — and is only
overridden if you set `<DRIVER>_PORT` (e.g. `POSTGRES_PORT`).

```sh
POSTGRES_PASSWORD=secret POSTGRES_PORT=5544 ./bin/overlite --db shop.db
# -> database "shop", user "postgres", password auth, on :5544
```

## Host-based auth (`pg_hba`)

Drop a `pg_hba.conf` (classic Postgres format) or a `pg_hba.yaml` into
`OVERLITE_HBA_DIR` to decide the auth method — or a rejection — per connection,
by type / database / user / client CIDR. Rules are evaluated top-to-bottom;
**the first match wins**, and an unmatched connection is refused (as Postgres
does). If both files are present, `pg_hba.conf` takes precedence.

```conf
# TYPE  DATABASE  USER   ADDRESS         METHOD
host    all       all    127.0.0.1/32    trust
hostssl shop      app    10.0.0.0/8      scram-sha-256
host    all       all    0.0.0.0/0       reject
```

```yaml
hba:
  - { type: host,    database: all,  user: all, address: 127.0.0.1/32, method: trust }
  - { type: hostssl, database: shop, user: app, address: 10.0.0.0/8,   method: scram-sha-256 }
  - { type: host,    database: all,  user: all, address: 0.0.0.0/0,     method: reject }
```

Methods `trust`, `reject`, `scram-sha-256`, `md5`, and `password` are enforced;
`peer`/`cert` are accepted without their verification. Each role authenticates
against **its own password** — `CREATE ROLE alice LOGIN PASSWORD 'x'` stores a
SCRAM verifier (never plaintext), and roles without one fall back to
`POSTGRES_PASSWORD`.

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

Across the full feature matrix — **157 items: ✅ 140 · 🟡 16 · ⬜ 1**
(89% done, 99% at least partial):

| Area | ✅ | 🟡 | ⬜ |
|---|--:|--:|--:|
| Wire protocol | 14 | 0 | 0 |
| Authentication | 9 | 0 | 0 |
| DML (queries) | 12 | 1 | 0 |
| DDL (schema) | 29 | 6 | 0 |
| Data types | 11 | 4 | 0 |
| Transactions | 8 | 1 | 0 |
| Schemas | 10 | 0 | 0 |
| Catalog / introspection | 20 | 2 | 1 |
| Functions & dialect | 21 | 1 | 0 |
| Tooling (psql/pg_dump/GUIs) | 6 | 1 | 0 |

| Area | Status | Notes |
|---|---|---|
| Wire protocol | ✅ | simple & extended, prepared statements, text/binary |
| SQL (DML) | ✅ | CRUD, joins, CTEs, window functions, upsert, `RETURNING`, `LATERAL` (over set-returning functions) |
| DDL | ✅ | tables, indexes, views, foreign keys |
| Transactions | ✅ | real `BEGIN`/`COMMIT`/`ROLLBACK` with aborted-tx semantics |
| Schemas | ✅ | one file per schema, `CREATE`/`DROP`, auto-discovery |
| Core types | ✅ | int/text/real/bool/bytea, date/time, `SERIAL`, `json`/`jsonb` |
| JSON | ✅ | `->` `->>` `#>` `#>>`, containment `@>`/`<@`, key-existence `?`/`?|`/`?&`, and the `json1` functions |
| COPY / bulk load | ✅ | `FROM STDIN` / `TO STDOUT`, `\copy` (text & CSV) |
| Introspection | ✅ | `pg_catalog` + `information_schema` (psql `\dt`/`\d`/`\l`, GUIs) |
| Authentication | ✅ | trust, `POSTGRES_PASSWORD` with SCRAM-SHA-256 (default)/md5/cleartext, TLS, and per-connection `pg_hba` (conf or YAML) |
| Concurrency | ✅ | dedicated connection per client; reads run in parallel, writes serialize (SQLite single-writer) |
| Migrations (`ALTER TABLE`) | ✅ | add/drop/rename column, `ALTER COLUMN TYPE`/`NOT NULL`/`DEFAULT` (table rebuild), `ADD UNIQUE`; `ADD` PK/FK/CHECK not enforced |
| Numeric precision | 🟡 | exact decimal storage + compare/order + `sum`/`avg`; infix `+`/`-`/`*` still float |
| Timestamps with time zone | ✅ | `timestamptz` stores a UTC instant; offsets honored, `AT TIME ZONE` works; output always UTC |
| Backup / restore | ✅ | `\copy` and `pg_dump` (schema + data: types, constraints, sequences) |
| Roles & permissions | 🟡 | `\du` roles with enforced passwords, table ownership, `GRANT`/`REVOKE` of table privileges, role membership with `INHERIT`, `CREATEROLE`/`ADMIN OPTION` policing, and row-level security (`CREATE POLICY`); no column-level privileges |
| Server-side logic | 🟡 | `CREATE FUNCTION`/`PROCEDURE` accepted (body not executed); no PL/pgSQL engine; triggers in SQLite syntax |
| Sequences | ✅ | `CREATE`/`ALTER`/`DROP SEQUENCE`, `nextval`/`currval`/`setval`/`lastval`, `\ds`, and `DEFAULT nextval()` in DDL |
| Enum types | 🟡 | `CREATE`/`ALTER`/`DROP TYPE … AS ENUM`, `\dT`; enum columns become `TEXT` + a `CHECK`; no enum ordering semantics |
| `uuid` type | ✅ | stored as text; `gen_random_uuid()`/`uuid_generate_v4()` |
| Query cancellation | ✅ | `CancelRequest` interrupts a running query |
| Rich types | 🟡 | arrays, `hstore`, and range types supported (stored as JSON/text); geometric/network not yet; `interval` arithmetic partial |
| Extensions | 🟡 | `CREATE EXTENSION` accepted as a no-op; common functions provided directly |
| `LISTEN`/`NOTIFY` | ✅ | real in-memory pub/sub across sessions on the same server |
| Replication / HA | ⬜ | |

Everything marked ✅ is exercised end-to-end against real `psql` and pgx
(`make test`). The 🟡/⬜ rows are the gap between "runs your app in dev" and
"drop-in Postgres for production".

## Limitations

overlite stores in SQLite, so some Postgres features either can't be expressed
faithfully or aren't modeled yet. The full, current breakdown:

### Inherent to the SQLite backend

These would require emulating features SQLite fundamentally lacks:

- **Geometric / network types** — not modeled yet. (**Arrays**, **`hstore`**, and
  **range types** *are* supported, stored as JSON/text — see Partial.)
- **Infix `numeric` arithmetic** — SQLite's `+`/`-`/`*`/`/` operators are float,
  and can't be overridden. (`numeric` *storage* is exact, though — see Partial —
  and `dec_add`/`dec_mul`/… give exact results explicitly.) `money` and enforced
  scale aren't modeled.
- **Session `TimeZone` display** — `timestamptz` works (stores a UTC instant,
  honors input offsets, `AT TIME ZONE` converts via the Go zone database), but
  output is always rendered in UTC (`+00`); `SET timezone` doesn't change the
  display zone.
- **`ALTER TABLE ADD` PRIMARY KEY / FOREIGN KEY / CHECK** — accepted but not
  enforced (a `pg_dump` restore relies on this). (`ALTER COLUMN TYPE`/`NOT
  NULL`/`DEFAULT` and `ADD UNIQUE` *are* applied — see the ALTER TABLE note under
  Partial.)
- **PL/pgSQL** — `CREATE FUNCTION`/`PROCEDURE` are *accepted* (so migrations
  proceed) but the body isn't executed; there's no procedural-language engine.
- **`CREATE`/`DROP SCHEMA` inside a transaction** — schemas are attached files
  and `ATTACH` can't run in a SQLite transaction (a clear error is returned).
- **`SERIAL` in `pg_dump`** dumps as plain `integer` — on disk it is
  `AUTOINCREMENT`, not a sequence.

### Not implemented yet (but feasible)

- **`CREATE TYPE`** range / base types (`… AS ENUM` is modeled and `… AS
  (composite)` is recorded in the catalog; range/base are accepted as a no-op).
- **Planner statistics** (`pg_statistic`, `pg_statistic_ext`) — empty; SQLite
  keeps stats in a different shape (`sqlite_stat1`), not the Postgres one.
  Everything backed by real data *is* populated, though: `pg_auth_members`
  (role membership → `\du`/`\drg`), `pg_policy` + `pg_class.relrowsecurity`
  (RLS in `\d`), and `pg_depend`/`pg_shdepend` (object dependencies & ownership).
- No replication / HA.

### Accepted but not enforced (no-ops)

These run so migrations, ORMs, and dumps proceed, but have no real effect:

- **`COMMENT ON`** — accepted, not stored.
- **`CREATE`/`DROP EXTENSION`** — the common functions (e.g. `gen_random_uuid`)
  are provided directly.
- **`SET`/`BEGIN ISOLATION LEVEL`** — SQLite serializes writes; levels have no
  effect.
- **`ALTER … OWNER TO`, `ALTER SCHEMA`** — the creating role owns its tables
  (and that drives privilege/RLS bypass), but *reassigning* an object's owner
  isn't honored.

### Partial (works, with caveats)

- **Roles & privileges** — `CREATE`/`ALTER`/`DROP ROLE`/`USER` show up in `\du`,
  enforce a per-role `PASSWORD` (stored as a SCRAM verifier), drive
  `current_user`/`SET ROLE`, and enforce table `GRANT`/`REVOKE`
  (`SELECT`/`INSERT`/`UPDATE`/`DELETE`/`TRUNCATE`/`ALL`) against the connected
  role, with the creating role owning its tables. Role membership
  (`GRANT role TO role`) is enforced too: a member inherits the role's
  privileges transitively when it has `INHERIT`, else through `SET ROLE` —
  which is itself allowed only to a role the session user is a member of
  (superusers unrestricted; `SET SESSION AUTHORIZATION` is superuser-only).
  `CREATE`/`ALTER`/`DROP ROLE` require `CREATEROLE`, and membership can be
  granted on only by a superuser or a holder of `ADMIN OPTION` (which the
  creator gets automatically, and `WITH ADMIN OPTION` delegates).
  **Row-level security is fully enforced**: `ALTER TABLE … ENABLE`/`FORCE ROW
  LEVEL SECURITY` and `CREATE POLICY … USING (…) [WITH CHECK (…)]` filter
  `SELECT`/`UPDATE`/`DELETE` and validate every `INSERT` form (`VALUES`, bound
  params, `SELECT` with its source read-filtered, `DEFAULT VALUES`, and
  `ON CONFLICT` DO NOTHING/DO UPDATE), with permissive/restrictive combining,
  default-deny, and owner/superuser/`BYPASSRLS` bypass. Not modeled:
  column-level privileges, `GRANT` on schemas/sequences/functions, and the
  other role attributes (`SUPERUSER`, `CREATEDB`, …) beyond
  `INHERIT`/`CREATEROLE`/`BYPASSRLS`. Superusers and roles that were never
  `CREATE ROLE`'d bypass the checks, so existing single-user setups are
  unaffected.
- **`numeric` / `decimal`** — stored as the exact decimal string in a `TEXT`
  cell (so a value beyond float64 precision round-trips losslessly and the file
  stays plain-SQLite readable), with numeric comparison/ordering via a `DECIMAL`
  collation and exact `sum`/`avg` (via `math/big`). Caveat: infix `+`/`-`/`*`/`/`
  still go through SQLite's float operators (call `dec_add`/`dec_sub`/`dec_mul`/
  `dec_div`/`dec_round` for exact results), and `numeric(p,s)` scale isn't
  enforced on write.
- **`hstore`** — a key/value column stored as a JSON object (plain-SQLite
  readable). `'k=>v'::hstore` on input, hstore text (`"k"=>"v"`) on output, and
  `->>` (value), `?` (key existence), `akeys`/`avals`, `hstore(k,v)`. Caveats:
  `->` returns the JSON-quoted scalar (use `->>` for text), and a bare insert
  needs the `::hstore` cast to be parsed.
- **Range types** (`int4range`, `int8range`, `numrange`, `daterange`,
  `tsrange`, …) — stored as range text (`[1,10)`), with constructors,
  `lower`/`upper`/`isempty`/`lower_inc`/`upper_inc`, and `@>` containment. No
  discrete-range canonicalization or `&&`/`-` operators yet.
- **`LATERAL`** — the keyword is dropped, so `FROM t, LATERAL json_each(t.x)`
  (and other set-returning functions) work; a LATERAL correlated *subquery* that
  references the left side still can't (a SQLite limitation).
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
- **`ALTER TABLE`** — add/drop/rename column, rename table, `ALTER COLUMN
  TYPE`/`SET`/`DROP NOT NULL`/`DEFAULT` (via a create-copy-swap table rebuild
  that preserves data, indexes, and triggers), and `ADD [CONSTRAINT] UNIQUE`
  (a real unique index). `ADD` PRIMARY KEY/FOREIGN KEY/CHECK are accepted but
  not enforced.
- **DBeaver** — connects and browses/reads data; full validation is in progress.
