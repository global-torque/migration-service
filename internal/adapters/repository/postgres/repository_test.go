package postgres

import (
	"testing"

	"github.com/webdevelop-pro/go-common/configurator"
)

func TestUnitNewDisablesSQLTracing(t *testing.T) {
	t.Setenv("DB_TYPE", "postgres")
	t.Setenv("DB_HOST", "localhost")
	t.Setenv("DB_PORT", "5432")
	t.Setenv("DB_USER", "postgres")
	t.Setenv("DB_PASSWORD", "test-only")
	t.Setenv("DB_DATABASE", "test-only")
	t.Setenv("DB_SSL_MODE", "disable")
	t.Setenv("DB_APP_NAME", "migration-unit-test")
	t.Setenv("DB_LOG_LEVEL", "debug")

	repo := New(configurator.NewConfigurator())
	t.Cleanup(repo.db.Close)

	if tracer := repo.db.Config().ConnConfig.Tracer; tracer != nil {
		t.Fatalf("migration repository tracer = %T, want nil", tracer)
	}
}
