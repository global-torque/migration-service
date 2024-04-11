package migration

import (
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"

	"github.com/pkg/errors"
	"github.com/webdevelop-pro/go-common/logger"
	"github.com/webdevelop-pro/migration-service/internal/adapters"
	"github.com/webdevelop-pro/migration-service/internal/domain/migration_log"
)

// Set is a set of migrations for all services.
type Set struct {
	data map[int]map[string]map[int][]Migration
	repo adapters.Repository
	log  logger.Logger
	sync.Mutex
}

// New returns new instance of Set.
func New(repo adapters.Repository) *Set {
	return &Set{
		data: make(map[int]map[string]map[int][]Migration),
		repo: repo,
		log:  logger.NewComponentLogger("migration", nil),
	}
}

func (s *Set) ClearData() {
	s.data = make(map[int]map[string]map[int][]Migration)
}

// ServiceExists returns true if there are known migrations for service.
func (s *Set) ServiceExists(name string) bool {
	for priority := range s.data {
		if _, exists := s.data[priority][name]; exists {
			return true
		}
	}
	return false
}

// Add adds migration to the set.
func (s *Set) Add(service string, priority, version int, mig Migration) {
	s.Lock()

	priorityService, exists := s.data[priority]
	if !exists {
		priorityService = make(map[string]map[int][]Migration)
	}

	serviceMigrations, exists := priorityService[service]
	if !exists {
		serviceMigrations = make(map[int][]Migration)
	}

	// ToDo
	// Return error if version is already taken, no version duplications
	versionMigrations, exists := serviceMigrations[version]
	if !exists {
		versionMigrations = make([]Migration, 0)
	}

	versionMigrations = append(versionMigrations, mig)
	serviceMigrations[version] = versionMigrations
	priorityService[service] = serviceMigrations
	s.data[priority] = priorityService

	s.Unlock()
}

// Services returns list of services for given priority. If priority is -1, returns services for all priorities.
func (s *Set) services(priority int) []string {
	s.Lock()
	defer s.Unlock()

	services := make([]string, 0)

	switch priority {
	case -1:
		for priority := range s.data {
			for k := range s.data[priority] {
				services = append(services, k)
			}
		}
	default:
		if priorityServices, ok := s.data[priority]; ok {
			keys := make([]string, 0, len(priorityServices))

			/*
				From golang documentation:
					...When iterating over a map with a range loop, the iteration order is not specified and
					is not guaranteed to be the same from one iteration to the next
				This causing [a bug](../../../tests/main_test.go#L404) with subfolders
				So we need to sort services by their name
			*/
			for k := range priorityServices {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			services = append(services, keys...)
		}
	}

	return services
}

// priorities returns list of priorities.
func (s *Set) priorities() []int {
	s.Lock()

	priorities := make([]int, len(s.data))
	i := 0

	for priority := range s.data {
		priorities[i] = priority

		i++
	}

	s.Unlock()

	sort.Ints(priorities)
	return priorities
}

// serviceMigrations returns migrations for specified service with version > minVersion.
func (s *Set) serviceMigrations(name string, priority, minVersion int) map[int][]Migration {
	migrations := make(map[int][]Migration)
	var priorities []int

	switch priority {
	case -1:
		priorities = s.priorities()
	default:
		priorities = []int{priority}
	}

	s.Lock()
	defer s.Unlock()

	for _, priority := range priorities {
		priorityMigrations, exists := s.data[priority]
		if !exists {
			continue
		}

		serviceMigrations, exists := priorityMigrations[name]
		if !exists {
			continue
		}

		for ver, m := range serviceMigrations {
			if ver <= minVersion {
				continue
			}

			if _, exists := migrations[ver]; !exists {
				migrations[ver] = make([]Migration, 0)
			}

			migrations[ver] = append(migrations[ver], m...)
		}
	}
	return migrations
}

// Apply applies migrations for specified service with version > minVersion.
func (s *Set) Apply(name string, priority, minVersion, curVersion int, envName string) (int, int, error) {
	migrations := s.serviceMigrations(name, priority, minVersion)

	var n, lastVersion int
	if len(migrations) == 0 {
		return n, lastVersion, nil
	}

	versions := make([]int, len(migrations))

	i := 0

	for ver := range migrations {
		versions[i] = ver
		i++
	}
	sort.Ints(versions)

	for _, ver := range versions {
		for _, mig := range migrations[ver] {

			var err error
			var regexRes bool
			if mig.EnvRegex != "" {
				doMatch := true
				if mig.EnvRegex[0] == '!' {
					doMatch = false
					mig.EnvRegex = mig.EnvRegex[1:len(mig.EnvRegex)]
				}
				regexRes, err = regexp.MatchString(mig.EnvRegex, envName)
				if regexRes == doMatch && err == nil {
					err = s.repo.Exec(context.Background(), mig.Query)
				} else {
					s.log.Debug().Msgf("do not match selection with required_env: %s and %s", mig.EnvRegex, envName)
					continue
				}
			} else {
				err = s.repo.Exec(context.Background(), mig.Query)
			}

			if err != nil {
				s.log.Error().Msgf("not executed query: \n%s\n for %s, version: %d, file: %s", mig.Query, name, ver, mig.Path)
				if !mig.AllowError {
					return n, lastVersion, errors.Wrapf(err, "migration(%d) query failed: %s, file: %s", ver, mig.Query, mig.Path)
				}
			}

			if curVersion < ver {
				if err = s.repo.UpdateServiceVersion(context.Background(), name, ver); err != nil {
					return n, lastVersion, errors.Wrapf(err, "cannot update migration_services, ver: %d, file: %s", ver, mig.Path)
				}
			}

			sLog := migration_log.MigrationServicesLog{
				MigrationServiceName: name,
				Priority:             priority,
				Version:              ver,
				FileName:             filepath.Base(mig.Path),
				SQL:                  mig.Query,
				Hash:                 mig.Hash,
			}
			if err = s.repo.WriteMigrationServiceLog(context.Background(), sLog); err != nil {
				return n, lastVersion, errors.Wrap(err, "cannot update migration_service_logs")
			}

			s.log.Info().Msgf("executed query \n%s\n for %s, version: %d, file: %s", mig.Query, name, ver, mig.Path)
		}

		lastVersion = ver
		n++
	}

	return n, lastVersion, nil
}

// GetSQL returns SQL statement for specified service with version > minVersion.
func (s *Set) GetSQL(name string, priority int, minVersion int) (sql string, err error) {
	migrations := s.serviceMigrations(name, priority, minVersion)

	if len(migrations) == 0 {
		return
	}

	versions := make([]int, len(migrations))

	i := 0

	for ver := range migrations {
		versions[i] = ver

		i++
	}
	sort.Ints(versions)

	for _, ver := range versions {
		for _, mig := range migrations[ver] {
			sql += "\n" + strings.TrimSpace(mig.Query)
			if sql[len(sql)-1:] != ";" {
				sql += ";"
			}
		}
	}

	return sql, nil
}

// ApplyAll applies all migrations for all services.
func (s *Set) ApplyAll(skipVersionCheck bool, envVersion string) (int, error) {
	var (
		n, ver, minVersion, curVersion int
		err                            error
	)
	lastVersions := make(map[string]int)

	pariorities := s.priorities()
	for _, priority := range pariorities {
		services := s.services(priority)
		for _, service := range services {
			minVersion = -1
			curVersion, err = s.repo.GetServiceVersion(context.Background(), service)

			if err != nil && priority > 0 && service != "migration" {
				s.log.Error().Err(err).Msgf("failed to get service version for %s", service)
				return n, fmt.Errorf("failed to get service version for %s", service)
			}

			if !skipVersionCheck {
				minVersion = curVersion
			}

			num, lastVersion, err := s.Apply(service, priority, minVersion, curVersion, envVersion)
			if err != nil {
				s.log.Error().Err(err).Msgf("failed to apply migrations for %s", service)
				return n, fmt.Errorf("failed to apply migrations for %s", service)
			}

			n += num
			if lastVersion > ver {
				if curLastVersion, ok := lastVersions[service]; !ok || lastVersion > curLastVersion {
					lastVersions[service] = lastVersion
				}
			}
		}
	}

	return n, nil
}

// FakeAll marked all migrations as finished without applying them
func (s *Set) FakeAll() (int, error) {
	servicesWithLastVersion := make(map[string]int)
	n := 0

	for priority := range s.data {
		for name, service := range s.data[priority] {
			for ver := range service {
				if ver >= servicesWithLastVersion[name] {
					servicesWithLastVersion[name] = ver
				}
			}
		}
	}

	for name, version := range servicesWithLastVersion {
		curVersion, err := s.repo.GetServiceVersion(context.Background(), name)
		if err != nil && name != "migration" {
			s.log.Error().Err(err).Msgf("failed to get service version for %s", name)
			return n, fmt.Errorf("failed to get service version for %s", name)
		}

		if curVersion < version {
			if err := s.repo.UpdateServiceVersion(context.Background(), name, version); err != nil {
				return n, errors.Wrapf(err, "cannot update migration_services %s, ver: %d", name, version)
			}
		}
		n++
	}

	return n, nil
}

// CheckMigrationHash verifies if all hashes of migrations are equal to those in migration table
func (s *Set) CheckMigrationHash() (allEqual bool, list []string, err error) {
	var hash string

	allEqual = true

	for priority := range s.data {
		for name, service := range s.data[priority] {
			for ver, migrationList := range service {
				for _, migration := range migrationList {
					sLog := migration_log.MigrationServicesLog{
						MigrationServiceName: name,
						Priority:             priority,
						Version:              ver,
						FileName:             filepath.Base(migration.Path),
					}
					hash, err = s.repo.GetHashFromMigrationServiceLog(context.Background(), sLog)
					if err != nil {
						return false, nil, err
					}
					if hash != migration.Hash {
						allEqual = false
						list = append(list, migration.Path)
					}
				}
			}
		}
	}

	return
}
