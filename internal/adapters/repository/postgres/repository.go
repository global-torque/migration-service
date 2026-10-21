package postgres

import (
	"context"
	"fmt"
	"sort"

	"github.com/jackc/pgx/v5"
	"github.com/pkg/errors"
	"github.com/webdevelop-pro/go-common/configurator"
	"github.com/webdevelop-pro/go-common/db"
	"github.com/webdevelop-pro/go-common/logger"
	"github.com/webdevelop-pro/migration-service/internal/domain/migration_log"
)

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

const (
	trackingSchemaCode = "42P01"
	serviceLockQuery   = `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`
)

type ambiguousLegacyProgressError struct {
	service    string
	aggregate  int
	priorities []int
}

func (e *ambiguousLegacyProgressError) Error() string {
	return fmt.Sprintf(
		"legacy migration progress for service %s at aggregate version %d is ambiguous across priorities %v",
		e.service,
		e.aggregate,
		e.priorities,
	)
}

type priorityProgressConflictError struct {
	service         string
	priority        int
	explicit        int
	loggedHighWater int
}

type priorityProgressMissingError struct {
	service  string
	priority int
}

func (e *priorityProgressMissingError) Error() string {
	return fmt.Sprintf(
		"explicit migration progress for service %s priority %d is missing after initialization",
		e.service,
		e.priority,
	)
}

func (e *priorityProgressConflictError) Error() string {
	return fmt.Sprintf(
		"explicit migration progress for service %s priority %d is %d below logged high-water %d",
		e.service,
		e.priority,
		e.explicit,
		e.loggedHighWater,
	)
}

// AdvanceServiceVersion advances the explicit priority row and legacy
// aggregate in one transaction. Neither value can move backwards.
func (r *Repository) AdvanceServiceVersion(ctx context.Context, name string, priority, version int) error {
	err := r.withTrackingRetry(ctx, func() error {
		return r.advanceServiceVersionOnce(ctx, name, priority, version)
	})
	if err != nil {
		return errors.Wrapf(err, "advance service %s priority %d to version %d failed", name, priority, version)
	}
	return nil
}

func (r *Repository) advanceServiceVersionOnce(ctx context.Context, name string, priority, version int) error {
	return pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		if err := lockService(ctx, tx, name); err != nil {
			return err
		}
		if err := initializeServicePrioritiesTx(ctx, tx, name, []int{priority}); err != nil {
			return err
		}
		if _, err := tx.Exec(ctx, `INSERT INTO migration_service_priorities
			(migration_services_name, priority, version)
			VALUES ($1, $2, $3)
			ON CONFLICT (migration_services_name, priority) DO UPDATE
			SET version = GREATEST(migration_service_priorities.version, EXCLUDED.version)`,
			name, priority, version); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, `INSERT INTO migration_services (name, version)
			VALUES ($1, $2)
			ON CONFLICT (name) DO UPDATE
			SET version = GREATEST(migration_services.version, EXCLUDED.version)`, name, version)
		return err
	})
}

// GetServiceVersion returns the authoritative explicit high-water version for
// one service and priority. A direct caller initializes only that priority;
// Set and App initialize all known priorities before calling it.
func (r *Repository) GetServiceVersion(ctx context.Context, name string, priority int) (int, error) {
	var version int
	err := r.withTrackingRetry(ctx, func() error {
		var err error
		version, err = r.getServiceVersionOnce(ctx, name, priority)
		return err
	})
	if err != nil {
		return 0, errors.Wrapf(err, "get service %s priority %d failed", name, priority)
	}
	return version, nil
}

func (r *Repository) getServiceVersionOnce(ctx context.Context, name string, priority int) (int, error) {
	var version int
	err := pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		if err := lockService(ctx, tx, name); err != nil {
			return err
		}
		if err := initializeServicePrioritiesTx(ctx, tx, name, []int{priority}); err != nil {
			return err
		}
		return tx.QueryRow(ctx, `SELECT version FROM migration_service_priorities
			WHERE migration_services_name = $1 AND priority = $2`, name, priority).Scan(&version)
	})
	return version, err
}

// InitializeServicePriorities materializes legacy progress into explicit rows.
// Once any explicit row exists for a service, those rows are authoritative.
func (r *Repository) InitializeServicePriorities(ctx context.Context, name string, priorities []int) error {
	return r.withTrackingRetry(ctx, func() error {
		return pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
			if err := lockService(ctx, tx, name); err != nil {
				return err
			}
			return initializeServicePrioritiesTx(ctx, tx, name, priorities)
		})
	})
}

func (r *Repository) withTrackingRetry(ctx context.Context, operation func() error) error {
	err := operation()
	if sqlState(err) != trackingSchemaCode {
		return err
	}
	if err := r.CreateMigrationTable(ctx); err != nil {
		return err
	}
	return operation()
}

func lockService(ctx context.Context, tx pgx.Tx, name string) error {
	_, err := tx.Exec(ctx, serviceLockQuery, name)
	return err
}

func normalizePriorities(priorities []int) []int {
	unique := make(map[int]struct{}, len(priorities))
	for _, priority := range priorities {
		unique[priority] = struct{}{}
	}
	result := make([]int, 0, len(unique))
	for priority := range unique {
		result = append(result, priority)
	}
	sort.Ints(result)
	return result
}

func readLoggedVersionsTx(ctx context.Context, tx pgx.Tx, name string) (map[int]int, error) {
	loggedVersions := make(map[int]int)
	rows, err := tx.Query(ctx, `SELECT priority, MAX(version)
		FROM migration_service_logs
		WHERE migration_services_name = $1
		GROUP BY priority`, name)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var priority, version int
		if err := rows.Scan(&priority, &version); err != nil {
			rows.Close()
			return nil, err
		}
		loggedVersions[priority] = version
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return loggedVersions, nil
}

func readExplicitVersionsTx(ctx context.Context, tx pgx.Tx, name string) (map[int]int, error) {
	explicitVersions := make(map[int]int)
	rows, err := tx.Query(ctx, `SELECT priority, version
		FROM migration_service_priorities
		WHERE migration_services_name = $1`, name)
	if err != nil {
		return nil, err
	}
	for rows.Next() {
		var priority, version int
		if err := rows.Scan(&priority, &version); err != nil {
			rows.Close()
			return nil, err
		}
		explicitVersions[priority] = version
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	rows.Close()
	return explicitVersions, nil
}

func validateExplicitVersions(service string, explicitVersions, loggedVersions map[int]int) error {
	for priority, version := range explicitVersions {
		if loggedVersion, ok := loggedVersions[priority]; ok && version < loggedVersion {
			return &priorityProgressConflictError{
				service:         service,
				priority:        priority,
				explicit:        version,
				loggedHighWater: loggedVersion,
			}
		}
	}
	return nil
}

func verifyServicePriorityRowsTx(
	ctx context.Context,
	tx pgx.Tx,
	service string,
	priorities []int,
	loggedVersions map[int]int,
) error {
	versions, err := readExplicitVersionsTx(ctx, tx, service)
	if err != nil {
		return err
	}
	for _, priority := range priorities {
		version, ok := versions[priority]
		if !ok {
			return &priorityProgressMissingError{service: service, priority: priority}
		}
		if loggedVersion, ok := loggedVersions[priority]; ok && version < loggedVersion {
			return &priorityProgressConflictError{
				service:         service,
				priority:        priority,
				explicit:        version,
				loggedHighWater: loggedVersion,
			}
		}
	}
	return nil
}

func allServicePriorities(priorities []int, loggedVersions map[int]int) []int {
	all := make(map[int]struct{}, len(priorities)+len(loggedVersions))
	for _, priority := range priorities {
		all[priority] = struct{}{}
	}
	for priority := range loggedVersions {
		all[priority] = struct{}{}
	}
	result := make([]int, 0, len(all))
	for priority := range all {
		result = append(result, priority)
	}
	sort.Ints(result)
	return result
}

func validateLegacyAggregate(
	service string,
	aggregateExists bool,
	aggregateVersion int,
	priorities []int,
	loggedVersions map[int]int,
) error {
	if !aggregateExists || aggregateVersion <= 0 || len(priorities) <= 1 {
		return nil
	}
	if len(loggedVersions) == 1 {
		return nil
	}
	if len(loggedVersions) == 0 {
		return nil
	}

	minimumHighWater := 0
	first := true
	for _, version := range loggedVersions {
		if first || version < minimumHighWater {
			minimumHighWater = version
			first = false
		}
	}
	if aggregateVersion <= minimumHighWater {
		return nil
	}
	return &ambiguousLegacyProgressError{
		service:    service,
		aggregate:  aggregateVersion,
		priorities: priorities,
	}
}

func initializeServicePrioritiesTx(ctx context.Context, tx pgx.Tx, name string, priorities []int) error {
	priorities = normalizePriorities(priorities)
	if len(priorities) == 0 {
		return nil
	}

	loggedVersions, err := readLoggedVersionsTx(ctx, tx, name)
	if err != nil {
		return err
	}

	aggregateVersion := 0
	aggregateExists := true
	if err := tx.QueryRow(ctx, `SELECT version FROM migration_services WHERE name = $1`, name).Scan(&aggregateVersion); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			aggregateExists = false
		} else {
			return err
		}
	}

	explicitVersions, err := readExplicitVersionsTx(ctx, tx, name)
	if err != nil {
		return err
	}

	if len(explicitVersions) > 0 {
		if err := validateExplicitVersions(name, explicitVersions, loggedVersions); err != nil {
			return err
		}
		for _, priority := range priorities {
			version := 0
			if loggedVersion, ok := loggedVersions[priority]; ok {
				version = loggedVersion
			}
			if _, err := tx.Exec(ctx, `INSERT INTO migration_service_priorities
				(migration_services_name, priority, version)
				VALUES ($1, $2, $3)
				ON CONFLICT (migration_services_name, priority) DO NOTHING`, name, priority, version); err != nil {
				return err
			}
		}
		return verifyServicePriorityRowsTx(ctx, tx, name, priorities, loggedVersions)
	}

	seedPriorities := allServicePriorities(priorities, loggedVersions)
	if err := validateLegacyAggregate(name, aggregateExists, aggregateVersion, seedPriorities, loggedVersions); err != nil {
		return err
	}

	for _, priority := range seedPriorities {
		version, ok := loggedVersions[priority]
		if ok {
			if len(loggedVersions) == 1 && aggregateExists && aggregateVersion > version {
				version = aggregateVersion
			}
		} else {
			// A service with logs starts every new priority at zero. Only a
			// service with no logs can use the legacy aggregate fallback.
			if len(loggedVersions) > 0 || !aggregateExists {
				version = 0
			} else if priority == seedPriorities[0] {
				version = aggregateVersion
			} else {
				version = 0
			}
		}
		if _, err := tx.Exec(ctx, `INSERT INTO migration_service_priorities
			(migration_services_name, priority, version)
			VALUES ($1, $2, $3)
			ON CONFLICT (migration_services_name, priority) DO NOTHING`, name, priority, version); err != nil {
			return err
		}
	}
	return verifyServicePriorityRowsTx(ctx, tx, name, seedPriorities, loggedVersions)
}

// ResolveServiceVersions reads priority progress without creating tables or
// rows. Relation discovery keeps this final-SQL path usable during any
// partially bootstrapped tracking-schema state.
func (r *Repository) ResolveServiceVersions(ctx context.Context, name string, priorities []int) (map[int]int, error) {
	normalized := normalizePriorities(priorities)
	result := make(map[int]int, len(normalized))
	err := pgx.BeginTxFunc(ctx, r.db, pgx.TxOptions{
		IsoLevel:   pgx.RepeatableRead,
		AccessMode: pgx.ReadOnly,
	}, func(tx pgx.Tx) error {
		var prioritiesExists, logsExists, servicesExists bool
		if err := tx.QueryRow(ctx, `SELECT
			to_regclass('public.migration_service_priorities') IS NOT NULL,
			to_regclass('public.migration_service_logs') IS NOT NULL,
			to_regclass('public.migration_services') IS NOT NULL`).Scan(
			&prioritiesExists,
			&logsExists,
			&servicesExists,
		); err != nil {
			return err
		}

		validRelationState := (!prioritiesExists && !logsExists && !servicesExists) ||
			(!prioritiesExists && logsExists && servicesExists) ||
			(prioritiesExists && logsExists && servicesExists)
		if !validRelationState {
			return fmt.Errorf(
				"invalid migration tracking relation state: priorities=%t logs=%t services=%t",
				prioritiesExists,
				logsExists,
				servicesExists,
			)
		}

		if !logsExists && !servicesExists {
			for _, priority := range normalized {
				result[priority] = 0
			}
			return nil
		}

		logged := make(map[int]int)
		if logsExists {
			var err error
			logged, err = readLoggedVersionsTx(ctx, tx, name)
			if err != nil {
				return err
			}
		}

		legacyVersion := 0
		aggregateExists := false
		if servicesExists {
			if err := tx.QueryRow(ctx, `SELECT version FROM migration_services WHERE name = $1`, name).Scan(&legacyVersion); err != nil {
				if !errors.Is(err, pgx.ErrNoRows) {
					return err
				}
			} else {
				aggregateExists = true
			}
		}

		if prioritiesExists {
			explicit, err := readExplicitVersionsTx(ctx, tx, name)
			if err != nil {
				return err
			}
			if len(explicit) > 0 {
				if err := validateExplicitVersions(name, explicit, logged); err != nil {
					return err
				}
				for _, priority := range normalized {
					if version, ok := explicit[priority]; ok {
						result[priority] = version
					} else {
						result[priority] = logged[priority]
					}
				}
				return nil
			}
		}

		seedPriorities := allServicePriorities(normalized, logged)
		if err := validateLegacyAggregate(name, aggregateExists, legacyVersion, seedPriorities, logged); err != nil {
			return err
		}
		for _, priority := range normalized {
			if version, ok := logged[priority]; ok {
				if len(logged) == 1 && aggregateExists && legacyVersion > version {
					version = legacyVersion
				}
				result[priority] = version
			} else if len(logged) > 0 || !aggregateExists {
				result[priority] = 0
			} else if priority == normalized[0] {
				result[priority] = legacyVersion
			} else {
				result[priority] = 0
			}
		}
		return nil
	})
	if err != nil {
		return nil, errors.Wrapf(err, "resolve service %s priorities failed", name)
	}
	return result, nil
}

type sqlStateError interface {
	SQLState() string
}

func sqlState(err error) string {
	var stateErr sqlStateError
	if errors.As(err, &stateErr) {
		return stateErr.SQLState()
	}
	return ""
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

CREATE TABLE IF NOT EXISTS migration_service_priorities
(
    migration_services_name character varying(255) NOT NULL,
    priority                integer                NOT NULL,
    version                 integer                NOT NULL DEFAULT 0,
    created_at              timestamptz            NOT NULL DEFAULT now(),
    updated_at              timestamptz            NOT NULL DEFAULT now(),
    PRIMARY KEY (migration_services_name, priority)
);

CREATE OR REPLACE TRIGGER migration_service_priorities_updated_at_timestamp
    BEFORE UPDATE
    ON migration_service_priorities
    FOR EACH ROW
EXECUTE PROCEDURE update_at_set_timestamp();

CREATE INDEX IF NOT EXISTS migration_service_priorities_service_index
    ON migration_service_priorities (migration_services_name);
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

const fakeMigrationLogInsertQuery = `INSERT INTO migration_service_logs
	(migration_services_name, priority, version, file_name, "sql", hash)
	VALUES ($1, $2, $3, $4, $5, $6)
	ON CONFLICT (migration_services_name, priority, version, file_name) DO NOTHING`

const fakeMigrationLogSelectQuery = `SELECT "sql", hash
	FROM migration_service_logs
	WHERE migration_services_name = $1
	  AND priority = $2
	  AND version = $3
	  AND file_name = $4`

const fakeMigrationAggregateQuery = `INSERT INTO migration_services (name, version)
	VALUES ($1, $2)
	ON CONFLICT (name) DO UPDATE
	SET version = GREATEST(migration_services.version, EXCLUDED.version)`

type fakeMigrationError struct {
	service  string
	priority int
	version  int
	fileName string
	category string
	cause    error
}

func (e *fakeMigrationError) Error() string {
	if e.fileName == "" {
		return fmt.Sprintf("fake migration service %s: %s", e.service, e.category)
	}
	return fmt.Sprintf(
		"fake migration service %s priority %d version %d file %s: %s",
		e.service,
		e.priority,
		e.version,
		e.fileName,
		e.category,
	)
}

func (e *fakeMigrationError) Unwrap() error {
	return e.cause
}

func newFakeMigrationError(service string, log migration_log.MigrationServicesLog, category string, cause error) error {
	return &fakeMigrationError{
		service:  service,
		priority: log.Priority,
		version:  log.Version,
		fileName: log.FileName,
		category: category,
		cause:    cause,
	}
}

// FakeMigrationService records a complete service fake batch atomically.
func (r *Repository) FakeMigrationService(
	ctx context.Context,
	serviceName string,
	aggregateVersion int,
	logs []migration_log.MigrationServicesLog,
) error {
	if serviceName == "" {
		return &fakeMigrationError{service: serviceName, category: "invalid batch"}
	}
	if len(logs) == 0 {
		return &fakeMigrationError{service: serviceName, category: "invalid batch"}
	}

	maxVersion := -1
	for _, log := range logs {
		if log.MigrationServiceName != serviceName || log.Version < 0 {
			return newFakeMigrationError(serviceName, log, "invalid batch", nil)
		}
		if log.Version > maxVersion {
			maxVersion = log.Version
		}
	}
	if aggregateVersion != maxVersion {
		return &fakeMigrationError{service: serviceName, category: "invalid batch"}
	}

	err := r.withTrackingRetry(ctx, func() error {
		return r.fakeMigrationServiceOnce(ctx, serviceName, aggregateVersion, logs)
	})
	if err == nil {
		return nil
	}

	var fakeErr *fakeMigrationError
	if errors.As(err, &fakeErr) {
		return err
	}
	return &fakeMigrationError{service: serviceName, category: "transaction commit failure", cause: err}
}

func (r *Repository) fakeMigrationServiceOnce(
	ctx context.Context,
	serviceName string,
	aggregateVersion int,
	logs []migration_log.MigrationServicesLog,
) error {
	priorityVersions := make(map[int]int)
	priorities := make([]int, 0)
	for _, log := range logs {
		if _, exists := priorityVersions[log.Priority]; !exists {
			priorities = append(priorities, log.Priority)
		}
		if log.Version > priorityVersions[log.Priority] {
			priorityVersions[log.Priority] = log.Version
		}
	}
	priorities = normalizePriorities(priorities)

	return pgx.BeginFunc(ctx, r.db, func(tx pgx.Tx) error {
		if err := lockService(ctx, tx, serviceName); err != nil {
			return &fakeMigrationError{service: serviceName, category: "service lock failure", cause: err}
		}
		if err := initializeServicePrioritiesTx(ctx, tx, serviceName, priorities); err != nil {
			return &fakeMigrationError{service: serviceName, category: "priority initialization failure", cause: err}
		}

		for _, log := range logs {
			if _, err := tx.Exec(ctx, fakeMigrationLogInsertQuery,
				log.MigrationServiceName,
				log.Priority,
				log.Version,
				log.FileName,
				log.SQL,
				log.Hash,
			); err != nil {
				return newFakeMigrationError(serviceName, log, "log insert failure", err)
			}

			var storedSQL, storedHash string
			if err := tx.QueryRow(ctx, fakeMigrationLogSelectQuery,
				log.MigrationServiceName,
				log.Priority,
				log.Version,
				log.FileName,
			).Scan(&storedSQL, &storedHash); err != nil {
				return newFakeMigrationError(serviceName, log, "log select failure", err)
			}
			if storedSQL != log.SQL || storedHash != log.Hash {
				category := "canonical log mismatch"
				if storedSQL == log.SQL {
					category = "canonical hash mismatch"
				} else if storedHash == log.Hash {
					category = "canonical SQL mismatch"
				}
				return newFakeMigrationError(serviceName, log, category, nil)
			}
		}

		for _, priority := range priorities {
			if _, err := tx.Exec(ctx, `INSERT INTO migration_service_priorities
				(migration_services_name, priority, version)
				VALUES ($1, $2, $3)
				ON CONFLICT (migration_services_name, priority) DO UPDATE
				SET version = GREATEST(migration_service_priorities.version, EXCLUDED.version)`,
				serviceName, priority, priorityVersions[priority]); err != nil {
				return &fakeMigrationError{service: serviceName, category: "priority update failure", cause: err}
			}
		}

		if _, err := tx.Exec(ctx, fakeMigrationAggregateQuery, serviceName, aggregateVersion); err != nil {
			return &fakeMigrationError{service: serviceName, category: "aggregate update failure", cause: err}
		}
		return nil
	})
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
