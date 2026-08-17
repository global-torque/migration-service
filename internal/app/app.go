package app

import (
	"context"
	"fmt"
	"os"

	"github.com/pkg/errors"
	"github.com/webdevelop-pro/go-common/configurator"
	"github.com/webdevelop-pro/lib/logger"
	"github.com/webdevelop-pro/migration-service/internal/adapters"
	"github.com/webdevelop-pro/migration-service/internal/domain/migration"
)

const pkgName = "migration"

type App struct {
	log  logger.Logger
	repo adapters.Repository
	cfg  *GeneralConfig
	set  *migration.Set
}

func New(c *configurator.Configurator, repo adapters.Repository) *App {
	cfg := &GeneralConfig{}
	l := logger.NewComponentLogger(pkgName, nil)

	if err := configurator.NewConfiguration(cfg); err != nil {
		l.Fatal().Err(err).Msg("failed to get configuration of server")
	}
	return &App{
		log:  l,
		repo: repo,
		cfg:  cfg,
		set:  migration.New(repo),
	}
}

func (a *App) ApplyAll(dir string) error {
	a.set.ClearData()
	err := migration.ReadDir(dir, "", a.set)
	if err != nil {
		a.log.Error().Err(err).Msgf("can't get migration data from directory: %s", dir)
		panic(err)
	}
	n, err := a.set.ApplyAll(false, a.cfg.EnvName)
	if err != nil {
		a.log.Error().Err(err).Msg("failed to apply all migrations")
		return err
	}
	a.log.Info().Int("n", n).Msg("applied migrations")
	return nil
}

func (a *App) Apply(ctx context.Context, serviceName string) (int, error) {
	if serviceName == "" || !a.set.ServiceExists(serviceName) {
		return 0, fmt.Errorf("service '%s' not found", serviceName)
	}

	ver, err := a.repo.GetServiceVersion(ctx, serviceName)
	if err != nil {
		return 0, errors.Wrap(err, "failed to get current service version")
	}

	n, lastVersion, err := a.set.Apply(serviceName, -1, ver, ver, a.cfg.EnvName)
	if err != nil {
		return 0, errors.Wrap(err, "failed to apply migrations")
	}

	if lastVersion > ver {
		if err := a.repo.UpdateServiceVersion(ctx, serviceName, lastVersion); err != nil {
			a.log.Error().Err(err).Msg("failed to bump service version")
		}
	}

	if err != nil {
		return 0, errors.Wrap(err, "failed to encode response")
	}

	return n, nil
}

func (a *App) GetSQL(ctx context.Context, dir string, serviceName string) (sql string, err error) {
	a.set.ClearData()
	err = migration.ReadDir(dir, "", a.set)
	if err != nil {
		a.log.Error().Err(err).Msgf("can't get migration data from directory: %s", dir)
		panic(err)
	}

	if serviceName == "" || !a.set.ServiceExists(serviceName) {
		return "", fmt.Errorf("service '%s' not found", serviceName)
	}

	ver, err := a.repo.GetServiceVersion(ctx, serviceName)
	if err != nil {
		return "", errors.Wrap(err, "failed to get current service version")
	}

	sql, err = a.set.GetSQL(serviceName, -1, ver)
	if err != nil {
		return "", errors.Wrap(err, "failed to get migrations")
	}

	return
}

func (a *App) Init(ctx context.Context) error {
	return a.repo.CreateMigrationTable(ctx)
}

func (a *App) ForceApply(args []string) error {
	a.set.ClearData()
	a.getMigrationDataFromAppArgs(args)
	n, err := a.set.ApplyAll(true, a.cfg.EnvName)
	if err != nil {
		a.log.Error().Err(err).Msg("failed to force apply all migrations")
		return err
	}
	a.log.Info().Int("n", n).Msg("applied migrations")
	return nil
}

func (a *App) FakeApply(args []string) error {
	a.set.ClearData()
	a.getMigrationDataFromAppArgs(args)
	n, err := a.set.FakeAll()
	if err != nil {
		a.log.Error().Err(err).Msg("failed to skip migrations")
		return err
	}
	a.log.Info().Int("n", n).Msg("skipped migrations")
	return nil
}

func (a *App) CheckMigrationHash(args []string) (allEqual bool, list []string, err error) {
	a.set.ClearData()
	a.getMigrationDataFromAppArgs(args)
	allEqual, list, err = a.set.CheckMigrationHash()
	if err != nil {
		a.log.Error().Err(err).Msg("failed to check migrations")
		return
	}

	if allEqual {
		a.log.Info().Msg("all hashes are equal")
	} else {
		str := fmt.Sprintf("%d migrations have differences:", len(list))
		for _, v := range list {
			str += "\n" + v
		}
		a.log.Warn().Msg(str)
	}

	return
}

func (a *App) CheckAndApplyMigrations(args []string) error {
	a.set.ClearData()
	a.getMigrationDataFromAppArgs(args)
	allEqual, list, err := a.set.CheckMigrationHash()
	if err != nil {
		a.log.Error().Err(err).Msg("failed to check migrations while executing CheckAndApplyMigrations")
		return err
	}

	if allEqual {
		a.log.Info().Msg("all hashes are equal")
	} else {
		str := fmt.Sprintf("%d migrations have differences:", len(list))
		for _, v := range list {
			str += "\n" + v
		}
		str += "\nTrying apply migrations"
		a.log.Warn().Msg(str)

		err = a.ForceApply(list)
		if err != nil {
			return err
		}
	}

	return nil
}

func (a *App) getMigrationDataFromAppArgs(args []string) {
	for _, path := range args {
		pathInfo, err := os.Stat(path)
		if err != nil {
			a.log.Error().Err(err).Msgf("can't read info data from path: %s", path)
			panic(err)
		}

		if pathInfo.IsDir() {
			err = migration.ReadDir(path, "", a.set)
		} else {
			err = migration.ReadFile(path, a.set)
		}

		if err != nil {
			a.log.Error().Err(err).Msgf("can't get migration data from path: %s", path)
			panic(err)
		}
	}
}
