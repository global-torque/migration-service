# Migration Service Enhancement Plan

## Goal

Move `migration-service` onto the latest split `github.com/webdevelop-pro/go-common/*` modules and remove the legacy local `pkg/lib` and `pkg/logger` replacements while keeping the migration behavior covered by repeatable integration tests.

## Steps

1. Establish a Docker Compose test harness.
   - Start a disposable PostgreSQL database.
   - Run the current Go test suite inside the shared Go test image.
   - Use cached Go module/build volumes so repeated runs are practical.
   - Keep compose-only database settings out of the local `.example.env`.

2. Capture the current baseline.
   - Run `docker compose -f docker-compose.yaml run --rm tests`.
   - Fix only test-environment failures in this phase.
   - Record any existing application/test failures before refactoring.

3. Update dependencies one package family at a time.
   - Replace local logger imports with `github.com/webdevelop-pro/go-common/logger`.
   - Replace configuration usage with `github.com/webdevelop-pro/go-common/configurator`.
   - Replace database usage with `github.com/webdevelop-pro/go-common/db`, adapting constructors to context-based APIs.
   - Replace the legacy local HTTP server with `github.com/webdevelop-pro/go-common/server` and route registration.

4. Remove obsolete local modules.
   - Drop `replace github.com/webdevelop-pro/lib => ./pkg/lib`.
   - Drop `replace github.com/webdevelop-pro/go-logger => ./pkg/logger`.
   - Delete or quarantine unused local copies only after imports and tests prove they are dead.

5. Modernize service wiring.
   - Align Fx setup with the latest service pattern.
   - Pass `context.Context` through blocking database operations.
   - Keep CLI modes (`--init`, `--force`, `--fake`, `--check`, `--check-apply`, `--apply-only`) behavior-compatible.

6. Validate after each step.
   - Run `go test -count=1 ./...`.
   - Run the compose test harness.
   - Run `go test -race ./...` when the refactor compiles cleanly.
   - Run `go vet ./...` and `golangci-lint run` if the tool is available.
