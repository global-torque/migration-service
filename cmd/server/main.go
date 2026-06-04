package main

import (
	"context"
	"flag"
	"fmt"

	"github.com/webdevelop-pro/go-common/configurator"
	"github.com/webdevelop-pro/go-common/logger"
	"github.com/webdevelop-pro/go-common/server"
	"github.com/webdevelop-pro/migration-service/internal/adapters"
	"github.com/webdevelop-pro/migration-service/internal/adapters/repository/postgres"
	"github.com/webdevelop-pro/migration-service/internal/app"
	"github.com/webdevelop-pro/migration-service/internal/ports"
	"github.com/webdevelop-pro/migration-service/internal/services"
	"go.uber.org/fx"
)

// @schemes https
func main() {
	log := logger.NewComponentLogger(context.Background(), "fx")

	fx.New(
		fx.Logger(&log),
		fx.Provide(
			// Configurator
			configurator.NewConfigurator,
			// Database connection
			postgres.New,
			// Bind DB with Repository interface
			func(repo *postgres.Repository) adapters.Repository { return repo },
			// app
			app.New,
			// Bind App with service interface
			func(mig *app.App) services.Migration { return mig },
		),

		fx.Invoke(
			// Close DB connections
			RegisterRepositoryLifecycle,
			// Run application
			RunApp,
		),
	).Run()

	/*
		if err := a.Start(context.Background()); err != nil {
			log.Fatal().Err(err).Msg("failed")
		}

		a.Done()
	*/

	log.Info().Msg("done")
}

func errorToint(err error) int {
	if err != nil {
		return 1
	}
	return 0
}

func shutdownWithExitCode(sd fx.Shutdowner, err error) {
	if shutdownErr := sd.Shutdown(fx.ExitCode(errorToint(err))); shutdownErr != nil {
		log := logger.NewComponentLogger(context.Background(), "shutdown")
		log.Error().Err(shutdownErr).Msg("failed to shutdown application")
	}
}

func RegisterRepositoryLifecycle(lc fx.Lifecycle, repo *postgres.Repository) {
	lc.Append(
		fx.Hook{
			OnStop: func(ctx context.Context) error {
				repo.Close()
				return nil
			},
		},
	)
}

func RunApp(sd fx.Shutdowner, _app *app.App, c *configurator.Configurator, lc fx.Lifecycle) {
	init := flag.Bool("init", false, "initialize service by creating migration table at DB")
	finalSql := flag.String("final-sql", "", "if provided - program return final SQL for migrations without applying it. Argument = service name")
	force := flag.Bool("force", false, "force apply migration without version checking. Accept files or dir paths. Will not update service version if applied version is lower, then already applied")
	skip := flag.Bool("fake", false, "fake do not apply any migration but mark according migrations in migration_services table as completed")
	check := flag.Bool("check", false, "check verifies if all hashes of migrations are equal to those in migration table. If no - returns list of files with migrations, that have differences. Can accept files or dirs of migrations as arguments")
	checkApply := flag.Bool("check-apply", false, "check-apply compares hashes of all migrations with hashes in DB and try to apply those, that have differences. Can accept files or dirs of migrations as arguments")
	applyOnly := flag.Bool("apply-only", true, "apply and shutdown migration service, do not start web service")
	httpServer := flag.Bool("http", false, "apply migrations and start web service")

	flag.Parse()

	ctx, cancel := context.WithCancel(context.Background())
	lc.Append(fx.Hook{
		OnStop: func(context.Context) error {
			cancel()
			return nil
		},
	})

	if *init {
		RunInit(ctx, sd, _app)
		return
	}
	if *force {
		args := flag.Args()
		RunForceApply(ctx, sd, _app, args)
		return
	}
	if *skip {
		args := flag.Args()
		RunFakeApply(ctx, sd, _app, args)
		return
	}
	if *check {
		args := flag.Args()
		RunCheck(ctx, sd, _app, args, c)
		return
	}
	if *checkApply {
		args := flag.Args()
		RunCheckApply(ctx, sd, _app, args, c)
		return
	}
	if *finalSql != "" {
		GetFinalSQL(ctx, sd, _app, c, *finalSql)
		return
	}

	err := RunMigrations(ctx, _app, c)
	if !shouldRunHTTP(*applyOnly, *httpServer, err) {
		shutdownWithExitCode(sd, err)
		return
	}

	// Run server
	if err := RunHttpServer(lc); err != nil {
		log := logger.NewComponentLogger(context.Background(), "RunHttpServer")
		log.Error().Err(err).Msg("error during http server setup")
		shutdownWithExitCode(sd, err)
	}
}

func shouldRunHTTP(applyOnly, httpServer bool, err error) bool {
	return err == nil && (httpServer || !applyOnly)
}

func RunMigrations(ctx context.Context, _app *app.App, c *configurator.Configurator) error {
	cfg := c.MustNew("migration", &app.Config{}, "migration").(*app.Config)
	err := _app.ApplyAllContext(ctx, cfg.Dir)
	if err != nil {
		log := logger.NewComponentLogger(context.Background(), "RunMigrations")
		log.Error().Err(err).Msg("error during migrations")
	}
	return err
}

func RunHttpServer(lc fx.Lifecycle) error {
	srv, err := server.NewServer()
	if err != nil {
		return fmt.Errorf("create http server: %w", err)
	}
	server.AddDefaultMiddlewares(srv)
	ports.InitHandlers(srv)
	server.StartServer(lc, srv)

	return nil
}

func GetFinalSQL(ctx context.Context, sd fx.Shutdowner, _app *app.App, c *configurator.Configurator, serviceName string) {
	cfg := c.MustNew("migration", &app.Config{}, "migration").(*app.Config)
	sql, err := _app.GetSQL(ctx, cfg.Dir, serviceName)
	if err != nil {
		log := logger.NewComponentLogger(context.Background(), "GetFinalSQL")
		log.Error().Err(err).Msg("error during forming sql for migration")
	}
	fmt.Println(sql)
	shutdownWithExitCode(sd, err)
}

func RunInit(ctx context.Context, sd fx.Shutdowner, _app *app.App) {
	err := _app.Init(ctx)
	log := logger.NewComponentLogger(context.Background(), "RunInit")
	if err != nil {
		log.Error().Err(err).Msg("error during creating migration table")
	}
	log.Info().Msg("successfully initialized")
	shutdownWithExitCode(sd, err)
}

func RunForceApply(ctx context.Context, sd fx.Shutdowner, _app *app.App, args []string) {
	err := _app.ForceApplyContext(ctx, args)
	log := logger.NewComponentLogger(context.Background(), "RunForceApply")
	if err != nil {
		log.Error().Err(err).Msg("error during force apply migrations")
	}
	log.Info().Msg("successfully force applied")
	shutdownWithExitCode(sd, err)
}

func RunFakeApply(ctx context.Context, sd fx.Shutdowner, _app *app.App, args []string) {
	err := _app.FakeApplyContext(ctx, args)
	log := logger.NewComponentLogger(context.Background(), "RunFakeApply")
	if err != nil {
		log.Error().Err(err).Msg("error during skip migrations")
	}
	log.Info().Msg("successfully skipped and marked as finished")
	shutdownWithExitCode(sd, err)
}

func RunCheck(ctx context.Context, sd fx.Shutdowner, _app *app.App, args []string, c *configurator.Configurator) {
	cfg := c.MustNew("migration", &app.Config{}, "migration").(*app.Config)
	if len(args) == 0 {
		args = append(args, cfg.Dir)
	}
	_, _, err := _app.CheckMigrationHashContext(ctx, args)
	log := logger.NewComponentLogger(context.Background(), "RunCheck")
	if err != nil {
		log.Error().Err(err).Msg("error during checking migrations")
	}
	shutdownWithExitCode(sd, err)
}

func RunCheckApply(ctx context.Context, sd fx.Shutdowner, _app *app.App, args []string, c *configurator.Configurator) {
	cfg := c.MustNew("migration", &app.Config{}, "migration").(*app.Config)
	if len(args) == 0 {
		args = append(args, cfg.Dir)
	}
	err := _app.CheckAndApplyMigrationsContext(ctx, args)
	log := logger.NewComponentLogger(context.Background(), "RunCheckApply")
	if err != nil {
		log.Error().Err(err).Msg("error during checking and applying migrations")
	}

	shutdownWithExitCode(sd, err)
}
