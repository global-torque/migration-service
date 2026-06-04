package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
	"github.com/webdevelop-pro/go-common/configurator"
	"github.com/webdevelop-pro/go-common/db"
	"github.com/webdevelop-pro/go-common/logger"
	"github.com/webdevelop-pro/migration-service/internal/adapters"
	"github.com/webdevelop-pro/migration-service/internal/adapters/repository/postgres"
	"github.com/webdevelop-pro/migration-service/internal/app"
	"github.com/webdevelop-pro/migration-service/internal/domain/migration"
	"github.com/webdevelop-pro/migration-service/internal/domain/migration_log"
)

func testInit(t *testing.T) (logger.Logger, *configurator.Configurator, *postgres.Repository, *app.App, *db.DB, context.Context) {
	t.Helper()
	requireDBEnv(t)

	ctx := context.Background()
	_log := logger.DefaultStdoutLogger(ctx, "info")
	c := configurator.NewConfigurator()
	pg, err := postgres.New()
	if err != nil {
		t.Fatalf("cannot create postgres repository: %v", err)
	}
	_migration := app.New(c, pg)
	rawPG, err := db.New(ctx)
	if err != nil {
		t.Fatalf("cannot create raw postgres db: %v", err)
	}
	t.Cleanup(func() {
		rawPG.Close()
		pg.Close()
	})

	_, err = rawPG.Exec(context.Background(), "DROP TABLE IF EXISTS email_emails")
	if err != nil {
		_log.Fatal().Err(err).Msg("can't drop table email_emails from DB")
	}
	_, err = rawPG.Exec(context.Background(), "DROP TABLE IF EXISTS user_users")
	if err != nil {
		_log.Fatal().Err(err).Msg("can't drop table user_users from DB")
	}
	_, err = rawPG.Exec(context.Background(), "DROP TABLE IF EXISTS migration_service_logs")
	if err != nil {
		_log.Fatal().Err(err).Msg("can't drop table migration_service_logs from DB")
	}
	_, err = rawPG.Exec(context.Background(), "DROP TABLE IF EXISTS migration_services")
	if err != nil {
		_log.Fatal().Err(err).Msg("can't drop table migration_services from DB")
	}

	return _log, c, pg, _migration, rawPG, ctx
}

func requireDBEnv(t *testing.T) {
	t.Helper()

	if err := configurator.LoadDotEnv(); err != nil {
		t.Fatalf("cannot load env file: %v", err)
	}

	if strings.EqualFold(os.Getenv("SKIP_DB_TESTS"), "true") {
		t.Skip("skipping DB integration test because SKIP_DB_TESTS=true")
	}

	required := []string{"DB_TYPE", "DB_USER", "DB_PASSWORD", "DB_DATABASE", "DB_APP_NAME"}
	missing := make([]string, 0, len(required))
	for _, key := range required {
		if strings.TrimSpace(os.Getenv(key)) == "" {
			missing = append(missing, key)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("missing DB integration env vars: %s; set SKIP_DB_TESTS=true to skip intentionally", strings.Join(missing, ", "))
	}
}

func checkResults(t *testing.T, rawPG *db.DB, log logger.Logger, expName string, expVer int) {
	ctx := context.Background()
	name := ""
	ver := 0
	query := "SELECT name, version FROM migration_services ORDER by id DESC LIMIT 1"
	if err := rawPG.QueryRow(ctx, query).Scan(&name, &ver); err != nil {
		log.Fatal().Err(err).Msg("cannot get values for migration service")
		t.Error()
	}
	if name != expName || ver != expVer {
		log.Fatal().Msgf("data does not match %s!=%s or %d!=%d", name, expName, ver, expVer)
		t.Error()
	}
}

func checkResultsByService(t *testing.T, rawPG *db.DB, log logger.Logger, serviceName string, expVer int) {
	ctx := context.Background()
	ver := 0
	query := fmt.Sprintf("SELECT version FROM migration_services WHERE name='%s' ORDER by id DESC LIMIT 1", serviceName)
	if err := rawPG.QueryRow(ctx, query).Scan(&ver); err != nil {
		log.Fatal().Err(err).Msg("cannot get values for migration service")
		t.Error()
	}
	if ver != expVer {
		log.Fatal().Msgf("version does not match %d!=%d", ver, expVer)
		t.Error()
	}
}

func checkValueResults(t *testing.T, rawPG *db.DB, log logger.Logger, expVal, tableName, columnName string, id int) {
	ctx := context.Background()
	val := ""
	query := fmt.Sprintf("SELECT %s FROM %s WHERE id = %d LIMIT 1", columnName, tableName, id)
	if err := rawPG.QueryRow(ctx, query).Scan(&val); err != nil {
		log.Fatal().Err(err).Msgf("cannot get values for %s from %s", columnName, tableName)
		t.Error()
	}
	if val != expVal {
		log.Fatal().Msgf("data does not match %s!=%s", val, expVal)
		t.Error()
	}
}

func checkNullValueResults(t *testing.T, rawPG *db.DB, log logger.Logger, tableName, columnName string, id int) {
	ctx := context.Background()
	val := ""
	query := fmt.Sprintf("SELECT %s FROM %s WHERE id = %d LIMIT 1", columnName, tableName, id)
	err := rawPG.QueryRow(ctx, query).Scan(&val)
	if err != pgx.ErrNoRows {
		if err != nil {
			log.Fatal().Err(err).Msgf("cannot get values for %s from %s", columnName, tableName)
		} else {
			log.Fatal().Err(err).Msgf("result should be 'No rows', but value received: %s", val)
		}
		t.Error()
	}
}

func checkRecordsCount(t *testing.T, rawPG *db.DB, log logger.Logger, tableName string, expVal int) {
	ctx := context.Background()
	val := -1
	query := fmt.Sprintf("SELECT count(*) FROM %s", tableName)
	if err := rawPG.QueryRow(ctx, query).Scan(&val); err != nil {
		log.Fatal().Err(err).Msgf("cannot get rows count from %s", tableName)
		t.Error()
	}
	if val != expVal {
		log.Fatal().Msgf("rows count didn't match %d!=%d", val, expVal)
		t.Error()
	}
}

// TestIgnoreNonSQLFiles checks if only *.sql files are applied
func TestIgnoreNonSQLFiles(t *testing.T) {
	_log, _, pg, _migration, rawPG, ctx := testInit(t)

	err := pg.CreateMigrationTable(ctx)
	if err != nil {
		_log.Fatal().Err(err).Msg("cannot create migration table")
	}

	if err := _migration.ApplyAll("./migrations/TestIgnoreNonSQLFiles"); err != nil {
		_log.Fatal().Err(err).Msg("cannot apply migrations")
	}

	checkResults(t, rawPG, _log, "user_seeds", 2)
}

// TestServicePriorities checks if services executed in correct order
func TestServicePriorities(t *testing.T) {
	// we will create new _migration for email service
	// and verify if _migration will be applied in correct order
	// first user and then _migration
	_log, _, _, _migration, rawPG, _ := testInit(t)

	// Create two different services with different indexes
	// make sure _migration executed in correct order

	// ToDo
	// check uniqueness of services numbers
	/*
		// broken file record, _migration should not be applied create new email table
		if err := os.WriteFile("./migrations/02_email/01_init-second-time.sql", []byte(SQL), 0644); err != nil {
			_log.Fatal().Err(err).Msg("cannot create a file")
		}
	*/
	if err := _migration.ApplyAll("./migrations/TestServicePriorities"); err != nil {
		_log.Fatal().Err(err).Msg("cannot apply migrations")
	}

	checkResults(t, rawPG, _log, "email", 1)
}

func TestAllowError(t *testing.T) {
	// we will create new migration for email service
	// and verify if migration will be applied in correct order
	// first user and then migration
	mig := migration.NewMigration(`--
	-- --   allow_error: true  
	--
	THIS IS SQL WITH AN ERROR`, "./migration")

	if mig.AllowError != true {
		log.Error().Msg("allow error should be true")
		t.Fail()
	}
}

// TestMigrationPriorities checks if files executed in correct order
func TestMigrationPriorities(t *testing.T) {
	// we will create new _migration for email service
	// and verify if _migration will be applied in correct order
	// first user and then _migration
	_log, _, _, _migration, rawPG, _ := testInit(t)

	_log.Debug().Msg("trying to apply _migration")

	// ToDo
	// check uniqueness of services numbers
	/*
		// broken file record, _migration should not be applied since we have order duplication
		if err := os.WriteFile("./migrations/01_user/03_add_bitint-for-second-time.sql", []byte(SQL), 0644); err != nil {
			_log.Fatal().Err(err).Msg("cannot create a file")
		}
	*/

	if err := _migration.ApplyAll("./migrations/TestMigrationPriorities"); err != nil {
		_log.Fatal().Err(err).Msg("cannot apply migrations")
	}

	checkResults(t, rawPG, _log, "email_emails", 2)
}

// TestMigrationApplied checks applied migrations commited to db
func TestMigrationCommited(t *testing.T) {
	// we will create new _migration for email service
	// and verify if _migration will be applied in correct order
	// first user and then _migration
	_log, _, _, _migration, rawPG, _ := testInit(t)

	_log.Debug().Msg("trying to apply _migration")

	// ToDo
	// check uniqueness of services numbers
	/*
		// broken file record, _migration should not be applied since we have order duplication
		if err := os.WriteFile("./migrations/01_user/03_add_bitint-for-second-time.sql", []byte(SQL), 0644); err != nil {
			_log.Fatal().Err(err).Msg("cannot create a file")
		}
	*/

	if err := _migration.ApplyAll("./migrations/TestMigrationCommited"); err == nil {
		_log.Fatal().Msg("last _migration should fail")
		t.Fail()
	}

	checkResults(t, rawPG, _log, "user_users", 1)
}

func TestApplyMigrationRollsBackSQLWhenMetadataFails(t *testing.T) {
	_log, _, pg, _, rawPG, ctx := testInit(t)

	if err := pg.CreateMigrationTable(ctx); err != nil {
		_log.Fatal().Err(err).Msg("cannot create migration table")
	}
	if _, err := rawPG.Exec(ctx, "DROP TABLE migration_service_logs"); err != nil {
		_log.Fatal().Err(err).Msg("can't drop table migration_service_logs from DB")
	}

	result, err := pg.ApplyMigration(ctx, adapters.AppliedMigration{
		ServiceName:   "atomic_metadata",
		Version:       1,
		Query:         "CREATE TABLE atomic_metadata_rollback (id int)",
		UpdateVersion: true,
		Log: migration_log.MigrationServicesLog{
			MigrationServiceName: "atomic_metadata",
			Priority:             1,
			Version:              1,
			FileName:             "01_atomic.sql",
			SQL:                  "CREATE TABLE atomic_metadata_rollback (id int)",
			Hash:                 "atomic-hash",
		},
	})
	if err == nil {
		t.Fatal("expected metadata write failure")
	}
	if !result.QuerySucceeded {
		t.Fatal("migration SQL should have succeeded before metadata failure")
	}

	var tableName *string
	if err := rawPG.QueryRow(ctx, "SELECT to_regclass('public.atomic_metadata_rollback')").Scan(&tableName); err != nil {
		_log.Fatal().Err(err).Msg("cannot check rollback table")
	}
	if tableName != nil {
		t.Fatalf("expected migration SQL to roll back, found table %s", *tableName)
	}

	ver, err := pg.GetServiceVersion(ctx, "atomic_metadata")
	if err != nil {
		t.Fatalf("cannot read service version: %v", err)
	}
	if ver != 0 {
		t.Fatalf("expected service version rollback, got %d", ver)
	}
}

// TestForceApply checks force apply migrations to db
func TestForceApply(t *testing.T) {
	// we will create new migration for user service in first phase
	// and verify if migration with lower version will be applied by forceApply
	// and verify, that version of service still 14 after applying version 3
	_log, _, _, _migration, rawPG, _ := testInit(t)
	_log.Debug().Msg("trying to apply first phase of migrations")

	// First phase - apply init migrations
	if err := _migration.ApplyAll("./migrations/TestForceApply/FirstPhase"); err != nil {
		_log.Fatal().Err(err).Msg("cannot apply migrations")
	}

	checkResultsByService(t, rawPG, _log, "user_users", 14)

	// Second phase - try to apply migration with lower version
	if err := _migration.ForceApply([]string{"./migrations/TestForceApply/SecondPhase"}); err != nil {
		_log.Fatal().Err(err).Msg("cannot apply migrations")
	}
	checkResultsByService(t, rawPG, _log, "user_users", 14)
	checkResultsByService(t, rawPG, _log, "user_users_seeds", 1)
	checkValueResults(t, rawPG, _log, "+1 (555) 555-1234", "user_users", "phone", 1)
}

// TestFakeApply checks writing applied migrations to migration_services table without actually applying migrations
func TestFakeApply(t *testing.T) {
	// we will create new migration for user service in first phase
	// and verify if migration will be checked as finished without applying
	_log, _, _, _migration, rawPG, _ := testInit(t)
	_log.Debug().Msg("trying to apply first phase of migrations")

	// First phase - apply init migrations
	if err := _migration.ApplyAll("./migrations/TestFakeApply/FirstPhase"); err != nil {
		_log.Fatal().Err(err).Msg("cannot apply migrations")
	}

	checkResultsByService(t, rawPG, _log, "user_users", 1)

	// Second phase - try to apply migration with lower version
	if err := _migration.FakeApply([]string{"./migrations/TestFakeApply/SecondPhase"}); err != nil {
		_log.Fatal().Err(err).Msg("cannot apply migrations")
	}
	checkResultsByService(t, rawPG, _log, "user_users", 1)
	checkResultsByService(t, rawPG, _log, "user_users_seeds", 2)
	checkNullValueResults(t, rawPG, _log, "user_users", "name", 1)
}

// TestMigrationLog checks writing logs to migration_service_logs table
func TestMigrationLog(t *testing.T) {
	// we will create new migrations for user_user service and verify
	// if all records was written to migration_service_log
	_log, _, _, _migration, rawPG, _ := testInit(t)
	_log.Debug().Msg("trying to apply migrations")

	// apply migrations
	if err := _migration.ApplyAll("./migrations/TestMigrationLog"); err != nil {
		_log.Fatal().Err(err).Msg("cannot apply migrations")
	}
	checkResultsByService(t, rawPG, _log, "user_users", 3)
	checkRecordsCount(t, rawPG, _log, "migration_service_logs", 3)
	checkValueResults(t, rawPG, _log, "03_add_bitint.sql", "migration_service_logs", "file_name", 3)
}

// TestMigrationLog checks writing logs to migration_service_logs table
func TestRequiredEnvInvertion(t *testing.T) {
	// we will create new migrations for user_user service and verify
	// if all records was written to migration_service_log
	t.Setenv("ENV_NAME", "master")
	_log, _, _, _migration, rawPG, _ := testInit(t)
	_log.Debug().Msg("checking required env comment")

	// apply migrations for non master branch
	if err := _migration.ApplyAll("./migrations/RequiredEnv/BranchInvertion"); err != nil {
		_log.Fatal().Err(err).Msg("cannot apply migrations")
	}

	ver := 1
	query := fmt.Sprintf("SELECT version FROM migration_services WHERE name='%s' ORDER by id DESC LIMIT 1", "user_users")
	err := rawPG.QueryRow(context.Background(), query).Scan(&ver)
	if err == nil {
		t.Errorf("query should return an error cause we should not have any migrations for user_users")
	}
	if err.Error() != "no rows in result set" {
		t.Errorf("query should return an error cause we should not have any migrations for user_users")
	}

	t.Setenv("ENV_NAME", "dev")
	_log, _, _, _migration, rawPG, _ = testInit(t)
	// apply migrations for non master branch
	if err := _migration.ApplyAll("./migrations/RequiredEnv/BranchInvertion"); err != nil {
		_log.Fatal().Err(err).Msg("cannot apply migrations")
	}

	checkResultsByService(t, rawPG, _log, "user", 1)
	// checkRecordsCount(t, rawPG, _log, "migration_service_logs", 0)
	// checkValueResults(t, rawPG, _log, "01_user_users.sql", "migration_service_logs", "file_name", 2)
}

func TestRequiredEnvMultipleBranch(t *testing.T) {
	// we will create new migrations for user_user service and verify
	// if all records was written to migration_service_log
	t.Setenv("ENV_NAME", "master")
	_log, _, _, _migration, rawPG, _ := testInit(t)
	_log.Debug().Msg("checking required env comment")

	// apply migrations for non master branch
	if err := _migration.ApplyAll("./migrations/RequiredEnv/MultipleBranches"); err != nil {
		_log.Fatal().Err(err).Msg("cannot apply migrations")
	}

	ver := 1
	query := fmt.Sprintf("SELECT version FROM migration_services WHERE name='%s' ORDER by id DESC LIMIT 1", "user_users")
	err := rawPG.QueryRow(context.Background(), query).Scan(&ver)
	if err == nil {
		t.Errorf("query should return an error cause we should not have any migrations for user_users")
	}
	if err.Error() != "no rows in result set" {
		t.Errorf("query should return an error cause we should not have any migrations for user_users")
	}

	t.Setenv("ENV_NAME", "stage")
	_log, _, _, _migration, rawPG, _ = testInit(t)
	// apply migrations for non master branch
	if err := _migration.ApplyAll("./migrations/RequiredEnv/MultipleBranches"); err != nil {
		_log.Fatal().Err(err).Msg("cannot apply migrations")
	}

	checkResultsByService(t, rawPG, _log, "user", 1)
	// checkRecordsCount(t, rawPG, _log, "migration_service_logs", 0)
	// checkValueResults(t, rawPG, _log, "01_user_users.sql", "migration_service_logs", "file_name", 2)
}

func TestExitCode(t *testing.T) {
	// We will run migration with a bad sql
	// to verify return code

	requireDBEnv(t)

	cmd := exec.Command("go", "run", "./cmd/server", "--apply-only")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(),
		"CORS_ALLOWED_ORIGINS=",
		"MIGRATION_DIR=./tests/migrations/TestErrorCode",
	)

	output, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("expected migration command to return a non-zero exit code")
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected process exit error, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit code 1, got %d\n%s", exitErr.ExitCode(), output)
	}
	if !strings.Contains(string(output), "failed to apply all migrations") {
		t.Fatalf("expected migration failure output, got:\n%s", output)
	}
}

func TestStartupStopsOnMigrationFailure(t *testing.T) {
	requireDBEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "go", "run", "./cmd/server")
	cmd.Dir = ".."
	cmd.Env = append(os.Environ(),
		"CORS_ALLOWED_ORIGINS=",
		"MIGRATION_DIR=./tests/migrations/TestErrorCode",
		"PORT=18085",
	)

	output, err := cmd.CombinedOutput()
	if ctx.Err() == context.DeadlineExceeded {
		t.Fatalf("migration command did not exit after migration failure; it likely continued serving HTTP:\n%s", output)
	}
	if err == nil {
		t.Fatal("expected migration command to return a non-zero exit code")
	}

	exitErr, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("expected process exit error, got %T: %v\n%s", err, err, output)
	}
	if exitErr.ExitCode() != 1 {
		t.Fatalf("expected exit code 1, got %d\n%s", exitErr.ExitCode(), output)
	}
}

func TestMigrationSubFolderOrder(t *testing.T) {
	// Make sure any subfolders are run after main files

	// we have a bug with random map keys during for loop
	// so we need to run migration for several times
	for x := 0; x < 20; x++ {
		t.Run(fmt.Sprintf("iteration_%d", x), func(t *testing.T) {
			_log, _, _, _migration, rawPG, _ := testInit(t)
			_log.Debug().Msg("checking migration order")

			// apply migrations for non master branch
			if err := _migration.ApplyAll("./migrations/TestTriggerFolder"); err != nil {
				_log.Fatal().Err(err).Msg("cannot apply migrations")
			}

			checkResultsByService(t, rawPG, _log, "user_users", 2)
			checkResultsByService(t, rawPG, _log, "user_users_trigger", 1)
		})
	}
}
