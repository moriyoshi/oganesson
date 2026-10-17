# Data, databases, and ORMs

Oganesson supplies one built-in key-value API and Go-backed substitutes for
several database drivers that normally depend on Node sockets or native addons.
The driver services are process-wide so connection state can survive the fresh
JavaScript VM used for each request.

## `og:kv`

`og:kv` follows the commonly used Deno KV API:

```ts
import { KvU64, openKv } from "og:kv";

const kv = await openKv();
await kv.set(["session", id], { user: "ada" }, { expireIn: 30 * 60_000 });

const current = await kv.get<KvU64>(["visits"]);
const committed = await kv.atomic()
  .check(current)
  .sum(["visits"], 1n)
  .commit();
```

Implemented operations are:

- tuple keys containing strings, bytes, numbers, bigints, and booleans;
- `get`, typed `getMany`, `set`, and `delete`;
- expiration in milliseconds;
- ordered `list` by prefix or range, with limit, cursor, reverse, and batch size;
- optimistic atomic checks;
- atomic set/delete mutations; and
- unsigned 64-bit `sum`, `min`, and `max` through `KvU64`.

Values preserve ordinary structured data used by applications, including
cycles, arrays, plain objects, `Date`, `RegExp`, `Map`, `Set`, `ArrayBuffer`,
typed arrays, `bigint`, special numbers, `undefined`, and `Error`. Functions,
symbols, and arbitrary class instances cannot be stored.

`openKv()` always returns the same in-memory process store; its optional path is
accepted for API compatibility but does not select a persistent backend. Data
is shared by requests, lost on restart, and not shared across processes.

## SQLite

Imports of `better-sqlite3` use a compatible JavaScript surface backed by
pure-Go SQLite. Covered behavior includes:

- opening file-backed and in-memory databases;
- `exec`, `prepare`, `run`, `get`, `all`, and iteration;
- positional and named parameters;
- transactions and nested savepoints used by ORMs;
- result metadata such as affected rows and inserted IDs; and
- Buffer/blob, number, bigint, text, null, and date-related mappings needed by
  validated consumers.

Relative database paths live in the runtime's in-memory overlay and disappear
on restart. For persistence, pass an absolute host path, for example:

```sh
DB_FILE=/var/lib/my-app/app.db ./my-app
```

`@libsql/client` is also substituted for local `file:` and `:memory:` URLs. Its
remote HTTP/Turso transport is not implemented.

## PostgreSQL

The following package entry points have Go-backed implementations:

- `pg` / node-postgres, including Pool and Client shapes used by applications
  and adapters; and
- `postgres` / postgres.js, including tagged templates, `unsafe`, values,
  transactions, and savepoints used by Drizzle.

The service uses pgx underneath and shares pools across request VMs. Plaintext
is the default, matching the JavaScript drivers; enabling SSL selects encrypted
connections. Common query rows, parameters, errors, transactions, and adapter
flows are covered.

Not every driver option is mapped. Applications depending on uncommon timeout,
session, certificate, parser, streaming-query, or connection customization must
test it explicitly.

## MySQL

`mysql2` and `mysql2/promise` are implemented over Go's MySQL driver. Covered
behavior includes:

- callback and promise APIs;
- pools and acquired clients;
- queries and writes;
- `rowsAsArray` for Drizzle;
- ResultSetHeader fields used by ORMs; and
- common MySQL value and error mappings.

The service shares Go pools across requests. Advanced mysql2 options are not all
mapped: custom `typeCast`, query streaming, full TLS certificate configuration,
and some session options are outside the validated surface.

## ORMs and query builders

Validated combinations include:

| Library | Drivers and features |
|---------|----------------------|
| Drizzle | `better-sqlite3`, `@libsql/client` local mode, node-postgres, postgres.js, and mysql2; schema/query builders and transactions |
| Kysely | SQLite, PostgreSQL, and MySQL dialect flows used by the fixtures |
| TypeORM | `better-sqlite3`, entities with TypeScript decorator metadata, schema operations, relations, and transactions |
| Prisma 7 | generated client with `@prisma/adapter-better-sqlite3`, `@prisma/adapter-pg`, and `@prisma/adapter-mariadb` (MySQL), queries, relations, aggregation, raw queries, and transactions |

TypeORM glob-based discovery of separately emitted entity or migration files is
not generally traced. Import entities normally so they enter the server bundle.

## Prisma generation

When a supported build script invokes `prisma generate`, Oganesson runs Prisma's
own generator in the build host. The generated TypeScript client is then bundled
normally. Prisma applications use driver adapters; the native Prisma query
engine executable is not used.

Only `prisma generate` is recognized from the generator family. Other generator
commands and project `postinstall` scripts are not implicitly executed.

## Prisma migrations and `db push`

Oganesson can package Prisma's WebAssembly schema engine and apply the schema at
application startup:

- a project with `prisma/migrations` uses migrate-deploy semantics;
- a build script containing `prisma db push` packages the schema and uses
  schema-push semantics instead;
- `--accept-data-loss` on `db push` is preserved;
- SQLite and PostgreSQL migrate-deploy paths are implemented; and
- SQLite `db push` is validated.

The datasource is read from `prisma.config.*`. A literal URL can be packaged;
an `env("NAME")` URL is resolved from that environment variable when the app
starts, preventing a build-time secret from being embedded in the binary.

Startup migration runs before the application begins serving requests. A
migration failure stops startup. PostgreSQL migration requires a real
multi-session server because Prisma's schema engine uses advisory locking and
additional connections. MySQL/MariaDB migration is not available: the published
`@prisma/adapter-mariadb` does not implement the schema engine's migration-aware
interface (its `executeScript` is a stub and it exposes no shadow-database
connection), so a MySQL datasource reports an unsupported-provider error for
migrate-deploy and `db push`. The MySQL query path (above) is unaffected.
PostgreSQL/MySQL `db push` is likewise not yet end-to-end validated.

## Connection and persistence guidance

Creating a JavaScript Pool or Client in a request VM does not create durable
JavaScript state, but the native Go service can reuse underlying pools and
sessions. Continue to close or release application-level clients normally; the
service remains available for later requests and closes when the app shuts
down.

For horizontal deployment, use an external database. None of the process-local
services (`og:kv`, ISR, Cache Components, virtual-file writes) synchronize
across replicas.
