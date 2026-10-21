package migration

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"

	"github.com/webdevelop-pro/migration-service/internal/domain/migration_log"
)

type variableTestRepository struct {
	execQueries []string
	execErr     error
	versions    []int
	logs        []migration_log.MigrationServicesLog
}

func (r *variableTestRepository) GetServiceVersion(context.Context, string) (int, error) {
	return 0, nil
}

func (r *variableTestRepository) UpdateServiceVersion(_ context.Context, _ string, version int) error {
	r.versions = append(r.versions, version)
	return nil
}

func (r *variableTestRepository) CreateMigrationTable(context.Context) error {
	return nil
}

func (r *variableTestRepository) Exec(_ context.Context, query string, _ ...interface{}) error {
	r.execQueries = append(r.execQueries, query)
	return r.execErr
}

func (r *variableTestRepository) WriteMigrationServiceLog(_ context.Context, log migration_log.MigrationServicesLog) error {
	r.logs = append(r.logs, log)
	return nil
}

func (r *variableTestRepository) GetHashFromMigrationServiceLog(context.Context, migration_log.MigrationServicesLog) (string, error) {
	return "", nil
}

func TestUnitExpandMigrationVariablesUsesRawValues(t *testing.T) {
	secret := "pa'ssword; DROP TABLE important; --"
	t.Setenv("MIGRATION_UNIT_SECRET", secret)
	t.Setenv("MIGRATION_UNIT_NUMBER", "42")

	query := "INSERT INTO settings (secret, retry_count, copy) VALUES " +
		"(${MIGRATION_UNIT_SECRET}, ${MIGRATION_UNIT_NUMBER}, ${MIGRATION_UNIT_SECRET});"
	want := "INSERT INTO settings (secret, retry_count, copy) VALUES " +
		"(" + secret + ", 42, " + secret + ");"

	expanded, err := expandMigrationVariables(query)
	if err != nil {
		t.Fatalf("expandMigrationVariables() error = %v", err)
	}
	if expanded != want {
		t.Fatalf("expandMigrationVariables() = %q, want %q", expanded, want)
	}
}

func TestUnitExpandMigrationVariablesRendersDOGrantRaw(t *testing.T) {
	t.Setenv("MIGRATION_RUNTIME_ROLE", "migration_runtime")
	query := "DO $body$ BEGIN EXECUTE 'GRANT USAGE ON SCHEMA public TO ${MIGRATION_RUNTIME_ROLE}'; END $body$;"
	want := "DO $body$ BEGIN EXECUTE 'GRANT USAGE ON SCHEMA public TO migration_runtime'; END $body$;"

	expanded, err := expandMigrationVariables(query)
	if err != nil {
		t.Fatalf("expandMigrationVariables() error = %v", err)
	}
	if expanded != want {
		t.Fatalf("expandMigrationVariables() = %q, want %q", expanded, want)
	}
}

func TestUnitExpandMigrationVariablesAllowsEmptyValues(t *testing.T) {
	t.Setenv("MIGRATION_UNIT_EMPTY", "")

	expanded, err := expandMigrationVariables("SELECT before${MIGRATION_UNIT_EMPTY}after")
	if err != nil {
		t.Fatalf("expandMigrationVariables() error = %v", err)
	}
	if expanded != "SELECT beforeafter" {
		t.Fatalf("expandMigrationVariables() = %q, want %q", expanded, "SELECT beforeafter")
	}
}

func TestUnitExpandMigrationVariablesSupportsEverySQLContextAndAdjacency(t *testing.T) {
	value := "raw'value; --\nsecond line"
	t.Setenv("MIGRATION_UNIT_RAW", value)

	query := "SELECT '${MIGRATION_UNIT_RAW}', \"${MIGRATION_UNIT_RAW}\"; -- ${MIGRATION_UNIT_RAW}\n" +
		"/* ${MIGRATION_UNIT_RAW} */ SELECT prefix${MIGRATION_UNIT_RAW}suffix; " +
		"DO $body$ BEGIN RAISE NOTICE '${MIGRATION_UNIT_RAW}'; END $body$;"
	want := "SELECT '" + value + "', \"" + value + "\"; -- " + value + "\n" +
		"/* " + value + " */ SELECT prefix" + value + "suffix; " +
		"DO $body$ BEGIN RAISE NOTICE '" + value + "'; END $body$;"

	expanded, err := expandMigrationVariables(query)
	if err != nil {
		t.Fatalf("expandMigrationVariables() error = %v", err)
	}
	if expanded != want {
		t.Fatalf("expandMigrationVariables() = %q, want %q", expanded, want)
	}
}

func TestUnitExpandMigrationVariablesPreservesRawSpecialBytes(t *testing.T) {
	value := "line\n\t\r\x01'\\$migration_variable$"
	t.Setenv("MIGRATION_UNIT_SPECIAL", value)

	expanded, err := expandMigrationVariables("SELECT ${MIGRATION_UNIT_SPECIAL}")
	if err != nil {
		t.Fatalf("expandMigrationVariables() error = %v", err)
	}
	if expanded != "SELECT "+value {
		t.Fatalf("expandMigrationVariables() = %q, want %q", expanded, "SELECT "+value)
	}
}

func TestUnitExpandMigrationVariablesDoesNotReexpandIntroducedMarkers(t *testing.T) {
	t.Setenv("MIGRATION_UNIT_INTRODUCED", "${MIGRATION_UNIT_OTHER}")
	t.Setenv("MIGRATION_UNIT_OTHER", "expanded")

	expanded, err := expandMigrationVariables("SELECT ${MIGRATION_UNIT_INTRODUCED}")
	if err != nil {
		t.Fatalf("expandMigrationVariables() error = %v", err)
	}
	if expanded != "SELECT ${MIGRATION_UNIT_OTHER}" {
		t.Fatalf("expandMigrationVariables() = %q, want introduced marker unchanged", expanded)
	}
}

func TestUnitApplyDoesNotRenderEnvironmentMarkerIntroducedByVariable(t *testing.T) {
	t.Setenv("MIGRATION_UNIT_INTRODUCED_ENV", "{{ ENV_NAME }}")

	repo := &variableTestRepository{}
	set := New(repo)
	mig := NewMigration(
		"SELECT ${MIGRATION_UNIT_INTRODUCED_ENV}_{{ ENV_NAME }};",
		"01_identifiers/01_marker.sql",
	)
	set.Add("identifiers", 1, 1, mig)

	_, _, err := set.Apply("identifiers", 1, -1, 0, "dev")
	if err != nil {
		t.Fatalf("Set.Apply() error = %v", err)
	}
	if len(repo.execQueries) != 1 || repo.execQueries[0] != "SELECT {{ ENV_NAME }}_dev;" {
		t.Fatalf("executed queries = %q, want introduced marker unchanged", repo.execQueries)
	}
}

func TestUnitExpandMigrationVariablesLeavesExistingDollarBodiesUnchanged(t *testing.T) {
	query := "CREATE FUNCTION answer() RETURNS integer AS $body$ BEGIN RETURN 42; END $body$ LANGUAGE plpgsql;"

	expanded, err := expandMigrationVariables(query)
	if err != nil {
		t.Fatalf("expandMigrationVariables() error = %v", err)
	}
	if expanded != query {
		t.Fatalf("expandMigrationVariables() = %q, want unchanged query", expanded)
	}
}

func TestUnitExpandMigrationVariablesRejectsUnsetValuesWithoutLeakingSecrets(t *testing.T) {
	const (
		missingName       = "MIGRATION_UNIT_DEFINITELY_UNSET"
		secondMissingName = "MIGRATION_UNIT_SECOND_UNSET"
	)
	for _, name := range []string{missingName, secondMissingName} {
		original, existed := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatalf("os.Unsetenv(%s) error = %v", name, err)
		}
		name, original, existed := name, original, existed
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(name, original)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}

	const secret = "must-not-appear-in-errors"
	t.Setenv("MIGRATION_UNIT_PRESENT", secret)
	_, err := expandMigrationVariables(
		"SELECT ${MIGRATION_UNIT_PRESENT}, ${" + secondMissingName + "}, ${" + missingName + "}, ${" + secondMissingName + "}",
	)
	if err == nil {
		t.Fatal("expandMigrationVariables() error = nil, want an unset-variable error")
	}
	wantError := "migration variables are not set: " + secondMissingName + ", " + missingName
	if err.Error() != wantError {
		t.Fatalf("error = %q, want %q", err, wantError)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error %q leaks a resolved variable", err)
	}
}

func TestUnitApplyExecutesExpandedSQLButLogsOriginalSQL(t *testing.T) {
	const secret = "audit;--safe-secret"
	t.Setenv("MIGRATION_UNIT_APPLY_SECRET", secret)

	repo := &variableTestRepository{}
	set := New(repo)
	originalQuery := "INSERT INTO credentials (secret) VALUES (${MIGRATION_UNIT_APPLY_SECRET});"
	mig := NewMigration(originalQuery, "01_credentials/01_insert.sql")
	set.Add("credentials", 1, 1, mig)

	n, lastVersion, err := set.Apply("credentials", 1, -1, 0, "dev")
	if err != nil {
		t.Fatalf("Set.Apply() error = %v", err)
	}
	if n != 1 || lastVersion != 1 {
		t.Fatalf("Set.Apply() = (%d, %d), want (1, 1)", n, lastVersion)
	}
	wantQuery := "INSERT INTO credentials (secret) VALUES (" + secret + ");"
	if len(repo.execQueries) != 1 || repo.execQueries[0] != wantQuery {
		t.Fatalf("executed queries = %q, want %q", repo.execQueries, wantQuery)
	}
	if len(repo.logs) != 1 || repo.logs[0].SQL != originalQuery {
		t.Fatalf("migration logs = %#v, want original SQL", repo.logs)
	}
	if strings.Contains(repo.logs[0].SQL, secret) {
		t.Fatal("migration log contains the resolved secret")
	}
	if repo.logs[0].Hash != mig.Hash {
		t.Fatalf("logged hash = %q, want %q", repo.logs[0].Hash, mig.Hash)
	}
}

func TestUnitApplyRejectsUnsetVariableBeforeRepositoryChanges(t *testing.T) {
	const missingName = "MIGRATION_UNIT_APPLY_UNSET"
	original, existed := os.LookupEnv(missingName)
	if err := os.Unsetenv(missingName); err != nil {
		t.Fatalf("os.Unsetenv() error = %v", err)
	}
	t.Cleanup(func() {
		if existed {
			_ = os.Setenv(missingName, original)
		} else {
			_ = os.Unsetenv(missingName)
		}
	})

	repo := &variableTestRepository{}
	set := New(repo)
	mig := NewMigration(
		"--- allow_error: true\nINSERT INTO credentials (secret) VALUES (${"+missingName+"});",
		"01_credentials/01_insert.sql",
	)
	set.Add("credentials", 1, 1, mig)

	_, _, err := set.Apply("credentials", 1, -1, 0, "dev")
	if err == nil || !strings.Contains(err.Error(), missingName) {
		t.Fatalf("Set.Apply() error = %v, want an error naming %s", err, missingName)
	}
	if len(repo.execQueries) != 0 || len(repo.versions) != 0 || len(repo.logs) != 0 {
		t.Fatalf(
			"repository changes = exec:%d versions:%d logs:%d, want all zero",
			len(repo.execQueries), len(repo.versions), len(repo.logs),
		)
	}
}

func TestUnitGetSQLRetainsCanonicalMigrationVariable(t *testing.T) {
	const secret = "final-sql-secret"
	t.Setenv("MIGRATION_UNIT_FINAL_SQL", secret)

	repo := &variableTestRepository{}
	set := New(repo)
	originalQuery := "INSERT INTO credentials (secret) VALUES (${MIGRATION_UNIT_FINAL_SQL});"
	set.Add(
		"credentials",
		1,
		1,
		NewMigration(originalQuery, "01_credentials/01_insert.sql"),
	)

	got, err := set.GetSQL("credentials", 1, -1)
	if err != nil {
		t.Fatalf("Set.GetSQL() error = %v", err)
	}
	if strings.TrimSpace(got) != originalQuery {
		t.Fatalf("Set.GetSQL() = %q, want canonical %q", got, originalQuery)
	}
	if strings.Contains(got, secret) || !strings.Contains(got, "${MIGRATION_UNIT_FINAL_SQL}") {
		t.Fatalf("Set.GetSQL() = %q, want placeholder without resolved value", got)
	}
}

func TestUnitApplyRedactsDatabaseErrorsContainingResolvedValues(t *testing.T) {
	const secret = "database-error-secret"
	t.Setenv("MIGRATION_UNIT_ERROR_SECRET", secret)
	sentinel := errors.New("database rejected value")

	repo := &variableTestRepository{
		execErr: fmt.Errorf("invalid value %q: %w", secret, sentinel),
	}
	set := New(repo)
	mig := NewMigration(
		"INSERT INTO credentials (secret) VALUES (${MIGRATION_UNIT_ERROR_SECRET});",
		"01_credentials/01_insert.sql",
	)
	set.Add("credentials", 1, 1, mig)

	_, _, err := set.Apply("credentials", 1, -1, 0, "dev")
	if err == nil {
		t.Fatal("Set.Apply() error = nil, want a database execution error")
	}
	if strings.Contains(err.Error(), secret) || strings.Contains(err.Error(), sentinel.Error()) {
		t.Fatalf("Set.Apply() error leaks database details: %v", err)
	}
	if !strings.Contains(err.Error(), "MIGRATION_UNIT_ERROR_SECRET") {
		t.Fatalf("Set.Apply() error = %v, want the variable name", err)
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("errors.Is(%v, sentinel) = false, want true", err)
	}
	if len(repo.versions) != 0 || len(repo.logs) != 0 {
		t.Fatalf("repository changes = versions:%d logs:%d, want both zero", len(repo.versions), len(repo.logs))
	}
}

func TestUnitApplyDoesNotResolveVariablesForSkippedEnvironment(t *testing.T) {
	const missingName = "MIGRATION_UNIT_SKIPPED_UNSET"
	if err := os.Unsetenv(missingName); err != nil {
		t.Fatalf("os.Unsetenv() error = %v", err)
	}

	repo := &variableTestRepository{}
	set := New(repo)
	mig := NewMigration(
		"--- required_env: master\nSELECT ${"+missingName+"};",
		"01_credentials/01_insert.sql",
	)
	set.Add("credentials", 1, 1, mig)

	n, _, err := set.Apply("credentials", 1, -1, 0, "dev")
	if err != nil {
		t.Fatalf("Set.Apply() error = %v", err)
	}
	if n != 1 {
		t.Fatalf("Set.Apply() applied versions = %d, want 1 to preserve existing skip semantics", n)
	}
	if len(repo.execQueries) != 0 {
		t.Fatalf("executed queries = %q, want none", repo.execQueries)
	}
}
