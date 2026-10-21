package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/webdevelop-pro/lib/logger"
	"github.com/webdevelop-pro/migration-service/internal/domain/migration"
	"github.com/webdevelop-pro/migration-service/internal/domain/migration_log"
)

type appTestRepository struct {
	priorityVersions map[int]int
	getErrors        map[int]error
	initializeCalls  []initializePriorityCall
	initializeErr    error
	logs             []migration_log.MigrationServicesLog
	queries          []string
	updates          []int
	fakeCalls        []fakeMigrationCall
}

type initializePriorityCall struct {
	serviceName string
	priorities  []int
}

type fakeMigrationCall struct {
	serviceName      string
	aggregateVersion int
	logs             []migration_log.MigrationServicesLog
}

func (r *appTestRepository) GetServiceVersion(_ context.Context, serviceName string, priority int) (int, error) {
	if err := r.getErrors[priority]; err != nil {
		return 0, err
	}
	version := r.priorityVersions[priority]
	for _, log := range r.logs {
		if log.MigrationServiceName == serviceName && log.Priority == priority && log.Version > version {
			version = log.Version
		}
	}
	return version, nil
}

func (r *appTestRepository) ResolveServiceVersions(_ context.Context, serviceName string, priorities []int) (map[int]int, error) {
	versions := make(map[int]int, len(priorities))
	for _, priority := range priorities {
		version := r.priorityVersions[priority]
		for _, log := range r.logs {
			if log.MigrationServiceName == serviceName && log.Priority == priority && log.Version > version {
				version = log.Version
			}
		}
		versions[priority] = version
	}
	return versions, nil
}

func (r *appTestRepository) InitializeServicePriorities(_ context.Context, serviceName string, priorities []int) error {
	r.initializeCalls = append(r.initializeCalls, initializePriorityCall{
		serviceName: serviceName,
		priorities:  append([]int(nil), priorities...),
	})
	return r.initializeErr
}

func (r *appTestRepository) AdvanceServiceVersion(_ context.Context, _ string, _ int, version int) error {
	r.updates = append(r.updates, version)
	return nil
}

func (r *appTestRepository) CreateMigrationTable(context.Context) error {
	return nil
}

func (r *appTestRepository) Exec(_ context.Context, query string, _ ...interface{}) error {
	r.queries = append(r.queries, query)
	return nil
}

func (r *appTestRepository) WriteMigrationServiceLog(_ context.Context, log migration_log.MigrationServicesLog) error {
	r.logs = append(r.logs, log)
	return nil
}

func (r *appTestRepository) FakeMigrationService(
	_ context.Context,
	serviceName string,
	aggregateVersion int,
	logs []migration_log.MigrationServicesLog,
) error {
	r.fakeCalls = append(r.fakeCalls, fakeMigrationCall{
		serviceName:      serviceName,
		aggregateVersion: aggregateVersion,
		logs:             append([]migration_log.MigrationServicesLog(nil), logs...),
	})
	return nil
}

func (r *appTestRepository) GetHashFromMigrationServiceLog(_ context.Context, log migration_log.MigrationServicesLog) (string, error) {
	for _, stored := range r.logs {
		if stored.MigrationServiceName == log.MigrationServiceName && stored.Priority == log.Priority && stored.Version == log.Version && stored.FileName == log.FileName {
			return stored.Hash, nil
		}
	}
	return "", nil
}

func newTestApp(repo *appTestRepository) *App {
	return &App{
		log:  logger.NewLogger("test", "error", io.Discard, nil),
		repo: repo,
		cfg:  &GeneralConfig{EnvName: "dev"},
		set:  migration.New(repo),
	}
}

func TestUnitAppApplyUsesRealPriorityAndIndependentHighWater(t *testing.T) {
	repo := &appTestRepository{
		priorityVersions: map[int]int{16: 3, 17: 2},
		getErrors:        map[int]error{},
		logs: []migration_log.MigrationServicesLog{
			{MigrationServiceName: "service", Priority: 16, Version: 1, FileName: "01.sql"},
			{MigrationServiceName: "service", Priority: 16, Version: 3, FileName: "03.sql"},
			{MigrationServiceName: "service", Priority: 17, Version: 2, FileName: "02.sql"},
		},
	}
	application := newTestApp(repo)
	application.set.Add("service", 16, 1, migration.NewMigration("SELECT 'p16-v1'", "16/01.sql"))
	application.set.Add("service", 16, 3, migration.NewMigration("SELECT 'p16-v3'", "16/03.sql"))
	application.set.Add("service", 17, 2, migration.NewMigration("SELECT 'p17-v2'", "17/02.sql"))
	application.set.Add("service", 17, 3, migration.NewMigration("SELECT 'p17-v3'", "17/03.sql"))

	count, err := application.Apply(context.Background(), "service")
	if err != nil {
		t.Fatalf("App.Apply() error = %v", err)
	}
	if count != 1 || !reflect.DeepEqual(repo.queries, []string{"SELECT 'p17-v3'"}) {
		t.Fatalf("App.Apply() = (%d, %q), want one priority 17 query", count, repo.queries)
	}
	if want := []int{16, 17}; len(repo.initializeCalls) != 1 ||
		repo.initializeCalls[0].serviceName != "service" ||
		!reflect.DeepEqual(repo.initializeCalls[0].priorities, want) {
		t.Fatalf("initialize calls = %#v, want service priorities %v", repo.initializeCalls, want)
	}
	if len(repo.logs) != 4 || repo.logs[3].Priority != 17 || repo.logs[3].Version != 3 {
		t.Fatalf("logs = %#v, want real priority 17 v3 log", repo.logs)
	}

	secondCount, err := application.Apply(context.Background(), "service")
	if err != nil {
		t.Fatalf("second App.Apply() error = %v", err)
	}
	if secondCount != 0 || len(repo.queries) != 1 {
		t.Fatalf("second App.Apply() = (%d, %q), want no reapply", secondCount, repo.queries)
	}
}

func TestUnitAppGetSQLUsesEachPriorityHighWaterInOrder(t *testing.T) {
	repo := &appTestRepository{
		priorityVersions: map[int]int{16: 1, 17: 1},
		getErrors:        map[int]error{},
	}
	application := newTestApp(repo)
	dir := t.TempDir()
	for _, file := range []struct {
		priority int
		name     string
		query    string
	}{
		{priority: 16, name: "01_first.sql", query: "SELECT 'p16-v1'"},
		{priority: 16, name: "02_second.sql", query: "SELECT 'p16-v2'"},
		{priority: 17, name: "01_first.sql", query: "SELECT 'p17-v1'"},
		{priority: 17, name: "02_second.sql", query: "SELECT 'p17-v2'"},
	} {
		serviceDir := filepath.Join(dir, fmt.Sprintf("%d_service", file.priority))
		if err := os.MkdirAll(serviceDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(serviceDir, file.name), []byte(file.query), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	repo.logs = append(repo.logs,
		migration_log.MigrationServicesLog{MigrationServiceName: "service", Priority: 16, Version: 1, FileName: "01.sql"},
		migration_log.MigrationServicesLog{MigrationServiceName: "service", Priority: 17, Version: 1, FileName: "01.sql"},
	)

	got, err := application.GetSQL(context.Background(), dir, "service")
	if err != nil {
		t.Fatalf("App.GetSQL() error = %v", err)
	}
	want := "\nSELECT 'p16-v2';\nSELECT 'p17-v2';"
	if got != want {
		t.Fatalf("App.GetSQL() = %q, want %q", got, want)
	}
}

func TestUnitAppApplyStopsOnPriorityLookupError(t *testing.T) {
	lookupErr := errors.New("priority lookup failed")
	repo := &appTestRepository{
		priorityVersions: map[int]int{},
		getErrors:        map[int]error{16: lookupErr},
	}
	application := newTestApp(repo)
	application.set.Add("service", 16, 1, migration.NewMigration("SELECT 1", "16/01.sql"))
	application.set.Add("service", 17, 1, migration.NewMigration("SELECT 2", "17/01.sql"))

	count, err := application.Apply(context.Background(), "service")
	if err == nil || !errors.Is(err, lookupErr) || count != 0 {
		t.Fatalf("App.Apply() = (%d, %v), want lookup error and zero count", count, err)
	}
	if len(repo.queries) != 0 {
		t.Fatalf("queries after lookup failure = %q, want none", repo.queries)
	}
}

func TestUnitAppApplyStopsOnPriorityInitializationError(t *testing.T) {
	initErr := errors.New("ambiguous legacy progress")
	repo := &appTestRepository{
		priorityVersions: map[int]int{},
		getErrors:        map[int]error{},
		initializeErr:    initErr,
	}
	application := newTestApp(repo)
	application.set.Add("service", 16, 1, migration.NewMigration("SELECT 1", "16/01.sql"))
	application.set.Add("service", 17, 1, migration.NewMigration("SELECT 2", "17/01.sql"))

	count, err := application.Apply(context.Background(), "service")
	if err == nil || !errors.Is(err, initErr) || count != 0 {
		t.Fatalf("App.Apply() = (%d, %v), want initialization error and zero count", count, err)
	}
	if len(repo.queries) != 0 {
		t.Fatalf("queries after initialization failure = %q, want none", repo.queries)
	}
	if want := []int{16, 17}; len(repo.initializeCalls) != 1 ||
		!reflect.DeepEqual(repo.initializeCalls[0].priorities, want) {
		t.Fatalf("initialize calls = %#v, want complete priorities %v", repo.initializeCalls, want)
	}
}

func TestUnitAppFakeApplyBuildsCanonicalServiceBatch(t *testing.T) {
	repo := &appTestRepository{
		priorityVersions: map[int]int{},
		getErrors:        map[int]error{},
	}
	application := newTestApp(repo)
	dir := t.TempDir()
	serviceDir := filepath.Join(dir, "17_service")
	if err := os.MkdirAll(serviceDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "02_second.sql"), []byte("SELECT 2"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serviceDir, "01_first.sql"), []byte("SELECT 1"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := application.FakeApply([]string{dir}); err != nil {
		t.Fatalf("App.FakeApply() error = %v", err)
	}
	if len(repo.fakeCalls) != 1 || len(repo.fakeCalls[0].logs) != 2 {
		t.Fatalf("fake calls = %#v, want one two-file service batch", repo.fakeCalls)
	}
	if repo.fakeCalls[0].aggregateVersion != 2 || repo.fakeCalls[0].logs[0].Version != 1 || repo.fakeCalls[0].logs[1].Version != 2 {
		t.Fatalf("fake batch = %#v, want version order and aggregate 2", repo.fakeCalls[0])
	}
	if len(repo.queries) != 0 {
		t.Fatalf("fake execution queries = %q, want none", repo.queries)
	}
}
