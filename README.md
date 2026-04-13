# pg-rds-proxy

A small, fast PostgreSQL wire-protocol proxy written in Go that lets **Webmin** and **Virtualmin** manage a PostgreSQL database hosted on **Amazon RDS**.

RDS PostgreSQL hides a handful of superuser-only catalogs (`pg_shadow`, `pg_authid`, ...). Webmin's `postgresql` module and Virtualmin's `feature-postgres.pl` still query those catalogs directly, which causes parts of the UI and domain lifecycle to fail against RDS. `pg-rds-proxy` sits between Webmin/Virtualmin and RDS, intercepts exactly those statements at the wire-protocol level, and rewrites them into queries against the equivalent non-privileged views (`pg_user`, `pg_roles`) that RDS does expose.

This proxy is deliberately scoped to the needs of Virtualmin/Webmin. It is not a general-purpose PostgreSQL compatibility shim.

## Why this exists

Upstream Webmin and Virtualmin were written against self-managed PostgreSQL where the admin role can read `pg_shadow` and `pg_authid`. On RDS the master user is not a real superuser and those reads return `permission denied`. The two concrete failure points we need to fix are:

1. **Virtualmin `postgres_user_exists`** (`virtualmin-gpl/feature-postgres.pl:86`)
   ```sql
   select * from pg_shadow where usename = ?
   ```
   Called every time Virtualmin checks whether a domain's PostgreSQL user already exists. Fails on RDS, which makes `Create Virtual Server` and `Disable/Enable Features` intermittently error out.

2. **Virtualmin domain deletion** (`virtualmin-gpl/feature-postgres.pl:380`)
   ```sql
   select datname from pg_database
   join pg_authid on pg_database.datdba = pg_authid.oid
   where rolname = '$user'
   ```
   Used to find databases owned by the user being removed so ownership can be reassigned. Fails on RDS because of the `pg_authid` join, leaving orphaned databases behind.

3. **Webmin user admin** (`webmin/postgresql/postgresql-lib.pl:1333`, `get_pg_shadow_table`)
   When the module's `useshadow` config is on, Webmin reads the columns
   ```
   usename, usesysid, usecreatedb, usesuper, usecatupd, passwd, valuntil
   ```
   directly from `pg_shadow`. Fails on RDS. Setting `useshadow=0` in the module config makes Webmin hit `pg_user` instead, which does work on RDS, but users regularly forget or can't edit that file, and Virtualmin's bundled config ships with the shadow path enabled on several distros.

Everything else the two projects query (`pg_user`, `pg_database`, `pg_tables`, `pg_indexes`, `pg_views`, `pg_class`, `pg_namespace`, `pg_type`, `pg_attribute`, `pg_statio_user_sequences`) is already readable on RDS and passes through the proxy untouched.

## How it works

```
  Webmin / Virtualmin  <-->  pg-rds-proxy  <-->  RDS PostgreSQL
                            (pgwire v3)          (pgwire v3, TLS)
```

1. Terminate the PostgreSQL frontend connection from Webmin/Virtualmin.
2. Open a pooled backend connection to the RDS endpoint (TLS, password or IAM auth).
3. For every `Query` and `Parse` message, apply a cheap prefilter that looks for the trigger tokens `pg_shadow` and `pg_authid`. Statements without those tokens are forwarded byte-for-byte.
4. Matching statements are rewritten using a small set of fixed rules and then forwarded.
5. Results from RDS stream back to the client unchanged.

Because the prefilter rejects >99% of traffic before any parsing happens, the proxy adds negligible overhead to normal Webmin/Virtualmin activity.

## Rewrite rules

The entire rule set is table-driven and lives in `internal/rewrite/rules.go`. It is intentionally short:

| Client sends | Proxy forwards to RDS | Notes |
|---|---|---|
| `pg_shadow` (any reference) | `pg_user` | Same column names; `passwd` is returned as `********` by RDS, which is what Webmin already expects when running as a non-superuser. |
| `pg_authid` (any reference) | `pg_roles` | `pg_roles` is a view over `pg_authid` that RDS does expose, with `rolpassword` masked and `oid` preserved, so the Virtualmin `pg_database.datdba = pg_authid.oid` join keeps working. |

That's it. No other catalog is touched. If Webmin or Virtualmin upstream adds another superuser-only query in the future, the fix is to add one row to that table.

## Quick start

### Build

```sh
make build          # host binary in ./bin/pg-rds-proxy
make build-linux    # static linux/amd64 in ./bin/pg-rds-proxy.linux-amd64
make test           # unit tests (rewrite rules)
make docker         # OCI image (linux/amd64) via scripts/build-docker.sh
make deb            # .deb package in ./dist/ via scripts/build-deb.sh
```

Both `make docker` and `make deb` run entirely inside containers, so Docker is the only host dependency. The deb target produces a package that installs:

- `/usr/bin/pg-rds-proxy` &nbsp;the binary
- `/lib/systemd/system/pg-rds-proxy.service` &nbsp;a hardened systemd unit
- `/etc/pg-rds-proxy/pg-rds-proxy.conf` &nbsp;the daemon's config file (mode `0640`, owned by `root:pg-rds-proxy`)

On install, the package creates a dedicated `pg-rds-proxy` system user, enables the unit, and reloads systemd. To bring the daemon up:

```sh
sudoedit /etc/pg-rds-proxy/pg-rds-proxy.conf   # set PGRP_UPSTREAM=...
sudo systemctl start pg-rds-proxy
sudo systemctl status pg-rds-proxy
journalctl -u pg-rds-proxy -f
```

The service invokes `/usr/bin/pg-rds-proxy --config /etc/pg-rds-proxy/pg-rds-proxy.conf`. The binary reads that file directly (simple `KEY=VALUE` format), so you can also run it outside systemd with the same config: `pg-rds-proxy --config /etc/pg-rds-proxy/pg-rds-proxy.conf`. Real environment variables and CLI flags override anything in the file.

### Run

```sh
pg-rds-proxy \
  --listen 127.0.0.1:5532 \
  --upstream mydb.cluster-xxxx.us-east-1.rds.amazonaws.com:5432 \
  --upstream-tls require \
  --upstream-user virtualmin_admin \
  --upstream-password-env RDS_PASSWORD
```

### Point Webmin/Virtualmin at it

In Webmin, go to **Servers -> PostgreSQL Database Server -> Module Config** and set:

- **Host for TCP connection**: `127.0.0.1` (the proxy)
- **Port for TCP connection**: `5532` (the proxy)
- **Login for administration**: the RDS master user
- **Password for administration**: the RDS master password

Virtualmin inherits these settings from the Webmin module, so no separate configuration is needed on the Virtualmin side.

### Configuration

| Flag | Description | Default |
|---|---|---|
| `--listen` | Address the proxy binds to for Webmin/Virtualmin | `127.0.0.1:5532` |
| `--upstream` | RDS endpoint `host:port` | required |
| `--upstream-tls` | TLS mode: `disable`, `require`, `verify-full` | `require` |
| `--upstream-user` | Backend login role | required |
| `--upstream-password-env` | Env var holding the backend password | |
| `--iam-auth` | Use RDS IAM token auth instead of password | `false` |
| `--max-conns` | Backend pool size | `16` |
| `--idle-timeout` | Idle backend eviction | `5m` |
| `--log-level` | `debug`, `info`, `warn`, `error` | `info` |

Webmin/Virtualmin open very few concurrent connections, so the default pool is small on purpose.

## Observability

- Structured JSON logs with a per-connection correlation id.
- A `--log-rewrites` flag that logs every statement the proxy actually rewrites, which is useful for confirming that a Webmin/Virtualmin action is going through the expected path.
- Optional Prometheus endpoint (`--metrics :9100`) exposing connection counts, rewrite hit rate, and backend round-trip latency.

## Project layout

```
cmd/pg-rds-proxy/     main entrypoint and flag wiring
internal/proxy/       pgwire accept loop, frontend/backend pumps
internal/rewrite/     the two rewrite rules and their tests
packaging/            systemd unit, env file, nfpm deb config
scripts/              build-docker.sh, build-deb.sh
Dockerfile            multi-stage build, distroless runtime
Makefile              build / build-linux / test / docker / deb
```

## Scope and non-goals

- **In scope:** keeping `feature-postgres.pl` and the Webmin `postgresql` module functional against RDS without patching either project.
- **Out of scope:** general PostgreSQL compatibility, query routing, connection multiplexing for unrelated clients, SQL firewalling, schema migration. Use `pgbouncer` or a real proxy like `pgcat` if you need those.

## Upstream references

For context, the specific call sites this proxy exists to support:

- `virtualmin-gpl/feature-postgres.pl` &nbsp;`postgres_user_exists`, `delete_postgres`
- `webmin/postgresql/postgresql-lib.pl` &nbsp;`get_pg_shadow_table`, `$pg_shadow_cols`

Both repos are mirrored on GitHub under `virtualmin/virtualmin-gpl` and `webmin/webmin`.

## License

TBD.
