package migration

import (
	"strings"
	"testing"
)

func TestUnitRenderEnvironmentIdentifier(t *testing.T) {
	tests := map[string]struct {
		env  string
		want string
	}{
		"dev":  {env: "dev", want: "CREATE ROLE svc_dev_queue_api NOLOGIN;"},
		"prod": {env: "prod", want: "CREATE ROLE svc_prod_queue_api NOLOGIN;"},
	}

	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			got, err := renderEnvironmentIdentifier(
				"CREATE ROLE svc_{{ ENV_NAME }}_queue_api NOLOGIN;",
				test.env,
			)
			if err != nil {
				t.Fatalf("renderEnvironmentIdentifier() error = %v", err)
			}
			if got != test.want {
				t.Fatalf("renderEnvironmentIdentifier() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestUnitRenderEnvironmentIdentifierRejectsInvalidEnvironment(t *testing.T) {
	tests := map[string]string{
		"unset":           "",
		"uppercase":       "DEV",
		"leading digit":   "1dev",
		"hyphen":          "dev-west",
		"sql punctuation": "dev;drop_role",
	}

	for name, envName := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := renderEnvironmentIdentifier("CREATE ROLE svc_{{ ENV_NAME }}_queue_api;", envName)
			if err == nil {
				t.Fatal("renderEnvironmentIdentifier() error = nil, want validation error")
			}
		})
	}
}

func TestUnitRenderEnvironmentIdentifierRejectsUnknownNestedAndQuotedTemplates(t *testing.T) {
	tests := map[string]string{
		"unknown":                "CREATE ROLE svc_{{ REGION }}_queue_api;",
		"nested":                 "CREATE ROLE svc_{{ {{ ENV_NAME }} }}_queue_api;",
		"single quoted":          "SELECT '{{ ENV_NAME }}';",
		"double quoted":          `CREATE ROLE "svc_{{ ENV_NAME }}_queue_api";`,
		"line comment":           "-- {{ ENV_NAME }}\nSELECT 1;",
		"block comment":          "/* {{ ENV_NAME }} */ SELECT 1;",
		"dollar quoted":          "DO $$ BEGIN RAISE NOTICE '{{ ENV_NAME }}'; END $$;",
		"unmatched close marker": "SELECT 1; }}",
	}

	for name, query := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := renderEnvironmentIdentifier(query, "dev")
			if err == nil {
				t.Fatal("renderEnvironmentIdentifier() error = nil, want template-position error")
			}
		})
	}
}

func TestUnitRenderEnvironmentIdentifierRejectsOrdinaryStringBackslashes(t *testing.T) {
	query := `SELECT '\\'; CREATE ROLE svc_{{ ENV_NAME }}_queue_api;`

	_, err := renderEnvironmentIdentifier(query, "dev")
	if err == nil || !strings.Contains(err.Error(), "E-prefixed") {
		t.Fatalf("renderEnvironmentIdentifier() error = %v, want ordinary-string backslash error", err)
	}
}

func TestUnitRenderEnvironmentIdentifierAllowsEscapeStringBackslashes(t *testing.T) {
	query := `SELECT E'escaped\\\\value'; CREATE ROLE svc_{{ ENV_NAME }}_queue_api;`

	got, err := renderEnvironmentIdentifier(query, "dev")
	if err != nil {
		t.Fatalf("renderEnvironmentIdentifier() error = %v", err)
	}
	want := `SELECT E'escaped\\\\value'; CREATE ROLE svc_dev_queue_api;`
	if got != want {
		t.Fatalf("renderEnvironmentIdentifier() = %q, want %q", got, want)
	}
}

func TestUnitRenderEnvironmentIdentifierRejectsLongGeneratedIdentifier(t *testing.T) {
	prefix := strings.Repeat("a", 60)
	_, err := renderEnvironmentIdentifier("CREATE ROLE "+prefix+"_{{ ENV_NAME }};", "dev")
	if err == nil || !strings.Contains(err.Error(), "63") {
		t.Fatalf("renderEnvironmentIdentifier() error = %v, want 63-byte limit error", err)
	}
}

func TestUnitRenderEnvironmentIdentifierRejectsLongIdentifierWithMultipleMarkers(t *testing.T) {
	prefix := strings.Repeat("a", 54)
	_, err := renderEnvironmentIdentifier(
		"CREATE ROLE "+prefix+"_{{ ENV_NAME }}_{{ ENV_NAME }};",
		"prod",
	)
	if err == nil || !strings.Contains(err.Error(), "63") {
		t.Fatalf("renderEnvironmentIdentifier() error = %v, want 63-byte limit error", err)
	}
}

func TestUnitApplyRendersIdentifierButLogsCanonicalSQL(t *testing.T) {
	repo := &variableTestRepository{}
	set := New(repo)
	originalQuery := "CREATE ROLE svc_{{ ENV_NAME }}_queue_api NOLOGIN;"
	mig := NewMigration(originalQuery, "100_queue_api/roles/01_database_access.sql")
	set.Add("queue_api_roles", 100, 1, mig)

	n, lastVersion, err := set.Apply("queue_api_roles", 100, -1, 0, "dev")
	if err != nil {
		t.Fatalf("Set.Apply() error = %v", err)
	}
	if n != 1 || lastVersion != 1 {
		t.Fatalf("Set.Apply() = (%d, %d), want (1, 1)", n, lastVersion)
	}
	if len(repo.execQueries) != 1 || repo.execQueries[0] != "CREATE ROLE svc_dev_queue_api NOLOGIN;" {
		t.Fatalf("executed queries = %q", repo.execQueries)
	}
	if len(repo.logs) != 1 || repo.logs[0].SQL != originalQuery || repo.logs[0].Hash != mig.Hash {
		t.Fatalf("migration logs = %#v, want canonical SQL and source hash", repo.logs)
	}
}

func TestUnitGetSQLRetainsCanonicalIdentifierTemplate(t *testing.T) {
	repo := &variableTestRepository{}
	set := New(repo)
	originalQuery := "CREATE ROLE svc_{{ ENV_NAME }}_queue_api NOLOGIN;"
	set.Add(
		"queue_api_roles",
		100,
		1,
		NewMigration(originalQuery, "100_queue_api/roles/01_database_access.sql"),
	)

	got, err := set.GetSQL("queue_api_roles", 100, -1)
	if err != nil {
		t.Fatalf("Set.GetSQL() error = %v", err)
	}
	if strings.TrimSpace(got) != originalQuery {
		t.Fatalf("Set.GetSQL() = %q, want canonical %q", got, originalQuery)
	}
}

func TestUnitApplyDoesNotRenderIdentifierForSkippedEnvironment(t *testing.T) {
	repo := &variableTestRepository{}
	set := New(repo)
	mig := NewMigration(
		"--- required_env: prod\nCREATE ROLE svc_{{ ENV_NAME }}_queue_api NOLOGIN;",
		"100_queue_api/roles/01_database_access.sql",
	)
	set.Add("queue_api_roles", 100, 1, mig)

	n, _, err := set.Apply("queue_api_roles", 100, -1, 0, "")
	if err != nil {
		t.Fatalf("Set.Apply() error = %v", err)
	}
	if n != 1 || len(repo.execQueries) != 0 {
		t.Fatalf("Set.Apply() applied=%d, queries=%q; want skipped migration", n, repo.execQueries)
	}
}
