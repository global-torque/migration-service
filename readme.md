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

