package postgres

import (
	"context"

	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
	"github.com/webdevelop-pro/go-common/configurator"
	"github.com/webdevelop-pro/go-common/db"
	"github.com/webdevelop-pro/go-common/logger"
	"github.com/webdevelop-pro/migration-service/internal/domain/migration_log"
)

const NO_TABLE_CODE = "42P01"

type Repository struct {
	db *db.DB
}

// New returns new DB instance.
func New(c *configurator.Configurator) *Repository {
	poolConfig := db.GetConfigPool(c)
	// Migration SQL may contain rendered secrets. The shared pgx tracer records
	// complete SQL statements, so it must remain disabled for this repository
	// regardless of DB_LOG_LEVEL.
	poolConfig.ConnConfig.Tracer = nil
	pool := db.NewPoolFromConfig(poolConfig)

	return &Repository{
		db: db.NewDB(pool, logger.NewComponentLogger("migration-db", nil)),
	}
}

// UpdateServiceVersion updates service version.
func (r *Repository) UpdateServiceVersion(ctx context.Context, name string, ver int) error {
	const query = `INSERT INTO migration_services (name, version) VALUES ($1, $2) ON CONFLICT(name) DO UPDATE SET version=$2`
	_, err := r.db.Exec(ctx, query, name, ver)

	if err != nil {
		return errors.Wrapf(err, "query %s failed, params: %s %d", query, name, ver)
	}
	return nil
}

// GetServiceVersion returns currently deployed version of the service.
func (r *Repository) GetServiceVersion(ctx context.Context, name string) (int, error) {
	const query = `SELECT version FROM migration_services WHERE name=$1`

	var ver int

	err := r.db.QueryRow(ctx, query, name).Scan(&ver)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		// ToDo: Research why
		// reflect.TypeOf(err) return *pgconn.PgError
		// but errors.As(err, &pgErr) return false ...
		// if errors.As(err, &pgErr) {
		// if pgErr.Code == NO_TABLE_CODE {
		sErr := err.Error()
		if len(sErr) > 45 && sErr[0:45] == "ERROR: relation \"migration_services\" does not" {
			if createErr := r.CreateMigrationTable(context.Background()); createErr != nil {
				return 0, errors.Wrapf(createErr, "failed to initialize migration tables after query error: %s", err)
			}
			return r.GetServiceVersion(ctx, name)
		}
		return 0, errors.Wrapf(err, "query %s failed, %s ", query, name)
	}

	return ver, nil
}

// Exec executes query
func (r *Repository) Exec(ctx context.Context, sql string, arguments ...interface{}) error {
	return pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		_, err := tx.Exec(ctx, sql, arguments...)

		return err
	})
}

// CreateMigrationTable will create a migration table
func (r *Repository) CreateMigrationTable(ctx context.Context) error {
	const query = `CREATE TABLE IF NOT EXISTS migration_services (
	id serial NOT NULL PRIMARY KEY,
	name varchar NOT NULL UNIQUE,
	version int NOT NULL DEFAULT 0,
	created_at timestamp with time zone DEFAULT now() NOT NULL,
	updated_at timestamp with time zone NOT NULL DEFAULT NOW()
);
CREATE OR REPLACE FUNCTION update_at_set_timestamp()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER set_timestamp_migration_services
  BEFORE UPDATE ON migration_services
  FOR EACH ROW
  EXECUTE PROCEDURE update_at_set_timestamp();
CREATE TABLE IF NOT EXISTS migration_service_logs
(
    id                      SERIAL PRIMARY KEY,

    -- required
    migration_services_name character varying(255) NOT NULL,
    priority                integer                NOT NULL,
    version                 integer                NOT NULL,
    file_name               character varying(255) NOT NULL,
    sql                     text                   NOT NULL,
    hash                    character varying(255) NOT NULL,

    -- dates
    created_at              timestamptz            NOT NULL DEFAULT now(),
    updated_at              timestamptz            NOT NULL DEFAULT now()
);

ALTER TABLE public.migration_service_logs DROP CONSTRAINT IF EXISTS migration_service_logs_pk;
ALTER TABLE public.migration_service_logs DROP CONSTRAINT IF EXISTS migration_service_logs_complex_uindex;
ALTER TABLE public.migration_service_logs
    ADD CONSTRAINT migration_service_logs_pk
        UNIQUE (migration_services_name, priority, version, file_name);

CREATE OR REPLACE TRIGGER migration_service_logs_updated_at_timestamp
    BEFORE UPDATE
    ON migration_service_logs
    FOR EACH ROW
EXECUTE PROCEDURE update_at_set_timestamp();

CREATE INDEX IF NOT EXISTS migration_service_logs_hash_index
    on migration_service_logs (hash);
`
	_, err := r.db.Exec(ctx, query)

	if err != nil {
		return errors.Wrapf(err, "query %s failed.", query)
	}

	return nil
}

// WriteMigrationServiceLog inserts row to migration_service_logs
func (r *Repository) WriteMigrationServiceLog(ctx context.Context, log migration_log.MigrationServicesLog) error {
	const query = `INSERT INTO migration_service_logs (migration_services_name, priority, version, file_name, "sql", hash) 
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT(migration_services_name, priority, version, file_name) DO UPDATE 
		SET "sql"=$5, hash=$6`
	_, err := r.db.Exec(ctx, query, log.MigrationServiceName, log.Priority, log.Version, log.FileName, log.SQL, log.Hash)

	if err != nil {
		sErr := err.Error()
		if len(sErr) > 55 && sErr[0:55] == "ERROR: relation \"migration_service_logs\" does not exist" {
			if createErr := r.CreateMigrationTable(context.Background()); createErr != nil {
				return errors.Wrapf(createErr, "failed to initialize migration tables after query error: %s", err)
			}
			return r.WriteMigrationServiceLog(ctx, log)
		}
		return errors.Wrapf(err, "query %s failed, params: MigrationServiceName = %s, Priority = %d, "+
			"Version = %d, FileName = %s, SQL = %s, Hash = %s", query, log.MigrationServiceName, log.Priority,
			log.Version, log.FileName, log.SQL, log.Hash)
	}
	return nil
}

// GetHashFromMigrationServiceLog returns hash from migration_service_logs
func (r *Repository) GetHashFromMigrationServiceLog(ctx context.Context, log migration_log.MigrationServicesLog) (string, error) {
	var hash string
	const query = `SELECT hash FROM migration_service_logs
    	WHERE migration_services_name = $1 AND priority = $2 AND version = $3 AND file_name = $4`
	err := r.db.QueryRow(ctx, query, log.MigrationServiceName, log.Priority, log.Version, log.FileName).Scan(&hash)

	if errors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", errors.Wrapf(err, "query %s failed, params: MigrationServiceName = %s, Priority = %d, "+
			"Version = %d, FileName = %s", query, log.MigrationServiceName, log.Priority,
			log.Version, log.FileName)
	}
	return hash, nil
}
