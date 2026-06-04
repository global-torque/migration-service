package adapters

import (
	"context"

	"github.com/webdevelop-pro/migration-service/internal/domain/migration_log"
)

// AppliedMigration describes one migration execution and its metadata updates.
type AppliedMigration struct {
	ServiceName   string
	Version       int
	Query         string
	AllowError    bool
	UpdateVersion bool
	Log           migration_log.MigrationServicesLog
}

// ApplyMigrationResult reports what happened while applying a migration.
type ApplyMigrationResult struct {
	QuerySucceeded bool
	QueryError     error
}

type Repository interface {
	GetServiceVersion(ctx context.Context, name string) (int, error)
	UpdateServiceVersion(ctx context.Context, name string, ver int) error
	CreateMigrationTable(ctx context.Context) error
	ApplyMigration(ctx context.Context, migration AppliedMigration) (ApplyMigrationResult, error)
	WriteMigrationServiceLog(ctx context.Context, log migration_log.MigrationServicesLog) error
	GetHashFromMigrationServiceLog(ctx context.Context, log migration_log.MigrationServicesLog) (string, error)
}
