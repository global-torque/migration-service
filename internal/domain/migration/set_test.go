package migration

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"testing"

	"github.com/webdevelop-pro/migration-service/internal/domain/migration_log"
)

type setTestRepository struct {
	versions        map[int]int
	versionErrors   map[int]error
	updated         []int
	advanceCalls    []advanceCall
	execQueries     []string
	execErr         error
	logs            []migration_log.MigrationServicesLog
	logErr          error
	hashes          map[string]string
	fakeCalls       []fakeMigrationCall
	fakeErrors      map[string]error
	initializeCalls []initializePriorityCall
	initializeErr   error
	createCalls     int
}

type fakeMigrationCall struct {
	serviceName      string
	aggregateVersion int
	logs             []migration_log.MigrationServicesLog
}

type initializePriorityCall struct {
	serviceName string
	priorities  []int
}

type advanceCall struct {
	priority int
	version  int
}

func (r *setTestRepository) GetServiceVersion(_ context.Context, _ string, priority int) (int, error) {
	if err := r.versionErrors[priority]; err != nil {
		return 0, err
	}
	if version, ok := r.versions[priority]; ok {
		return version, nil
	}
	return 0, nil
}

func (r *setTestRepository) ResolveServiceVersions(_ context.Context, _ string, priorities []int) (map[int]int, error) {
	versions := make(map[int]int, len(priorities))
	for _, priority := range priorities {
		versions[priority] = r.versions[priority]
	}
	return versions, nil
}

func (r *setTestRepository) InitializeServicePriorities(_ context.Context, serviceName string, priorities []int) error {
	r.initializeCalls = append(r.initializeCalls, initializePriorityCall{
		serviceName: serviceName,
		priorities:  append([]int(nil), priorities...),
	})
	return r.initializeErr
}

func (r *setTestRepository) AdvanceServiceVersion(_ context.Context, _ string, priority, version int) error {
	r.advanceCalls = append(r.advanceCalls, advanceCall{priority: priority, version: version})
	if version > r.versions[priority] {
		r.updated = append(r.updated, version)
		r.versions[priority] = version
	}
	return nil
}

func (r *setTestRepository) CreateMigrationTable(context.Context) error {
	r.createCalls++
	return nil
}

func (r *setTestRepository) Exec(_ context.Context, query string, _ ...interface{}) error {
	r.execQueries = append(r.execQueries, query)
	return r.execErr
}

func (r *setTestRepository) WriteMigrationServiceLog(_ context.Context, log migration_log.MigrationServicesLog) error {
	if r.logErr != nil {
		return r.logErr
	}
	r.logs = append(r.logs, log)
	return nil
}

func (r *setTestRepository) FakeMigrationService(
	_ context.Context,
	serviceName string,
	aggregateVersion int,
	logs []migration_log.MigrationServicesLog,
) error {
	call := fakeMigrationCall{
		serviceName:      serviceName,
		aggregateVersion: aggregateVersion,
		logs:             append([]migration_log.MigrationServicesLog(nil), logs...),
	}
	r.fakeCalls = append(r.fakeCalls, call)
	if err := r.fakeErrors[serviceName]; err != nil {
		return err
	}
	return nil
}

func (r *setTestRepository) GetHashFromMigrationServiceLog(_ context.Context, log migration_log.MigrationServicesLog) (string, error) {
	return r.hashes[logKey(log)], nil
}

func logKey(log migration_log.MigrationServicesLog) string {
	return fmt.Sprintf("%s/%d/%d/%s", log.MigrationServiceName, log.Priority, log.Version, log.FileName)
}

func addTestMigration(set *Set, service string, priority, version int, path, query string) Migration {
	migration := NewMigration(query, path)
	set.Add(service, priority, version, migration)
	return migration
}

func TestUnitApplyAllUsesIndependentPriorityHighWater(t *testing.T) {
	repo := &setTestRepository{
		versions:      map[int]int{16: 3, 17: 2},
		versionErrors: map[int]error{},
		hashes:        map[string]string{},
		fakeErrors:    map[string]error{},
	}
	set := New(repo)
	addTestMigration(set, "user_user_invitations", 16, 1, "16/01.sql", "SELECT 'priority16-v1'")
	addTestMigration(set, "user_user_invitations", 16, 3, "16/03.sql", "SELECT 'priority16-v3'")
	addTestMigration(set, "user_user_invitations", 17, 2, "17/02.sql", "SELECT 'priority17-v2'")
	priority17v3 := addTestMigration(set, "user_user_invitations", 17, 3, "17/03.sql", "SELECT 'priority17-v3'")

	n, err := set.ApplyAll(false, "dev")
	if err != nil {
		t.Fatalf("Set.ApplyAll() error = %v", err)
	}
	if n != 1 {
		t.Fatalf("Set.ApplyAll() count = %d, want 1", n)
	}
	if want := []int{16, 17}; len(repo.initializeCalls) != 1 ||
		repo.initializeCalls[0].serviceName != "user_user_invitations" ||
		!reflect.DeepEqual(repo.initializeCalls[0].priorities, want) {
		t.Fatalf("initialize calls = %#v, want complete priorities %v", repo.initializeCalls, want)
	}
	if !reflect.DeepEqual(repo.execQueries, []string{"SELECT 'priority17-v3'"}) {
		t.Fatalf("executed queries = %q, want only priority 17 version 3", repo.execQueries)
	}
	if len(repo.logs) != 1 || repo.logs[0].Priority != 17 || repo.logs[0].Version != 3 || repo.logs[0].Hash != priority17v3.Hash {
		t.Fatalf("logs = %#v, want priority 17 version 3", repo.logs)
	}
}

func TestUnitApplyAllStopsOnPriorityInitializationError(t *testing.T) {
	initErr := errors.New("ambiguous legacy progress")
	repo := &setTestRepository{
		versions:      map[int]int{},
		versionErrors: map[int]error{},
		hashes:        map[string]string{},
		fakeErrors:    map[string]error{},
		initializeErr: initErr,
	}
	set := New(repo)
	addTestMigration(set, "service", 16, 1, "16/01.sql", "SELECT 1")
	addTestMigration(set, "service", 17, 1, "17/01.sql", "SELECT 2")

	n, err := set.ApplyAll(false, "dev")
	if err == nil || !errors.Is(err, initErr) || n != 0 {
		t.Fatalf("Set.ApplyAll() = (%d, %v), want initialization error and zero count", n, err)
	}
	if len(repo.execQueries) != 0 {
		t.Fatalf("queries after initialization failure = %q, want none", repo.execQueries)
	}
	if want := []int{16, 17}; len(repo.initializeCalls) != 1 ||
		!reflect.DeepEqual(repo.initializeCalls[0].priorities, want) {
		t.Fatalf("initialize calls = %#v, want complete priorities %v", repo.initializeCalls, want)
	}
}

func TestUnitCheckReportsOnlyRemainingPriorityDrift(t *testing.T) {
	repo := &setTestRepository{
		versions:      map[int]int{17: 2},
		versionErrors: map[int]error{},
		hashes:        map[string]string{},
		fakeErrors:    map[string]error{},
	}
	set := New(repo)
	v2 := addTestMigration(set, "user_user_invitations", 17, 2, "17/02.sql", "SELECT 'priority17-v2'")
	v3 := addTestMigration(set, "user_user_invitations", 17, 3, "17/03.sql", "SELECT 'priority17-v3'")
	repo.hashes[logKey(migration_log.MigrationServicesLog{
		MigrationServiceName: "user_user_invitations",
		Priority:             17,
		Version:              2,
		FileName:             "02.sql",
	})] = "drifted"

	if _, err := set.ApplyAll(false, "dev"); err != nil {
		t.Fatalf("Set.ApplyAll() error = %v", err)
	}
	repo.hashes[logKey(repo.logs[0])] = v3.Hash
	// The simulated existing v2 log is drifted, while the newly applied v3 is canonical.
	repo.hashes[logKey(migration_log.MigrationServicesLog{
		MigrationServiceName: "user_user_invitations",
		Priority:             17,
		Version:              2,
		FileName:             "02.sql",
	})] = v2.Hash + "-drift"

	allEqual, paths, err := set.CheckMigrationHash()
	if err != nil {
		t.Fatalf("Set.CheckMigrationHash() error = %v", err)
	}
	if allEqual || !reflect.DeepEqual(paths, []string{"17/02.sql"}) {
		t.Fatalf("hash check = (%t, %v), want only v2 drift", allEqual, paths)
	}
}

func TestUnitApplyDoesNotAutoApplyGapsOrPartialSameVersion(t *testing.T) {
	repo := &setTestRepository{
		versions:      map[int]int{17: 3},
		versionErrors: map[int]error{},
		hashes:        map[string]string{},
		fakeErrors:    map[string]error{},
	}
	set := New(repo)
	v1 := addTestMigration(set, "service", 17, 1, "17/01.sql", "SELECT 1")
	v2a := addTestMigration(set, "service", 17, 2, "17/02a.sql", "SELECT 2")
	addTestMigration(set, "service", 17, 2, "17/02b.sql", "SELECT 22")
	v3 := addTestMigration(set, "service", 17, 3, "17/03.sql", "SELECT 3")

	n, err := set.ApplyAll(false, "dev")
	if err != nil {
		t.Fatalf("Set.ApplyAll() error = %v", err)
	}
	if n != 0 || len(repo.execQueries) != 0 {
		t.Fatalf("apply = (%d, %q), want no automatic replay", n, repo.execQueries)
	}
	repo.hashes[logKey(migration_log.MigrationServicesLog{MigrationServiceName: "service", Priority: 17, Version: 1, FileName: "01.sql"})] = v1.Hash
	repo.hashes[logKey(migration_log.MigrationServicesLog{MigrationServiceName: "service", Priority: 17, Version: 2, FileName: "02a.sql"})] = v2a.Hash
	repo.hashes[logKey(migration_log.MigrationServicesLog{MigrationServiceName: "service", Priority: 17, Version: 3, FileName: "03.sql"})] = v3.Hash

	allEqual, paths, err := set.CheckMigrationHash()
	if err != nil {
		t.Fatalf("Set.CheckMigrationHash() error = %v", err)
	}
	if allEqual || !reflect.DeepEqual(paths, []string{"17/02b.sql"}) {
		t.Fatalf("hash check = (%t, %v), want missing same-version file", allEqual, paths)
	}
}

func TestUnitForceApplyKeepsAggregateMonotonic(t *testing.T) {
	repo := &setTestRepository{
		versions:      map[int]int{17: 14},
		versionErrors: map[int]error{},
		hashes:        map[string]string{},
		fakeErrors:    map[string]error{},
	}
	set := New(repo)
	addTestMigration(set, "service", 17, 3, "17/03.sql", "SELECT 3")

	n, lastVersion, err := set.Apply("service", 17, -1, 14, "dev")
	if err != nil {
		t.Fatalf("Set.Apply() error = %v", err)
	}
	if n != 1 || lastVersion != 3 {
		t.Fatalf("Set.Apply() = (%d, %d), want (1, 3)", n, lastVersion)
	}
	if len(repo.updated) != 0 {
		t.Fatalf("aggregate updates = %v, want none below existing high-water", repo.updated)
	}
	if want := []advanceCall{{priority: 17, version: 3}}; !reflect.DeepEqual(repo.advanceCalls, want) {
		t.Fatalf("advance calls = %#v, want %v", repo.advanceCalls, want)
	}
	if want := []string{"SELECT 3"}; !reflect.DeepEqual(repo.execQueries, want) {
		t.Fatalf("force queries = %q, want %q", repo.execQueries, want)
	}
	if len(repo.logs) != 1 || repo.logs[0].Priority != 17 || repo.logs[0].Version != 3 {
		t.Fatalf("force logs = %#v, want priority 17 version 3", repo.logs)
	}
}

func TestUnitNormalApplyLeavesAggregateAdvancedWhenLogWriteFails(t *testing.T) {
	logErr := errors.New("log write failed")
	repo := &setTestRepository{
		versions:      map[int]int{17: 0},
		versionErrors: map[int]error{},
		hashes:        map[string]string{},
		fakeErrors:    map[string]error{},
		logErr:        logErr,
	}
	set := New(repo)
	addTestMigration(set, "service", 17, 1, "17/01.sql", "SELECT 1")

	_, _, err := set.Apply("service", 17, -1, 0, "dev")
	if err == nil || !errors.Is(err, logErr) {
		t.Fatalf("Set.Apply() error = %v, want log failure", err)
	}
	if !reflect.DeepEqual(repo.updated, []int{1}) || len(repo.logs) != 0 {
		t.Fatalf("post-failure state updates=%v logs=%v, want aggregate before absent log", repo.updated, repo.logs)
	}
}

func TestUnitFakeAllIsDeterministicAndCountsCommittedServices(t *testing.T) {
	repo := &setTestRepository{
		versions:      map[int]int{},
		versionErrors: map[int]error{},
		hashes:        map[string]string{},
		fakeErrors:    map[string]error{"service_b": errors.New("service b failed")},
	}
	set := New(repo)
	addTestMigration(set, "service_b", 17, 2, "17/02.sql", "SELECT 'b'")
	addTestMigration(set, "service_a", 17, 2, "17/02.sql", "SELECT 'a2'")
	addTestMigration(set, "service_a", 16, 1, "16/01.sql", "SELECT 'a1'")

	n, err := set.FakeAll()
	if err == nil || n != 1 {
		t.Fatalf("Set.FakeAll() = (%d, %v), want one committed service and service b error", n, err)
	}
	if len(repo.fakeCalls) != 2 || repo.fakeCalls[0].serviceName != "service_a" || repo.fakeCalls[1].serviceName != "service_b" {
		t.Fatalf("fake calls = %#v, want sorted service order", repo.fakeCalls)
	}
	if repo.fakeCalls[0].aggregateVersion != 2 || len(repo.fakeCalls[0].logs) != 2 || repo.fakeCalls[0].logs[0].Priority != 16 || repo.fakeCalls[0].logs[1].Priority != 17 {
		t.Fatalf("service_a fake batch = %#v, want sorted priority logs and max aggregate", repo.fakeCalls[0])
	}
	if len(repo.execQueries) != 0 {
		t.Fatalf("fake execution queries = %q, want none", repo.execQueries)
	}
}
