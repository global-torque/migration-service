package adapters

import (
	"context"

	"github.com/webdevelop-pro/migration-service/internal/domain/migration_log"
)

type Repository interface {
	GetServiceVersion(ctx context.Context, name string, priority int) (int, error)
	ResolveServiceVersions(ctx context.Context, name string, priorities []int) (map[int]int, error)
	InitializeServicePriorities(ctx context.Context, name string, priorities []int) error
	AdvanceServiceVersion(ctx context.Context, name string, priority, version int) error
	CreateMigrationTable(ctx context.Context) error
	Exec(ctx context.Context, sql string, arguments ...interface{}) error
	WriteMigrationServiceLog(ctx context.Context, log migration_log.MigrationServicesLog) error
	FakeMigrationService(ctx context.Context, serviceName string, aggregateVersion int, logs []migration_log.MigrationServicesLog) error
	GetHashFromMigrationServiceLog(ctx context.Context, log migration_log.MigrationServicesLog) (string, error)
}
