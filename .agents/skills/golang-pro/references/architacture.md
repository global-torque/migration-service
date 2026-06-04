# Go DDD and Hexagonal Architecture

Use this reference when building or reviewing Go services with DDD, hexagonal architecture, CQRS-style handlers, transport ports, and persistence adapters. The pattern is based on `internal/trainer` from `wild-workouts-go-ddd-example`.

## Dependency Rule

Keep dependencies pointing inward:

```text
main/service -> ports -> app -> domain
main/service -> adapters -> domain/app interfaces
```

- `domain/<aggregate>` owns entities, value objects, domain errors, factories, and repository interfaces.
- `app/command` and `app/query` orchestrate use cases through domain contracts and read-model interfaces.
- `ports` translate HTTP/gRPC/OpenAPI/protobuf/auth details into commands and queries.
- `adapters` implement repository and read-model interfaces with Firestore, MySQL, memory, or other external systems.
- `service` wires concrete adapters, factories, loggers, metrics, and handlers into `app.Application`.
- `main.go` selects runtime mode and registers HTTP or gRPC servers.

Do not import ports, adapters, generated API types, SQL DTOs, Firestore DTOs, or protobuf types into `domain` or `app`.

## Package Shape

The trainer service uses this structure:

```text
internal/trainer/
+-- domain/hour/          # Hour aggregate, Availability enum, Factory, Repository contract
+-- app/
|   +-- app.go            # Application container with Commands and Queries
|   +-- command/          # Use cases that mutate state
|   +-- query/            # Use cases/read models that return data
+-- adapters/             # Firestore, MySQL, memory repositories and DTO mapping
+-- ports/                # HTTP and gRPC servers plus generated OpenAPI types
+-- service/              # Dependency wiring
+-- main.go               # Runtime entrypoint
```

This keeps domain rules testable without server startup, generated code, or databases.

## Domain Layer

Model business state and transitions in the aggregate:

- `Hour` keeps fields private and exposes behavior methods such as `MakeAvailable`, `MakeNotAvailable`, `ScheduleTraining`, and `CancelTraining`.
- `Availability` is a struct-backed enum, not `type Availability string`, so arbitrary string values cannot be constructed accidentally.
- Domain errors such as `ErrTrainingScheduled`, `ErrNoTrainingScheduled`, and `ErrHourNotAvailable` live near the rules that produce them.
- `Factory` validates creation rules such as full-hour precision, future date limits, and allowed UTC working hours.
- `UnmarshalHourFromDatabase` is reserved for adapters reconstructing persisted state; do not use it as a normal constructor.

Put repository contracts beside the aggregate they persist:

```go
type Repository interface {
    GetHour(ctx context.Context, hourTime time.Time) (*Hour, error)
    UpdateHour(
        ctx context.Context,
        hourTime time.Time,
        updateFn func(h *Hour) (*Hour, error),
    ) error
}
```

The `UpdateHour` closure lets the repository own locking, transactions, rollback, and retries while the application layer owns the domain mutation.

## Application Layer

Use commands for mutations and queries for reads:

- Command structs are input DTOs, for example `ScheduleTraining{Hour time.Time}` or `MakeHoursAvailable{Hours []time.Time}`.
- Command handlers load/update aggregates through domain repositories and call domain methods. They should not contain transport parsing or SQL/Firestore details.
- Query handlers return application read models. They can use domain repositories for single-aggregate reads or dedicated read-model interfaces for list/reporting views.
- Constructors validate required dependencies and apply decorators for logging and metrics.

The application container groups handlers instead of exposing a broad service type:

```go
type Application struct {
    Commands Commands
    Queries  Queries
}
```

Ports call handlers through `Handle(ctx, commandOrQuery)`, which keeps use cases explicit and easy to decorate.

## Ports

Ports are edge translators:

- HTTP handlers decode OpenAPI request types, check auth/roles, call application handlers, and encode responses.
- gRPC handlers translate protobuf timestamps and return protobuf responses or gRPC status errors.
- Transport-specific validation and error conversion belong here; domain rules do not.
- Keep time normalization obvious at the edge when it is transport-specific, such as `AsTime().UTC().Truncate(time.Hour)` for protobuf timestamps.

Ports should not reach into adapters directly.

## Adapters

Adapters implement interfaces and isolate external schemas:

- Firestore stores `DateModel` documents in `trainer-hours`, uses `RunTransaction` for `UpdateHour`, treats missing documents as existing dates with default not-available hours, and maps legacy boolean DTOs into domain `Availability`.
- MySQL stores one row per hour, uses `SELECT ... FOR UPDATE`, upserts with `ON DUPLICATE KEY UPDATE`, retries deadlocks, and centralizes commit/rollback handling.
- Memory stores aggregate values with an `RWMutex` and returns copies so callers cannot mutate persisted state outside `UpdateHour`.
- Read-model adapters can be separate from aggregate repositories, for example `AvailableHoursReadModel`, and may fill missing dates/hours or sort output for UI needs.

Keep DTO-to-domain mapping in adapters. Do not leak persistence shape into commands, queries, or aggregates.

## Wiring

Use a service-level constructor for concrete dependencies:

- Build external clients from environment/config.
- Create domain factories from validated config.
- Instantiate repositories/read models.
- Create logger and metrics clients.
- Construct command/query handlers and return `app.Application`.

This keeps `main.go` small and lets tests replace adapters without changing ports or handlers.

## Extension Workflow

1. Add or change domain behavior first when a business invariant or state transition changes.
2. Add a command or query DTO and handler for the use case.
3. Define the narrowest repository or read-model interface needed by that handler.
4. Implement adapter behavior transactionally and keep mapping code local to the adapter.
5. Expose the use case through HTTP/gRPC ports with DTO translation only.
6. Wire the handler into `app.Application` and `service.NewApplication`.
7. Add domain unit tests, repository contract tests across adapters when persistence behavior matters, and component tests for auth/transport behavior.

## Avoid

- Putting HTTP, gRPC, SQL, Firestore, or generated DTOs in `domain` or `app`.
- Letting ports call repositories directly.
- Duplicating domain transition rules in handlers or adapters.
- Returning mutable in-memory aggregate pointers that bypass repository update semantics.
- Persisting partial state after an `UpdateHour` closure returns an error.
- Using naked string enums for constrained domain concepts.
- Spreading timezone and precision normalization across many layers without a clear owner.
