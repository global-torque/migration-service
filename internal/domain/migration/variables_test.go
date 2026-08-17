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

func TestUnitExpandMigrationVariables(t *testing.T) {
	t.Setenv("MIGRATION_UNIT_SECRET", "pa'ssword; DROP TABLE important; --")
	t.Setenv("MIGRATION_UNIT_NUMBER", "42")

	query := "INSERT INTO settings (secret, retry_count, copy) VALUES " +
		"(${MIGRATION_UNIT_SECRET}, ${MIGRATION_UNIT_NUMBER}, ${MIGRATION_UNIT_SECRET});"

	expanded, err := expandMigrationVariables(query)
	if err != nil {
		t.Fatalf("expandMigrationVariables() error = %v", err)
	}

	wantSecret := "$migration_variable$pa'ssword; DROP TABLE important; --$migration_variable$"
	if strings.Count(expanded, wantSecret) != 2 {
		t.Fatalf("expanded query does not contain the safely quoted secret twice: %s", expanded)
	}
	if !strings.Contains(expanded, "$migration_variable$42$migration_variable$") {
		t.Fatalf("expanded query does not contain the dynamic number: %s", expanded)
	}
}

func TestUnitExpandMigrationVariablesChoosesNonConflictingDelimiter(t *testing.T) {
	t.Setenv("MIGRATION_UNIT_SECRET", "first $migration_variable$ second")

	expanded, err := expandMigrationVariables("SELECT ${MIGRATION_UNIT_SECRET}")
	if err != nil {
		t.Fatalf("expandMigrationVariables() error = %v", err)
	}

	want := "$migration_variable_1$first $migration_variable$ second$migration_variable_1$"
	if expanded != "SELECT "+want {
		t.Fatalf("expanded query = %q, want %q", expanded, "SELECT "+want)
	}
}

func TestUnitExpandMigrationVariablesAllowsEmptyValues(t *testing.T) {
	t.Setenv("MIGRATION_UNIT_EMPTY", "")

	expanded, err := expandMigrationVariables("SELECT ${MIGRATION_UNIT_EMPTY}")
	if err != nil {
		t.Fatalf("expandMigrationVariables() error = %v", err)
	}
	if expanded != "SELECT $migration_variable$$migration_variable$" {
		t.Fatalf("expanded query = %q", expanded)
	}
}

func TestUnitExpandMigrationVariablesRejectsUnsafeSQLContexts(t *testing.T) {
	t.Setenv("MIGRATION_UNIT_UNSAFE", "'; DROP TABLE important; --\n$$ $body$ second line")

	tests := map[string]string{
		"identifier before":          "SELECT 1 AS alias${MIGRATION_UNIT_UNSAFE};",
		"identifier after":           "SELECT ${MIGRATION_UNIT_UNSAFE}suffix;",
		"line comment":               "-- ${MIGRATION_UNIT_UNSAFE}\nSELECT 1;",
		"nested block comment":       "/* outer /* ${MIGRATION_UNIT_UNSAFE} */ outer */ SELECT 1;",
		"single quoted string":       "SELECT '${MIGRATION_UNIT_UNSAFE}';",
		"double quoted identifier":   `SELECT "${MIGRATION_UNIT_UNSAFE}";`,
		"named dollar body":          "DO $body$ BEGIN RAISE NOTICE ${MIGRATION_UNIT_UNSAFE}; END $body$;",
		"unnamed dollar body":        "DO $$ BEGIN RAISE NOTICE ${MIGRATION_UNIT_UNSAFE}; END $$;",
		"ordinary backslash string":  "SELECT '\\' || 'prefix ${MIGRATION_UNIT_UNSAFE}';",
		"named tag after identifier": "SELECT 1 AS foo$tag$, $tag$ ${MIGRATION_UNIT_UNSAFE} $tag$;",
		"empty tag after identifier": "SELECT 1 AS foo$$, $$ ${MIGRATION_UNIT_UNSAFE} $$;",
	}

	for name, query := range tests {
		t.Run(name, func(t *testing.T) {
			expanded, err := expandMigrationVariables(query)
			if err == nil {
				t.Fatalf("expandMigrationVariables() = %q, nil; want a position error", expanded)
			}
			if !strings.Contains(err.Error(), "MIGRATION_UNIT_UNSAFE") {
				t.Fatalf("error %q does not name the unsafe variable", err)
			}
			if strings.Contains(err.Error(), "DROP TABLE") {
				t.Fatalf("error %q leaks the resolved value", err)
			}
		})
	}
}

func TestUnitExpandMigrationVariablesAcceptsValueAfterQuotedRegions(t *testing.T) {
	t.Setenv("MIGRATION_UNIT_AFTER_QUOTES", "safe value")
	query := "SELECT 'ordinary string', \"quoted identifier\", $body$function-like body$body$; " +
		"INSERT INTO settings (value) VALUES (${MIGRATION_UNIT_AFTER_QUOTES}); -- trailing comment"

	expanded, err := expandMigrationVariables(query)
	if err != nil {
		t.Fatalf("expandMigrationVariables() error = %v", err)
	}
	if !strings.Contains(expanded, "$migration_variable$safe value$migration_variable$") {
		t.Fatalf("expanded query does not contain the rendered value: %q", expanded)
	}
}

func TestUnitExpandMigrationVariablesUnderstandsEscapeStrings(t *testing.T) {
	t.Setenv("MIGRATION_UNIT_AFTER_ESCAPE_STRING", "safe value")
	query := `SELECT E'escaped\' quote'; SELECT ${MIGRATION_UNIT_AFTER_ESCAPE_STRING};`

	expanded, err := expandMigrationVariables(query)
	if err != nil {
		t.Fatalf("expandMigrationVariables() error = %v", err)
	}
	if !strings.Contains(expanded, "$migration_variable$safe value$migration_variable$") {
		t.Fatalf("expanded query does not contain the rendered value: %q", expanded)
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
	const missingName = "MIGRATION_UNIT_DEFINITELY_UNSET"
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

	const secret = "must-not-appear-in-errors"
	t.Setenv("MIGRATION_UNIT_PRESENT", secret)
	_, err := expandMigrationVariables(
		"SELECT ${MIGRATION_UNIT_PRESENT}, ${" + missingName + "}",
	)
	if err == nil {
		t.Fatal("expandMigrationVariables() error = nil, want an unset-variable error")
	}
	if !strings.Contains(err.Error(), missingName) {
		t.Fatalf("error %q does not name the unset variable", err)
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("error %q leaks a resolved variable", err)
	}
}

func TestUnitApplyExecutesExpandedSQLButLogsOriginalSQL(t *testing.T) {
	const secret = "audit-safe-secret"
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
	if len(repo.execQueries) != 1 || !strings.Contains(repo.execQueries[0], secret) {
		t.Fatalf("executed queries = %q, want one expanded query", repo.execQueries)
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

func TestUnitApplyRejectsUnsafePlaceholderBeforeRepositoryChanges(t *testing.T) {
	t.Setenv("MIGRATION_UNIT_UNSAFE_APPLY", "; DROP TABLE credentials; --")

	repo := &variableTestRepository{}
	set := New(repo)
	mig := NewMigration(
		"SELECT 1 AS alias${MIGRATION_UNIT_UNSAFE_APPLY};",
		"01_credentials/01_insert.sql",
	)
	set.Add("credentials", 1, 1, mig)

	_, _, err := set.Apply("credentials", 1, -1, 0, "dev")
	if err == nil || !strings.Contains(err.Error(), "must be separated") {
		t.Fatalf("Set.Apply() error = %v, want an unsafe-position error", err)
	}
	if len(repo.execQueries) != 0 || len(repo.versions) != 0 || len(repo.logs) != 0 {
		t.Fatalf(
			"repository changes = exec:%d versions:%d logs:%d, want all zero",
			len(repo.execQueries), len(repo.versions), len(repo.logs),
		)
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
