package postgres

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	commondb "github.com/webdevelop-pro/go-common/db"
	commonlogger "github.com/webdevelop-pro/go-common/logger"
	"github.com/webdevelop-pro/migration-service/internal/domain/migration_log"
)

var integrationDatabaseSequence uint64

func newIntegrationRepository(t *testing.T) (*Repository, context.Context) {
	t.Helper()
	if os.Getenv("MIGRATION_RUNNER_POSTGRES_TEST") != "1" {
		t.Skip("set MIGRATION_RUNNER_POSTGRES_TEST=1 for disposable PostgreSQL tests")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	adminDatabase := os.Getenv("DB_DATABASE")
	if adminDatabase == "" {
		cancel()
		t.Fatalf("DB_DATABASE must name the configured PostgreSQL admin connection database")
	}

	adminPool := openIntegrationPool(t, ctx, adminDatabase, "admin")
	databaseName := fmt.Sprintf(
		"migration_runner_test_%d_%d",
		os.Getpid(),
		atomic.AddUint64(&integrationDatabaseSequence, 1),
	)
	if _, err := adminPool.Exec(ctx, "CREATE DATABASE "+pgx.Identifier{databaseName}.Sanitize()); err != nil {
		adminPool.Close()
		cancel()
		t.Fatalf("create disposable integration database %s: %v", databaseName, err)
	}

	var dedicatedPool *pgxpool.Pool
	t.Cleanup(func() {
		if dedicatedPool != nil {
			dedicatedPool.Close()
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if _, err := adminPool.Exec(cleanupCtx, "DROP DATABASE "+pgx.Identifier{databaseName}.Sanitize()); err != nil {
			t.Errorf("drop disposable integration database %s: %v", databaseName, err)
		}
		adminPool.Close()
		cancel()
	})
	dedicatedPool = openIntegrationPool(t, ctx, databaseName, databaseName)
	repo := &Repository{db: commondb.NewDB(
		dedicatedPool,
		commonlogger.NewComponentLogger("migration-test-db", nil),
	)}
	return repo, ctx
}

func openIntegrationPool(t *testing.T, ctx context.Context, databaseName, applicationSuffix string) *pgxpool.Pool {
	t.Helper()
	if os.Getenv("DB_TYPE") != "postgres" {
		t.Fatalf("DB_TYPE must be postgres for disposable PostgreSQL integration tests")
	}

	sslMode := strings.ToLower(os.Getenv("DB_SSL_MODE"))
	switch sslMode {
	case "false", "disable", "disabled":
		sslMode = "disable"
	case "true":
		sslMode = "require"
	case "allow", "prefer", "require", "verify-ca", "verify-full":
	default:
		t.Fatalf("unsupported DB_SSL_MODE %q for disposable PostgreSQL integration tests", sslMode)
	}

	query := url.Values{}
	query.Set("application_name", fmt.Sprintf("%s_%s", os.Getenv("DB_APP_NAME"), applicationSuffix))
	query.Set("sslmode", sslMode)
	connectionURL := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(os.Getenv("DB_USER"), os.Getenv("DB_PASSWORD")),
		Host:     net.JoinHostPort(os.Getenv("DB_HOST"), os.Getenv("DB_PORT")),
		Path:     "/" + databaseName,
		RawQuery: query.Encode(),
	}
	config, err := pgxpool.ParseConfig(connectionURL.String())
	if err != nil {
		t.Fatalf("parse PostgreSQL integration connection: %v", err)
	}
	config.ConnConfig.Tracer = nil
	config.MinConns = 1
	config.MaxConns = 5
	if value := os.Getenv("DB_MIN_CONNECTIONS"); value != "" {
		minConnections, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			t.Fatalf("parse DB_MIN_CONNECTIONS: %v", err)
		}
		config.MinConns = int32(minConnections)
	}
	if value := os.Getenv("DB_MAX_CONNECTIONS"); value != "" {
		maxConnections, err := strconv.ParseInt(value, 10, 32)
		if err != nil {
			t.Fatalf("parse DB_MAX_CONNECTIONS: %v", err)
		}
		config.MaxConns = int32(maxConnections)
	}

	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		t.Fatalf("open PostgreSQL integration database %s: %v", databaseName, err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Fatalf("ping PostgreSQL integration database %s: %v", databaseName, err)
	}
	return pool
}

func resetTrackingSchema(t *testing.T, repo *Repository, ctx context.Context) {
	t.Helper()
	_, err := repo.db.Exec(ctx, `DROP TABLE IF EXISTS migration_service_priorities,
		migration_service_logs, migration_services CASCADE;
		DROP FUNCTION IF EXISTS update_at_set_timestamp() CASCADE`)
	if err != nil {
		t.Fatalf("reset tracking schema: %v", err)
	}
}

func createTrackingSchema(t *testing.T, repo *Repository, ctx context.Context) {
	t.Helper()
	if err := repo.CreateMigrationTable(ctx); err != nil {
		t.Fatalf("create tracking schema: %v", err)
	}
}

func priorityRows(t *testing.T, repo *Repository, ctx context.Context, service string) map[int]int {
	t.Helper()
	rows, err := repo.db.Query(ctx, `SELECT priority, version
		FROM migration_service_priorities
		WHERE migration_services_name = $1
		ORDER BY priority`, service)
	if err != nil {
		t.Fatalf("query priority rows: %v", err)
	}
	defer rows.Close()

	result := make(map[int]int)
	for rows.Next() {
		var priority, version int
		if err := rows.Scan(&priority, &version); err != nil {
			t.Fatalf("scan priority row: %v", err)
		}
		result[priority] = version
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate priority rows: %v", err)
	}
	return result
}

func migrationServiceLogIdentityConstraintNames(t *testing.T, repo *Repository, ctx context.Context) []string {
	t.Helper()
	rows, err := repo.db.Query(ctx, `SELECT constraint_row.conname
		FROM pg_constraint AS constraint_row
		JOIN LATERAL unnest(constraint_row.conkey) WITH ORDINALITY AS key_column(attnum, ordinal) ON true
		JOIN pg_attribute AS attribute
		  ON attribute.attrelid = constraint_row.conrelid
		 AND attribute.attnum = key_column.attnum
		WHERE constraint_row.conrelid = 'public.migration_service_logs'::regclass
		  AND constraint_row.contype IN ('p', 'u')
		GROUP BY constraint_row.oid, constraint_row.conname
		HAVING array_agg(attribute.attname::text ORDER BY key_column.ordinal) =
			ARRAY['migration_services_name', 'priority', 'version', 'file_name']::text[]
		ORDER BY constraint_row.conname`)
	if err != nil {
		t.Fatalf("query migration log identity constraints: %v", err)
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatalf("scan migration log identity constraint: %v", err)
		}
		names = append(names, name)
	}
	if err := rows.Err(); err != nil {
		t.Fatalf("iterate migration log identity constraints: %v", err)
	}
	return names
}

func TestIntegrationMigrationServiceLogConstraintRepairAndUpsert(t *testing.T) {
	repo, ctx := newIntegrationRepository(t)
	createTrackingSchema(t, repo, ctx)

	const service = "constraint_repair"
	legacy := migration_log.MigrationServicesLog{
		MigrationServiceName: service,
		Priority:             16,
		Version:              1,
		FileName:             "01.sql",
		SQL:                  "SELECT 'legacy'",
		Hash:                 "legacy-hash",
	}
	if err := repo.WriteMigrationServiceLog(ctx, legacy); err != nil {
		t.Fatalf("write legacy migration log: %v", err)
	}
	if _, err := repo.db.Exec(ctx, `ALTER TABLE migration_service_logs
		DROP CONSTRAINT migration_service_logs_pk;
		ALTER TABLE migration_service_logs
		ADD CONSTRAINT migration_service_logs_complex_uindex
		UNIQUE (migration_services_name, priority, version, file_name)`); err != nil {
		t.Fatalf("create legacy migration log constraint: %v", err)
	}
	if got, want := migrationServiceLogIdentityConstraintNames(t, repo, ctx), []string{"migration_service_logs_complex_uindex"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy migration log constraints = %v, want %v", got, want)
	}

	if err := repo.CreateMigrationTable(ctx); err != nil {
		t.Fatalf("repair migration log constraint: %v", err)
	}
	if got, want := migrationServiceLogIdentityConstraintNames(t, repo, ctx), []string{"migration_service_logs_pk"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("repaired migration log constraints = %v, want %v", got, want)
	}

	var rowCount int
	if err := repo.db.QueryRow(ctx, `SELECT count(*) FROM migration_service_logs
		WHERE migration_services_name = $1 AND priority = $2 AND version = $3 AND file_name = $4`,
		legacy.MigrationServiceName, legacy.Priority, legacy.Version, legacy.FileName).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("repaired migration log count = %d, want 1", rowCount)
	}

	if err := repo.CreateMigrationTable(ctx); err != nil {
		t.Fatalf("repeat migration log bootstrap: %v", err)
	}
	if got, want := migrationServiceLogIdentityConstraintNames(t, repo, ctx), []string{"migration_service_logs_pk"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("repeated migration log constraints = %v, want %v", got, want)
	}
	if err := repo.db.QueryRow(ctx, `SELECT count(*) FROM migration_service_logs`).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 1 {
		t.Fatalf("repeated bootstrap migration log count = %d, want 1", rowCount)
	}

	updated := legacy
	updated.SQL = "SELECT 'updated'"
	updated.Hash = "updated-hash"
	if err := repo.WriteMigrationServiceLog(ctx, updated); err != nil {
		t.Fatalf("upsert migration log under canonical constraint: %v", err)
	}
	inserted := migration_log.MigrationServicesLog{
		MigrationServiceName: service,
		Priority:             16,
		Version:              2,
		FileName:             "02.sql",
		SQL:                  "SELECT 'inserted'",
		Hash:                 "inserted-hash",
	}
	if err := repo.WriteMigrationServiceLog(ctx, inserted); err != nil {
		t.Fatalf("insert migration log under canonical constraint: %v", err)
	}
	if err := repo.db.QueryRow(ctx, `SELECT count(*) FROM migration_service_logs`).Scan(&rowCount); err != nil {
		t.Fatal(err)
	}
	if rowCount != 2 {
		t.Fatalf("upsert migration log count = %d, want 2", rowCount)
	}
	var storedSQL, storedHash string
	if err := repo.db.QueryRow(ctx, `SELECT sql, hash FROM migration_service_logs
		WHERE migration_services_name = $1 AND priority = $2 AND version = $3 AND file_name = $4`,
		updated.MigrationServiceName, updated.Priority, updated.Version, updated.FileName).Scan(&storedSQL, &storedHash); err != nil {
		t.Fatal(err)
	}
	if storedSQL != updated.SQL || storedHash != updated.Hash {
		t.Fatalf("upserted migration log = (%q, %q), want (%q, %q)", storedSQL, storedHash, updated.SQL, updated.Hash)
	}
}

func TestIntegrationWriteMigrationServiceLogBootstrapFailureIsSafe(t *testing.T) {
	repo, ctx := newIntegrationRepository(t)
	if _, err := repo.db.Exec(ctx, `CREATE VIEW migration_services AS SELECT 1::integer AS version`); err != nil {
		t.Fatalf("create bootstrap failure relation: %v", err)
	}

	creationErr := repo.CreateMigrationTable(ctx)
	if creationErr == nil {
		t.Fatal("CreateMigrationTable unexpectedly succeeded with an incompatible relation")
	}
	var creationState sqlStateError
	if !errors.As(creationErr, &creationState) {
		t.Fatalf("CreateMigrationTable error has no SQL state: %v", creationErr)
	}

	const secret = "write-log-bootstrap-secret"
	log := migration_log.MigrationServicesLog{
		MigrationServiceName: "bootstrap_failure",
		Priority:             16,
		Version:              1,
		FileName:             "01.sql",
		SQL:                  secret,
		Hash:                 secret,
	}
	var recovered interface{}
	var writeErr error
	func() {
		defer func() { recovered = recover() }()
		writeErr = repo.WriteMigrationServiceLog(ctx, log)
	}()
	if recovered != nil {
		t.Fatalf("WriteMigrationServiceLog panicked: %v", recovered)
	}
	if writeErr == nil {
		t.Fatal("WriteMigrationServiceLog unexpectedly succeeded")
	}
	var writeState sqlStateError
	if !errors.As(writeErr, &writeState) {
		t.Fatalf("WriteMigrationServiceLog error has no creation SQL state: %v", writeErr)
	}
	if writeState.SQLState() != creationState.SQLState() {
		t.Fatalf("WriteMigrationServiceLog SQL state = %q, want creation state %q", writeState.SQLState(), creationState.SQLState())
	}
	if strings.Contains(writeErr.Error(), secret) {
		t.Fatalf("WriteMigrationServiceLog error exposed secret: %v", writeErr)
	}
}

func TestIntegrationPriorityInitializationAndReadOnlyResolution(t *testing.T) {
	repo, ctx := newIntegrationRepository(t)
	createTrackingSchema(t, repo, ctx)

	const service = "user_user_invitations"
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_services(name, version) VALUES ($1, 3)`, service); err != nil {
		t.Fatal(err)
	}
	for _, log := range []migration_log.MigrationServicesLog{
		{MigrationServiceName: service, Priority: 16, Version: 1, FileName: "01.sql", SQL: "SELECT 1", Hash: "h1"},
		{MigrationServiceName: service, Priority: 16, Version: 3, FileName: "03.sql", SQL: "SELECT 3", Hash: "h3"},
		{MigrationServiceName: service, Priority: 17, Version: 2, FileName: "02.sql", SQL: "SELECT 2", Hash: "h2"},
	} {
		if err := repo.WriteMigrationServiceLog(ctx, log); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := repo.db.Exec(ctx, `DROP TABLE migration_service_priorities`); err != nil {
		t.Fatal(err)
	}
	before, err := repo.ResolveServiceVersions(ctx, service, []int{16, 17})
	var ambiguous *ambiguousLegacyProgressError
	if !errors.As(err, &ambiguous) {
		t.Fatalf("resolve ambiguous legacy progress error = %v, want ambiguity error", err)
	}
	if before != nil {
		t.Fatalf("ambiguous resolution = %v, want no result", before)
	}
	var relationExists bool
	if err := repo.db.QueryRow(ctx, `SELECT to_regclass('public.migration_service_priorities') IS NOT NULL`).Scan(&relationExists); err != nil {
		t.Fatal(err)
	}
	if relationExists {
		t.Fatal("read-only resolver recreated priority relation")
	}

	if _, err := repo.db.Exec(ctx, `UPDATE migration_services SET version = 2 WHERE name = $1`, service); err != nil {
		t.Fatal(err)
	}
	before, err = repo.ResolveServiceVersions(ctx, service, []int{16, 17, 18})
	if err != nil {
		t.Fatalf("resolve compatible legacy progress: %v", err)
	}
	if want := map[int]int{16: 3, 17: 2, 18: 0}; !reflect.DeepEqual(before, want) {
		t.Fatalf("compatible resolution = %v, want %v", before, want)
	}

	if err := repo.InitializeServicePriorities(ctx, service, []int{16, 17, 18}); err != nil {
		t.Fatalf("initialize explicit priorities: %v", err)
	}
	if got, want := priorityRows(t, repo, ctx, service), map[int]int{16: 3, 17: 2, 18: 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("priority rows = %v, want %v", got, want)
	}
	after, err := repo.ResolveServiceVersions(ctx, service, []int{16, 17, 18})
	if err != nil {
		t.Fatalf("resolve after explicit initialization: %v", err)
	}
	if !reflect.DeepEqual(after, before) {
		t.Fatalf("resolution after initialization = %v, want %v", after, before)
	}

	for _, test := range []struct {
		name       string
		priorities []int
	}{
		{name: "read-only descending", priorities: []int{17, 16, 18}},
		{name: "read-only ascending", priorities: []int{16, 17, 18}},
		{name: "mutating descending", priorities: []int{17, 16, 18}},
		{name: "mutating ascending", priorities: []int{16, 17, 18}},
	} {
		t.Run(test.name, func(t *testing.T) {
			service := "legacy_aggregate_" + strings.ReplaceAll(test.name, " ", "_")
			if _, err := repo.db.Exec(ctx, `INSERT INTO migration_services(name, version) VALUES ($1, 14)`, service); err != nil {
				t.Fatal(err)
			}
			resolved, err := repo.ResolveServiceVersions(ctx, service, test.priorities)
			if err != nil {
				t.Fatalf("legacy aggregate resolution: %v", err)
			}
			if want := map[int]int{16: 14, 17: 0, 18: 0}; !reflect.DeepEqual(resolved, want) {
				t.Fatalf("legacy aggregate resolution = %v, want %v", resolved, want)
			}
			if strings.HasPrefix(test.name, "mutating") {
				if err := repo.InitializeServicePriorities(ctx, service, test.priorities); err != nil {
					t.Fatalf("legacy aggregate initialization: %v", err)
				}
				if got, want := priorityRows(t, repo, ctx, service), map[int]int{16: 14, 17: 0, 18: 0}; !reflect.DeepEqual(got, want) {
					t.Fatalf("legacy aggregate rows = %v, want %v", got, want)
				}
			}
		})
	}

	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_services(name, version)
		VALUES ('missing_priority', 2)`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_service_logs
		(migration_services_name, priority, version, file_name, sql, hash)
		VALUES ('missing_priority', 16, 3, '03.sql', 'SELECT 3', 'h3')`); err != nil {
		t.Fatal(err)
	}
	if err := repo.InitializeServicePriorities(ctx, "missing_priority", []int{16, 17}); err != nil {
		t.Fatalf("one-logged-priority initialization: %v", err)
	}
	if got, want := priorityRows(t, repo, ctx, "missing_priority"), map[int]int{16: 3, 17: 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("one-logged-priority rows = %v, want %v", got, want)
	}
	resolved, err := repo.ResolveServiceVersions(ctx, "missing_priority", []int{16, 17})
	if err != nil {
		t.Fatalf("one-logged-priority resolution: %v", err)
	}
	if want := map[int]int{16: 3, 17: 0}; !reflect.DeepEqual(resolved, want) {
		t.Fatalf("one-logged-priority resolution = %v, want %v", resolved, want)
	}
}

func TestIntegrationSingleLoggedPriorityUsesGreatestAggregate(t *testing.T) {
	repo, ctx := newIntegrationRepository(t)
	createTrackingSchema(t, repo, ctx)
	const service = "single_logged_priority"
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_services(name, version)
		VALUES ($1, 5)`, service); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_service_logs
		(migration_services_name, priority, version, file_name, sql, hash)
		VALUES ($1, 16, 3, '03.sql', 'SELECT 3', 'h3')`, service); err != nil {
		t.Fatal(err)
	}

	resolved, err := repo.ResolveServiceVersions(ctx, service, []int{16, 17})
	if err != nil {
		t.Fatalf("read-only single-priority resolution: %v", err)
	}
	if want := map[int]int{16: 5, 17: 0}; !reflect.DeepEqual(resolved, want) {
		t.Fatalf("read-only single-priority resolution = %v, want %v", resolved, want)
	}

	if err := repo.InitializeServicePriorities(ctx, service, []int{16, 17}); err != nil {
		t.Fatalf("mutating single-priority initialization: %v", err)
	}
	if got, want := priorityRows(t, repo, ctx, service), map[int]int{16: 5, 17: 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mutating single-priority rows = %v, want %v", got, want)
	}
}

func TestIntegrationPrioritySchemaShape(t *testing.T) {
	repo, ctx := newIntegrationRepository(t)
	createTrackingSchema(t, repo, ctx)

	var primaryKeyColumns []string
	rows, err := repo.db.Query(ctx, `SELECT attribute.attname
		FROM pg_constraint AS constraint_row
		JOIN LATERAL unnest(constraint_row.conkey) WITH ORDINALITY AS key_column(attnum, ordinal) ON true
		JOIN pg_attribute AS attribute
		  ON attribute.attrelid = constraint_row.conrelid
		 AND attribute.attnum = key_column.attnum
		WHERE constraint_row.conrelid = 'public.migration_service_priorities'::regclass
		  AND constraint_row.contype = 'p'
		ORDER BY key_column.ordinal`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			rows.Close()
			t.Fatal(err)
		}
		primaryKeyColumns = append(primaryKeyColumns, column)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if want := []string{"migration_services_name", "priority"}; !reflect.DeepEqual(primaryKeyColumns, want) {
		t.Fatalf("priority table primary key = %v, want %v", primaryKeyColumns, want)
	}

	var idColumnCount int
	if err := repo.db.QueryRow(ctx, `SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema = 'public'
		  AND table_name = 'migration_service_priorities'
		  AND column_name = 'id'`).Scan(&idColumnCount); err != nil {
		t.Fatal(err)
	}
	if idColumnCount != 0 {
		t.Fatalf("priority table has %d id columns, want none", idColumnCount)
	}

	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_service_priorities
		(migration_services_name, priority, version)
		VALUES ('schema_shape', 16, 0)`); err != nil {
		t.Fatal(err)
	}
	var beforeUpdated time.Time
	if err := repo.db.QueryRow(ctx, `SELECT updated_at FROM migration_service_priorities
		WHERE migration_services_name = 'schema_shape' AND priority = 16`).Scan(&beforeUpdated); err != nil {
		t.Fatal(err)
	}

	var afterUpdated time.Time
	for attempt := 0; attempt < 10; attempt++ {
		time.Sleep(10 * time.Millisecond)
		if _, err := repo.db.Exec(ctx, `UPDATE migration_service_priorities
			SET version = version + 1
			WHERE migration_services_name = 'schema_shape' AND priority = 16`); err != nil {
			t.Fatal(err)
		}
		if err := repo.db.QueryRow(ctx, `SELECT updated_at FROM migration_service_priorities
			WHERE migration_services_name = 'schema_shape' AND priority = 16`).Scan(&afterUpdated); err != nil {
			t.Fatal(err)
		}
		if afterUpdated.After(beforeUpdated) {
			break
		}
	}
	if !afterUpdated.After(beforeUpdated) {
		t.Fatalf("updated_at did not advance from %v to %v", beforeUpdated, afterUpdated)
	}
}

func TestIntegrationPriorityInitializationRereadsRowsAndRollsBack(t *testing.T) {
	repo, ctx := newIntegrationRepository(t)
	createTrackingSchema(t, repo, ctx)
	const service = "reread_rollback"
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_services(name, version) VALUES ($1, 3)`, service); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_service_logs
			(migration_services_name, priority, version, file_name, sql, hash)
			VALUES ($1, 16, 3, '03.sql', 'SELECT 3', 'h3')`, service); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_service_priorities
			(migration_services_name, priority, version)
			VALUES ($1, 16, 3)`, service); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(ctx, `CREATE OR REPLACE FUNCTION test_lower_priority_row()
		RETURNS trigger AS $$
		BEGIN
			IF NEW.migration_services_name = 'reread_rollback' AND NEW.priority = 16 THEN
				UPDATE migration_service_priorities
				SET version = 0
				WHERE migration_services_name = NEW.migration_services_name
				  AND priority = NEW.priority;
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(ctx, `CREATE TRIGGER test_lower_priority_row_before_insert
		BEFORE INSERT ON migration_service_priorities
		FOR EACH ROW EXECUTE FUNCTION test_lower_priority_row()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repo.db.Exec(context.Background(), `DROP TRIGGER IF EXISTS
			test_lower_priority_row_before_insert ON migration_service_priorities`)
		_, _ = repo.db.Exec(context.Background(), `DROP FUNCTION IF EXISTS test_lower_priority_row()`)
	})

	err := repo.InitializeServicePriorities(ctx, service, []int{17, 16})
	var conflict *priorityProgressConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("initialization after conflicting insert = %v, want conflict error", err)
	}
	if got, want := priorityRows(t, repo, ctx, service), map[int]int{16: 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rolled-back priority rows = %v, want preserved preexisting row %v", got, want)
	}
}

func TestIntegrationPriorityInitializationRereadsMissingRowsAndRollsBack(t *testing.T) {
	repo, ctx := newIntegrationRepository(t)
	createTrackingSchema(t, repo, ctx)
	const service = "reread_missing"
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_service_logs
		(migration_services_name, priority, version, file_name, sql, hash)
		VALUES ($1, 16, 3, '03.sql', 'SELECT 3', 'h3')`, service); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_service_priorities
		(migration_services_name, priority, version)
		VALUES ($1, 16, 3)`, service); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(ctx, `CREATE OR REPLACE FUNCTION test_delete_priority_row()
		RETURNS trigger AS $$
		BEGIN
			IF NEW.migration_services_name = 'reread_missing' AND NEW.priority = 17 THEN
				DELETE FROM migration_service_priorities
				WHERE migration_services_name = NEW.migration_services_name
				  AND priority = NEW.priority;
			END IF;
			RETURN NEW;
		END;
		$$ LANGUAGE plpgsql`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(ctx, `CREATE TRIGGER test_delete_priority_row_after_insert
		AFTER INSERT ON migration_service_priorities
		FOR EACH ROW EXECUTE FUNCTION test_delete_priority_row()`); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = repo.db.Exec(context.Background(), `DROP TRIGGER IF EXISTS
			test_delete_priority_row_after_insert ON migration_service_priorities`)
		_, _ = repo.db.Exec(context.Background(), `DROP FUNCTION IF EXISTS test_delete_priority_row()`)
	})

	err := repo.InitializeServicePriorities(ctx, service, []int{17, 16})
	var missing *priorityProgressMissingError
	if !errors.As(err, &missing) {
		t.Fatalf("initialization after missing insert = %v, want missing-row error", err)
	}
	if got, want := priorityRows(t, repo, ctx, service), map[int]int{16: 3}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rolled-back priority rows = %v, want preserved preexisting row %v", got, want)
	}
}

func TestIntegrationExplicitPriorityAuthorityValidationAndMissingSeeds(t *testing.T) {
	repo, ctx := newIntegrationRepository(t)
	createTrackingSchema(t, repo, ctx)
	const service = "explicit_service"
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_services(name, version) VALUES ($1, 3)`, service); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_service_logs
		(migration_services_name, priority, version, file_name, sql, hash)
		VALUES ($1, 16, 3, '03.sql', 'SELECT 3', 'h3'),
		       ($1, 17, 2, '02.sql', 'SELECT 2', 'h2')`, service); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_service_priorities
		(migration_services_name, priority, version) VALUES ($1, 16, 2)`, service); err != nil {
		t.Fatal(err)
	}

	err := repo.InitializeServicePriorities(ctx, service, []int{16, 17})
	var conflict *priorityProgressConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("explicit/log conflict = %v, want conflict error", err)
	}
	if got := priorityRows(t, repo, ctx, service); !reflect.DeepEqual(got, map[int]int{16: 2}) {
		t.Fatalf("conflicting initialization changed rows to %v", got)
	}

	if _, err := repo.db.Exec(ctx, `UPDATE migration_service_priorities
		SET version = 3 WHERE migration_services_name = $1 AND priority = 16`, service); err != nil {
		t.Fatal(err)
	}
	if err := repo.InitializeServicePriorities(ctx, service, []int{16, 17, 18}); err != nil {
		t.Fatalf("explicit initialization with missing priorities: %v", err)
	}
	if got, want := priorityRows(t, repo, ctx, service), map[int]int{16: 3, 17: 2, 18: 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("explicit rows = %v, want %v", got, want)
	}

	if _, err := repo.db.Exec(ctx, `UPDATE migration_service_priorities
		SET version = 1 WHERE migration_services_name = $1 AND priority = 16`, service); err != nil {
		t.Fatal(err)
	}
	resolved, err := repo.ResolveServiceVersions(ctx, service, []int{16, 17, 18})
	if !errors.As(err, &conflict) {
		t.Fatalf("read-only explicit/log conflict = %v, want conflict error", err)
	}
	if resolved != nil {
		t.Fatalf("conflicting read-only resolution = %v, want no result", resolved)
	}
	if got, want := priorityRows(t, repo, ctx, service), map[int]int{16: 1, 17: 2, 18: 0}; !reflect.DeepEqual(got, want) {
		t.Fatalf("read-only validation changed rows to %v, want %v", got, want)
	}
}

func TestIntegrationExplicitRowsSurviveLegacyRelationReset(t *testing.T) {
	repo, ctx := newIntegrationRepository(t)
	createTrackingSchema(t, repo, ctx)
	const service = "preserved_service"
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_service_priorities
		(migration_services_name, priority, version) VALUES ($1, 16, 14)`, service); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(ctx, `DROP TABLE migration_service_logs, migration_services`); err != nil {
		t.Fatal(err)
	}

	version, err := repo.GetServiceVersion(ctx, service, 16)
	if err != nil {
		t.Fatalf("get after legacy relation reset: %v", err)
	}
	if version != 14 {
		t.Fatalf("preserved explicit version = %d, want 14", version)
	}
	if got, want := priorityRows(t, repo, ctx, service), map[int]int{16: 14}; !reflect.DeepEqual(got, want) {
		t.Fatalf("legacy reset changed explicit rows to %v, want %v", got, want)
	}

	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_services(name, version) VALUES ($1, 14)`, service); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_service_logs
		(migration_services_name, priority, version, file_name, sql, hash)
		VALUES ($1, 16, 15, '15.sql', 'SELECT 15', 'h15')`, service); err != nil {
		t.Fatal(err)
	}
	err = repo.InitializeServicePriorities(ctx, service, []int{16, 17})
	var conflict *priorityProgressConflictError
	if !errors.As(err, &conflict) {
		t.Fatalf("preserved explicit/log conflict = %v, want conflict error", err)
	}
	if got, want := priorityRows(t, repo, ctx, service), map[int]int{16: 14}; !reflect.DeepEqual(got, want) {
		t.Fatalf("failed initialization changed explicit rows to %v, want %v", got, want)
	}
}

func TestIntegrationReadOnlyResolverRelationStates(t *testing.T) {
	repo, ctx := newIntegrationRepository(t)
	tests := []struct {
		name                 string
		services, logs, rows bool
		wantError            bool
	}{
		{name: "all absent"},
		{name: "old two", services: true, logs: true},
		{name: "all three", services: true, logs: true, rows: true},
		{name: "services only", services: true, wantError: true},
		{name: "logs only", logs: true, wantError: true},
		{name: "priorities only", rows: true, wantError: true},
		{name: "services and priorities", services: true, rows: true, wantError: true},
		{name: "logs and priorities", logs: true, rows: true, wantError: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resetTrackingSchema(t, repo, ctx)
			if test.services || test.logs || test.rows {
				createTrackingSchema(t, repo, ctx)
			}
			if !test.services {
				if _, err := repo.db.Exec(ctx, `DROP TABLE IF EXISTS migration_services`); err != nil {
					t.Fatal(err)
				}
			}
			if !test.logs {
				if _, err := repo.db.Exec(ctx, `DROP TABLE IF EXISTS migration_service_logs`); err != nil {
					t.Fatal(err)
				}
			}
			if !test.rows {
				if _, err := repo.db.Exec(ctx, `DROP TABLE IF EXISTS migration_service_priorities`); err != nil {
					t.Fatal(err)
				}
			}

			resolved, err := repo.ResolveServiceVersions(ctx, "relation_state", []int{16, 17})
			if test.wantError {
				if err == nil || resolved != nil {
					t.Fatalf("partial relation resolution = (%v, %v), want error and no result", resolved, err)
				}
			} else {
				if err != nil {
					t.Fatalf("valid relation resolution: %v", err)
				}
				if want := map[int]int{16: 0, 17: 0}; !reflect.DeepEqual(resolved, want) {
					t.Fatalf("valid relation resolution = %v, want %v", resolved, want)
				}
			}

			var prioritiesExists, logsExists, servicesExists bool
			if err := repo.db.QueryRow(ctx, `SELECT
				to_regclass('public.migration_service_priorities') IS NOT NULL,
				to_regclass('public.migration_service_logs') IS NOT NULL,
				to_regclass('public.migration_services') IS NOT NULL`).Scan(
				&prioritiesExists, &logsExists, &servicesExists,
			); err != nil {
				t.Fatal(err)
			}
			if prioritiesExists != test.rows || logsExists != test.logs || servicesExists != test.services {
				t.Fatalf("resolver mutated relation state to priorities=%t logs=%t services=%t", prioritiesExists, logsExists, servicesExists)
			}
		})
	}
}

func TestIntegrationFakeBatchIsAtomicAndMonotonic(t *testing.T) {
	repo, ctx := newIntegrationRepository(t)
	createTrackingSchema(t, repo, ctx)
	const service = "fake_service"
	logs := []migration_log.MigrationServicesLog{
		{MigrationServiceName: service, Priority: 16, Version: 1, FileName: "01.sql", SQL: "SELECT 1", Hash: "h1"},
		{MigrationServiceName: service, Priority: 17, Version: 2, FileName: "02.sql", SQL: "SELECT 2", Hash: "h2"},
	}
	if err := repo.FakeMigrationService(ctx, service, 2, logs); err != nil {
		t.Fatalf("fake batch: %v", err)
	}
	if got, want := priorityRows(t, repo, ctx, service), map[int]int{16: 1, 17: 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fake priority rows = %v, want %v", got, want)
	}
	var aggregate int
	if err := repo.db.QueryRow(ctx, `SELECT version FROM migration_services WHERE name = $1`, service).Scan(&aggregate); err != nil {
		t.Fatal(err)
	}
	if aggregate != 2 {
		t.Fatalf("fake aggregate = %d, want 2", aggregate)
	}
	var beforeUpdated time.Time
	if err := repo.db.QueryRow(ctx, `SELECT updated_at FROM migration_service_logs
		WHERE migration_services_name = $1 AND priority = 16 AND version = 1`, service).Scan(&beforeUpdated); err != nil {
		t.Fatal(err)
	}
	if err := repo.FakeMigrationService(ctx, service, 2, logs); err != nil {
		t.Fatalf("identical fake batch: %v", err)
	}
	var afterUpdated time.Time
	if err := repo.db.QueryRow(ctx, `SELECT updated_at FROM migration_service_logs
		WHERE migration_services_name = $1 AND priority = 16 AND version = 1`, service).Scan(&afterUpdated); err != nil {
		t.Fatal(err)
	}
	if !beforeUpdated.Equal(afterUpdated) {
		t.Fatalf("identical fake changed updated_at from %v to %v", beforeUpdated, afterUpdated)
	}

	lowerFake := []migration_log.MigrationServicesLog{
		{MigrationServiceName: service, Priority: 17, Version: 1, FileName: "01.sql", SQL: "SELECT lower", Hash: "hlower"},
	}
	if err := repo.FakeMigrationService(ctx, service, 1, lowerFake); err != nil {
		t.Fatalf("successful lower fake batch: %v", err)
	}
	if got, want := priorityRows(t, repo, ctx, service), map[int]int{16: 1, 17: 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lower fake changed priority rows to %v, want %v", got, want)
	}
	if err := repo.db.QueryRow(ctx, `SELECT version FROM migration_services WHERE name = $1`, service).Scan(&aggregate); err != nil {
		t.Fatal(err)
	}
	if aggregate != 2 {
		t.Fatalf("lower fake changed aggregate to %d, want 2", aggregate)
	}

	beforePriorities := priorityRows(t, repo, ctx, service)
	mismatch := []migration_log.MigrationServicesLog{
		{MigrationServiceName: service, Priority: 18, Version: 1, FileName: "new.sql", SQL: "SELECT new", Hash: "new"},
		{MigrationServiceName: service, Priority: 16, Version: 1, FileName: "01.sql", SQL: "SELECT 1", Hash: "wrong"},
	}
	if err := repo.FakeMigrationService(ctx, service, 1, mismatch); err == nil {
		t.Fatal("mismatched fake batch succeeded")
	}
	if got := priorityRows(t, repo, ctx, service); !reflect.DeepEqual(got, beforePriorities) {
		t.Fatalf("mismatched fake changed priority rows to %v, want %v", got, beforePriorities)
	}
	var count int
	if err := repo.db.QueryRow(ctx, `SELECT count(*) FROM migration_service_logs
		WHERE migration_services_name = $1 AND priority = 18`, service).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("rolled-back fake log count = %d, want 0", count)
	}
	var storedHash string
	if err := repo.db.QueryRow(ctx, `SELECT hash FROM migration_service_logs
		WHERE migration_services_name = $1 AND priority = 16 AND version = 1 AND file_name = '01.sql'`, service).Scan(&storedHash); err != nil {
		t.Fatal(err)
	}
	if storedHash != "h1" {
		t.Fatalf("mismatched fake changed existing hash to %q", storedHash)
	}
	if err := repo.db.QueryRow(ctx, `SELECT version FROM migration_services WHERE name = $1`, service).Scan(&aggregate); err != nil {
		t.Fatal(err)
	}
	if aggregate != 2 {
		t.Fatalf("mismatched fake changed aggregate to %d", aggregate)
	}
}

func TestIntegrationPriorityInitializationSerializesClaims(t *testing.T) {
	repo, ctx := newIntegrationRepository(t)
	createTrackingSchema(t, repo, ctx)

	for round := 0; round < 20; round++ {
		service := fmt.Sprintf("concurrent_service_%02d", round)
		if _, err := repo.db.Exec(ctx, `INSERT INTO migration_services(name, version) VALUES ($1, 14)`, service); err != nil {
			t.Fatal(err)
		}

		ready := make(chan struct{}, 2)
		start := make(chan struct{})
		errs := make(chan error, 2)
		var wait sync.WaitGroup
		for _, priority := range []int{16, 17} {
			wait.Add(1)
			go func(priority int) {
				defer wait.Done()
				ready <- struct{}{}
				<-start
				errs <- repo.InitializeServicePriorities(ctx, service, []int{priority})
			}(priority)
		}
		for i := 0; i < 2; i++ {
			<-ready
		}
		close(start)
		wait.Wait()
		close(errs)
		for err := range errs {
			if err != nil {
				t.Fatalf("concurrent initialization round %d: %v", round, err)
			}
		}

		got := priorityRows(t, repo, ctx, service)
		if len(got) != 2 || !((got[16] == 14 && got[17] == 0) || (got[16] == 0 && got[17] == 14)) {
			t.Fatalf("concurrent priority claims round %d = %v, want one 14 and one 0", round, got)
		}
	}
}

func TestIntegrationMutatingEntrypointsBootstrapMissingRelations(t *testing.T) {
	repo, ctx := newIntegrationRepository(t)
	version, err := repo.GetServiceVersion(ctx, "fresh", 16)
	if err != nil {
		t.Fatalf("get on absent schema: %v", err)
	}
	if version != 0 {
		t.Fatalf("fresh version = %d, want 0", version)
	}

	resetTrackingSchema(t, repo, ctx)
	if err := repo.AdvanceServiceVersion(ctx, "advance_bootstrap", 16, 3); err != nil {
		t.Fatalf("advance on absent schema: %v", err)
	}
	var priorityVersion, aggregateVersion int
	if err := repo.db.QueryRow(ctx, `SELECT version FROM migration_service_priorities
		WHERE migration_services_name = 'advance_bootstrap' AND priority = 16`).Scan(&priorityVersion); err != nil {
		t.Fatal(err)
	}
	if err := repo.db.QueryRow(ctx, `SELECT version FROM migration_services
		WHERE name = 'advance_bootstrap'`).Scan(&aggregateVersion); err != nil {
		t.Fatal(err)
	}
	if priorityVersion != 3 || aggregateVersion != 3 {
		t.Fatalf("advance bootstrap state = (%d, %d), want (3, 3)", priorityVersion, aggregateVersion)
	}
	if err := repo.AdvanceServiceVersion(ctx, "advance_bootstrap", 16, 1); err != nil {
		t.Fatalf("lower monotonic advance: %v", err)
	}
	if err := repo.db.QueryRow(ctx, `SELECT version FROM migration_service_priorities
		WHERE migration_services_name = 'advance_bootstrap' AND priority = 16`).Scan(&priorityVersion); err != nil {
		t.Fatal(err)
	}
	if err := repo.db.QueryRow(ctx, `SELECT version FROM migration_services
		WHERE name = 'advance_bootstrap'`).Scan(&aggregateVersion); err != nil {
		t.Fatal(err)
	}
	if priorityVersion != 3 || aggregateVersion != 3 {
		t.Fatalf("lower advance changed monotonic state to (%d, %d)", priorityVersion, aggregateVersion)
	}

	resetTrackingSchema(t, repo, ctx)
	fakeLog := migration_log.MigrationServicesLog{
		MigrationServiceName: "fake_bootstrap",
		Priority:             17,
		Version:              2,
		FileName:             "02.sql",
		SQL:                  "SELECT 2",
		Hash:                 "h2",
	}
	if err := repo.FakeMigrationService(ctx, "fake_bootstrap", 2, []migration_log.MigrationServicesLog{fakeLog}); err != nil {
		t.Fatalf("fake on absent schema: %v", err)
	}
	if got := priorityRows(t, repo, ctx, "fake_bootstrap"); !reflect.DeepEqual(got, map[int]int{17: 2}) {
		t.Fatalf("fake bootstrap priority rows = %v, want priority 17 at 2", got)
	}

	if _, err := repo.db.Exec(ctx, `DROP TABLE migration_services`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_service_logs
		(migration_services_name, priority, version, file_name, sql, hash)
		VALUES ('logged', 17, 2, '02.sql', 'SELECT 2', 'h2')`); err != nil {
		t.Fatal(err)
	}
	version, err = repo.GetServiceVersion(ctx, "logged", 17)
	if err != nil {
		t.Fatalf("get with missing services relation: %v", err)
	}
	if version != 2 {
		t.Fatalf("logged version = %d, want 2", version)
	}

	if _, err := repo.db.Exec(ctx, `DROP TABLE migration_service_logs`); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.db.Exec(ctx, `INSERT INTO migration_services(name, version) VALUES ('legacy', 4)`); err != nil {
		t.Fatal(err)
	}
	version, err = repo.GetServiceVersion(ctx, "legacy", 16)
	if err != nil {
		t.Fatalf("get with missing logs relation: %v", err)
	}
	if version != 4 {
		t.Fatalf("legacy version = %d, want 4", version)
	}
}
