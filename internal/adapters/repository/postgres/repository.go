package postgres

import (
	"context"
	stderrors "errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	pkgerrors "github.com/pkg/errors"
	"github.com/webdevelop-pro/go-common/db"
	"github.com/webdevelop-pro/migration-service/internal/adapters"
	"github.com/webdevelop-pro/migration-service/internal/domain/migration_log"
)

const noTableCode = "42P01"
const startupTimeout = 30 * time.Second

const updateServiceVersionQuery = `INSERT INTO migration_services (name, version) VALUES ($1, $2) ON CONFLICT(name) DO UPDATE SET version=$2`

const writeMigrationServiceLogQuery = `INSERT INTO migration_service_logs (migration_services_name, priority, version, file_name, "sql", hash) 
		VALUES ($1, $2, $3, $4, $5, $6) ON CONFLICT(migration_services_name, priority, version, file_name) DO UPDATE 
		SET "sql"=$5, hash=$6`

type Repository struct {
	db *db.DB
}

// New returns new DB instance.
func New() (*Repository, error) {
	ctx, cancel := context.WithTimeout(context.Background(), startupTimeout)
	defer cancel()

	return NewWithContext(ctx)
}

// NewWithContext returns new DB instance using the provided startup context.
func NewWithContext(ctx context.Context) (*Repository, error) {
	database, err := db.New(ctx)
	if err != nil {
		return nil, fmt.Errorf("create migration repository db: %w", err)
	}

	return &Repository{
		db: database,
	}, nil
}

// Close closes the underlying database pool.
func (r *Repository) Close() {
	if r.db != nil {
		r.db.Close()
	}
}

// UpdateServiceVersion updates service version.
func (r *Repository) UpdateServiceVersion(ctx context.Context, name string, ver int) error {
	if err := updateServiceVersion(ctx, r.db, name, ver); err != nil {
		return pkgerrors.Wrapf(err, "query %s failed, params: %s %d", updateServiceVersionQuery, name, ver)
	}
	return nil
}

// GetServiceVersion returns currently deployed version of the service.
func (r *Repository) GetServiceVersion(ctx context.Context, name string) (int, error) {
	const query = `SELECT version FROM migration_services WHERE name=$1`

	var ver int

	err := r.db.QueryRow(ctx, query, name).Scan(&ver)
	if err != nil {
		if stderrors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		if isUndefinedTable(err) {
			if err := r.CreateMigrationTable(ctx); err != nil {
				return 0, pkgerrors.Wrapf(err, "query %s failed", query)
			}
			return r.GetServiceVersion(ctx, name)
		}
		return 0, pkgerrors.Wrapf(err, "query %s failed, %s ", query, name)
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

// ApplyMigration applies migration SQL and records its metadata in one transaction.
func (r *Repository) ApplyMigration(ctx context.Context, migration adapters.AppliedMigration) (adapters.ApplyMigrationResult, error) {
	result := adapters.ApplyMigrationResult{QuerySucceeded: true}

	err := pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, "SAVEPOINT migration_query"); err != nil {
			return fmt.Errorf("create migration savepoint: %w", err)
		}

		if _, err := tx.Exec(ctx, migration.Query); err != nil {
			result.QuerySucceeded = false
			result.QueryError = err

			if _, rollbackErr := tx.Exec(ctx, "ROLLBACK TO SAVEPOINT migration_query"); rollbackErr != nil {
				return fmt.Errorf("rollback migration savepoint: %w", stderrors.Join(rollbackErr, err))
			}

			if !migration.AllowError {
				return fmt.Errorf("execute migration query: %w", err)
			}
		} else if _, err := tx.Exec(ctx, "RELEASE SAVEPOINT migration_query"); err != nil {
			return fmt.Errorf("release migration savepoint: %w", err)
		}

		if migration.UpdateVersion {
			if err := updateServiceVersion(ctx, tx, migration.ServiceName, migration.Version); err != nil {
				return fmt.Errorf("update migration service version: %w", err)
			}
		}

		if err := writeMigrationServiceLog(ctx, tx, migration.Log); err != nil {
			return fmt.Errorf("write migration service log: %w", err)
		}

		return nil
	})
	if err != nil {
		return result, err
	}

	return result, nil
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

ALTER TABLE public.migration_service_logs DROP CONSTRAINT IF EXISTS migration_service_logs_complex_uindex;
ALTER TABLE public.migration_service_logs
    ADD CONSTRAINT migration_service_logs_complex_uindex
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
		return pkgerrors.Wrapf(err, "query %s failed.", query)
	}

	return nil
}

// WriteMigrationServiceLog inserts row to migration_service_logs
func (r *Repository) WriteMigrationServiceLog(ctx context.Context, log migration_log.MigrationServicesLog) error {
	err := writeMigrationServiceLog(ctx, r.db, log)
	if err != nil {
		if isUndefinedTable(err) {
			if createErr := r.CreateMigrationTable(ctx); createErr != nil {
				return pkgerrors.Wrapf(createErr, "query %s failed", writeMigrationServiceLogQuery)
			}
			return r.WriteMigrationServiceLog(ctx, log)
		}
		return pkgerrors.Wrapf(err, "query %s failed, params: MigrationServiceName = %s, Priority = %d, "+
			"Version = %d, FileName = %s, SQL = %s, Hash = %s", writeMigrationServiceLogQuery, log.MigrationServiceName, log.Priority,
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

	if stderrors.Is(err, pgx.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", pkgerrors.Wrapf(err, "query %s failed, params: MigrationServiceName = %s, Priority = %d, "+
			"Version = %d, FileName = %s", query, log.MigrationServiceName, log.Priority,
			log.Version, log.FileName)
	}
	return hash, nil
}

type execer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func updateServiceVersion(ctx context.Context, db execer, name string, ver int) error {
	_, err := db.Exec(ctx, updateServiceVersionQuery, name, ver)
	return err
}

func writeMigrationServiceLog(ctx context.Context, db execer, log migration_log.MigrationServicesLog) error {
	_, err := db.Exec(ctx, writeMigrationServiceLogQuery, log.MigrationServiceName, log.Priority, log.Version, log.FileName, log.SQL, log.Hash)
	return err
}

func isUndefinedTable(err error) bool {
	var pgErr *pgconn.PgError
	return stderrors.As(err, &pgErr) && pgErr.Code == noTableCode
}
