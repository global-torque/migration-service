package adapters

import (
	"context"

	"github.com/webdevelop-pro/migration-service/internal/domain/migration_log"
)

type Repository interface {
	GetServiceVersion(ctx context.Context, name string) (int, error)
	UpdateServiceVersion(ctx context.Context, name string, ver int) error
	CreateMigrationTable(ctx context.Context) error
	Exec(ctx context.Context, sql string, arguments ...interface{}) error
	WriteMigrationServiceLog(ctx context.Context, log migration_log.MigrationServicesLog) error
	GetHashFromMigrationServiceLog(ctx context.Context, log migration_log.MigrationServicesLog) (string, error)
}
