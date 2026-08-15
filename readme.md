# Database migration service

### Streamlining Your Schema Transition
Migration service stands as a beacon of resilience and security in the realm of database migrations. It's expertly designed to handle the intricacies of database schema conversion, data migration, and seed uploading with unmatched proficiency. At the heart of the service lies a straightforward yet powerful concept: maintaining the database schema state within the `migration_services` table.

- Each migration within our service is assigned a unique revision, ensuring a meticulous and organized execution in ascending order.
- Migration service can be used to run tests, see [tests examples](/tests/migrations/RequiredEnv/BranchInvertion/01_user/01_init.sql#L1) or [in-file-configuration](#in-file-configurations)

## Structure
All migrations files located in the `migrations/` folder.
Migration service reads file one by one in alphabetical order and execute it one by one.
In order to work properly migration service require `migration_services` and `migration_service_logs` tables to be created first:
```sh
set -a && source .dev.env && go run cmd/server/main.go --init
```

## File structure
Every file represented by `.sql` standard which parameters in the first comment.
```
- migrations/
- migrations/<PROIRITY>_<service_name>                        --- We set up priority and service name 
- migrations/<PROIRITY>_<service_name>/<VERSION>_<TITLE>.sql  --- We set up migration version and short description
```

## In file configurations
First line in every file can be pass configuration for the migration service.
- `allow_error: true/false` - will define if service will fail or will continue working during SQL error
- `required_env: [regex]` - will apply migrations only for specific git branch. Check [tests/migrations/RequiredEnv](./tests/migrations/RequiredEnv) files for more examples. Its been used in combination with ENV_NAME variable, check [TestRequiredEnvMultipleBranch](./tests/main_test.go#L357) test for more info. Useful to upload seeds and other temporary data for dev or stage envs but not for production.

__Example__:
```sql
--- allow_error: false, required_env: !master 
CREATE TABLE migration_services (
  id serial NOT NULL PRIMARY KEY,
  name varchar NOT NULL UNIQUE,
  version int NOT NULL DEFAULT 0,
  created_at timestamp with time zone DEFAULT now() NOT NULL
);
CREATE TABLE user_users(id serial primary key);
```

## Usage example
There is two main migration service usage:
- running migrations locally.
```bash
# set -a && source .dev.env && go run cmd/main/main.go
```
will apply all new migrations locally
- automatically applying migrations during merging to dev|stage|master branch
- Once github PR reviewed and merged to one of those branches service will execute new migrations automatically.

[Check](example/) full usage example [here](example/)


## Env variables
check `.example.env` file 

### Variables in migration SQL

Environment variables whose names start with `MIGRATION_` can be inserted as
values during migration execution. Use an unquoted `${MIGRATION_NAME}`
placeholder as a standalone value token in the SQL file:

```sql
INSERT INTO service_credentials (service_name, api_key, timeout_seconds)
VALUES ('payments', ${MIGRATION_PAYMENTS_API_KEY}, ${MIGRATION_TIMEOUT_SECONDS});
```

Variable names must match `MIGRATION_[A-Z0-9_]+`. Make every referenced
variable available to the migration-service process through its shell,
container, or deployment secret configuration before applying the migration:

```sh
export MIGRATION_PAYMENTS_API_KEY="<injected secret>"
export MIGRATION_TIMEOUT_SECONDS="30"
MIGRATION_DIR=./migrations/ ./app --apply-only=true
```

Every replacement is a PostgreSQL string literal. Add an explicit cast when
the intended type would otherwise be unclear, for example
`${MIGRATION_TIMEOUT_SECONDS}::integer`, `${MIGRATION_TENANT_ID}::uuid`, or
`${MIGRATION_RULES_JSON}::jsonb`. Do not put quotes around the placeholder;
`'${MIGRATION_PAYMENTS_API_KEY}'` is rejected.

The runner renders each value as a PostgreSQL string literal, so quotes,
newlines, and SQL-like content in a value cannot change the SQL structure when
the placeholder passes this validation.
PostgreSQL will coerce the literal when the destination is another type, such
as an integer, UUID, or JSON column. Placeholders are for values only; they
cannot be used for identifiers or raw SQL fragments, and they must not be
surrounded by single quotes. The runner rejects placeholders inside line or
block comments, single-quoted strings, double-quoted identifiers, and
dollar-quoted data or function bodies. It also rejects placeholders touching
an identifier character. To avoid depending on the session's
`standard_conforming_strings` setting, a variable-bearing migration also
rejects backslashes inside ordinary SQL strings; use an explicit `E'...'`
escape string when needed. Put dynamic procedural values in a top-level DML
statement rather than inside a `DO` or function body.

An unset variable fails the migration before its SQL is sent to PostgreSQL. An
explicitly set empty value is allowed. Variable-resolution errors are always
fatal, even when the migration uses `allow_error: true`. Migration hashes,
migration-service application logs, rows in `migration_service_logs`, and the
`--final-sql` output retain the placeholder rather than storing the resolved
value. PostgreSQL execution error details are redacted by the runner because
they can echo input values; the wrapped database error remains available to Go
callers through `errors.Is` and `errors.As`. The migration repository also
disables pgx SQL tracing regardless of `DB_LOG_LEVEL`, because that tracer logs
complete rendered statements.

The resolved value must still be sent to PostgreSQL. PostgreSQL statement,
audit, or parameter logging can therefore record it outside the migration
service. Review those database-side logging settings before using this feature
for secrets. PostgreSQL can log a failed statement by default; secret-bearing
migration jobs need appropriately restricted database roles and operator-owned
settings for statement/error/duration/audit logging and `pg_stat_activity`
access. If database-side query-text secrecy is mandatory, do not use textual
SQL variables.

Because `--final-sql` intentionally retains placeholders, its output is an
audit/debug view and is not directly executable by PostgreSQL.

### Environment-scoped identifiers

Migration SQL may use the exact `{{ ENV_NAME }}` marker inside an unquoted
PostgreSQL identifier, for example:

```sql
CREATE ROLE svc_{{ ENV_NAME }}_queue_api NOLOGIN;
```

The runner renders this marker only immediately before execution. `ENV_NAME`
must be set and match `[a-z][a-z0-9_]*`; the complete generated identifier must
be at most 63 bytes. Unknown, nested, quoted, commented, or dollar-quoted
template markers are rejected. This is the only supported identifier
interpolation—use `${MIGRATION_*}` only for standalone SQL values.
For marker-bearing SQL, an ordinary single-quoted string containing a
backslash is also rejected because its parsing depends on
`standard_conforming_strings`; use an explicit `E'...'` escape string.

Hashes, migration logs, and `--final-sql` retain the canonical unrendered SQL,
so the migration identity is stable across environments.

## Application options

### --init
creates migration table
```sh 
set -a && source .dev.env && go run cmd/server/main.go --init
```

### --force
force apply migration without version checking. Can accept multiply files or dir paths. Will not update service version if applied version is lower, then already applied
```sh 
set -a && source .dev.env && go run cmd/server/main.go --force ./migrations/01_user_user ./migrations/02_email_emails/02_add_id.sql
```

### --fake
do not apply any migration but mark according migrations in `migration_services` table as completed. Can accept multiply files or dir paths
```sh 
set -a && source .dev.env && go run cmd/server/main.go --fake ./migrations/01_user_user ./migrations/02_email_emails/02_add_id.sql
```

### --check
Verifies if all hashes of migrations are equal to those in migration table. If no - returns list of files with migrations, that have differences. Can accept files or dirs of migrations as arguments
```sh 
set -a && source .dev.env && go run cmd/server/main.go --check
```

```sh 
set -a && source .dev.env && go run cmd/server/main.go --check ./migrations/01_user_user ./migrations/02_email_emails/02_add_id.sql
```

### --check-apply
Compares hashes of all migrations with hashes in DB and try to apply those, that have differences. Can accept files or dirs of migrations as arguments
```sh 
set -a && source .dev.env && go run cmd/server/main.go --check-apply
```

```sh 
set -a && source .dev.env && go run cmd/server/main.go --check-apply ./migrations/01_user_user ./migrations/02_email_emails/02_add_id.sql
```

# ToDo
- [ ] refactor app and http using generic responses https://github.com/webdevelop-pro/go-common/tree/master/server/response#response-component
- [ ] add integration with sqllite
- [ ] update go-common and logger
- [ ] remove http server (we don't use it now)
- [ ] migrate to alpine image, understand how zip works in alpine
