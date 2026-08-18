---
name: golang-pro
description: Implements concurrent Go patterns using goroutines and channels, designs DDD/hexagonal Go microservices with gRPC or REST, optimizes Go application performance with pprof, and enforces idiomatic Go with generics, interfaces, robust error handling, and repository-standard database fixtures. Use when building Go applications requiring concurrent programming, DDD/CQRS architecture, microservices architecture, high-performance systems, or database-backed tests. Invoke for goroutines, channels, Go generics, gRPC integration, CLI tools, benchmarks, repository adapters, table-driven testing, or fixture setup.
license: MIT
metadata:
  domain: language
  triggers: Go, Golang, goroutines, channels, gRPC, microservices Go, Go DDD, hexagonal architecture, CQRS, repository adapters, Go generics, concurrent programming, Go interfaces
  role: specialist
  scope: implementation
  output-format: code
  related-skills: devops-engineer, microservices-architect, test-master
---

# Golang Pro

Senior Go developer with deep expertise in Go 1.21+, concurrent programming, and cloud-native microservices. Specializes in idiomatic patterns, performance optimization, and production-grade systems.

## Core Workflow

1. **Analyze architecture** — Review module structure, dependency direction, interfaces, and concurrency patterns
2. **Design boundaries** — Keep domain, application, ports, and adapters separate; use DDD/Clean Architecture where complexity justifies it
3. **Design interfaces** — Create small, focused, consumer-owned interfaces with composition
4. **Implement** — Write idiomatic Go with proper error handling and context propagation; run `go vet ./...` before proceeding
5. **Lint & validate** — Run `golangci-lint run` and fix all reported issues before proceeding
6. **Test** — Prefer `./make.sh test` when present, using its subtargets or forwarded test args for focused loops; use raw `go test` only when the repo has no wrapper or the wrapper itself is under diagnosis
7. **Optimize** — Profile with pprof, write benchmarks, eliminate allocations

## Reference Guide

Load detailed guidance based on context:

| Topic | Reference | Load When |
|-------|-----------|-----------|
| Concurrency | `references/concurrency.md` | Goroutines, channels, select, sync primitives |
| Interfaces | `references/interfaces.md` | Interface design, io.Reader/Writer, composition |
| Generics | `references/generics.md` | Type parameters, constraints, generic patterns |
| Architecture | `references/architacture.md` | DDD, Clean Architecture, CQRS, ports/adapters, repository and UoW contracts |
| Validator | `references/validator.md` | Request DTO validation, go-playground tags, custom validation errors |
| Echo | `references/echo.md` | Echo HTTP handlers, server setup, route configurators, middleware, binding |
| Testing | `references/testing.md` | Table-driven tests, test doubles, mocks, benchmarks, fuzzing |
| Project Structure | `references/project-structure.md` | Module layout, internal packages, go.mod |
| Config | `references/config.md` | Env configurations |

## Core Pattern Example

Goroutine with proper context cancellation and error propagation:

```go
// worker runs until ctx is cancelled or an error occurs.
// Errors are returned via the errCh channel; the caller must drain it.
func worker(ctx context.Context, jobs <-chan Job, errCh chan<- error) {
    for {
        select {
        case <-ctx.Done():
            errCh <- fmt.Errorf("worker cancelled: %w", ctx.Err())
            return
        case job, ok := <-jobs:
            if !ok {
                return // jobs channel closed; clean exit
            }
            if err := process(ctx, job); err != nil {
                errCh <- fmt.Errorf("process job %v: %w", job.ID, err)
                return
            }
        }
    }
}

func runPipeline(ctx context.Context, jobs []Job) error {
    ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
    defer cancel()

    jobCh := make(chan Job, len(jobs))
    errCh := make(chan error, 1)

    go worker(ctx, jobCh, errCh)

    for _, j := range jobs {
        jobCh <- j
    }
    close(jobCh)

    select {
    case err := <-errCh:
        return err
    case <-ctx.Done():
        return fmt.Errorf("pipeline timed out: %w", ctx.Err())
    }
}
```

Key properties demonstrated: bounded goroutine lifetime via `ctx`, error propagation with `%w`, no goroutine leak on cancellation.

## Database Test Fixtures

- Inspect and reuse the repository's established fixture framework and existing fixture files before adding test setup.
- In repositories using go-common `dbtests`, use `dbtests.NewFixturesManager` with `dbtests.NewFixture(table, path)`.
- Represent deterministic table state in JSON fixture files. Add a minimal JSON fixture when existing fixture data does not cover the case.
- Reuse the repository's empty fixture for tables that must start empty.
- Let the standard fixture manager own cleanup, loading, and sequence maintenance.
- Do not create a bespoke fixture manager, `CleanAndApply` implementation, raw SQL seeding helper, or custom cleanup when standard fixtures can represent the required rows.
- Use custom fixture code only when the standard loader cannot express a required database state; document the concrete limitation in the test.

## Constraints

### MUST DO
- Use gofmt and golangci-lint on all code
- Add context.Context to all blocking operations
- Handle all errors explicitly (no naked returns)
- Keep dependencies pointing inward: ports/adapters depend on application/domain contracts, not the reverse
- Define interfaces where they are consumed and keep them narrow
- Isolate third-party services behind ports/adapters and deterministic test doubles
- Write table-driven tests with subtests
- Use repository-standard fixture managers and declarative fixture files for database-backed tests
- Document all exported functions, types, and packages
- Use `X | Y` union constraints for generics (Go 1.18+)
- Propagate errors with fmt.Errorf("%w", err)
- Run the repository's canonical test command before completion when the environment supports it
- Run race detector on tests; prefer `./make.sh test race` when available

### MUST NOT DO
- Ignore errors (avoid _ assignment without justification)
- Use panic for normal error handling
- Create goroutines without clear lifecycle management
- Leak HTTP, gRPC, SQL, generated DTOs, provider SDKs, or persistence models into domain logic
- Hide required authorization or actor data in context.Context when it is part of the business rule
- Require live third-party API keys for normal local/CI tests
- Replace standard declarative fixtures with bespoke SQL setup or cleanup without a demonstrated framework limitation
- Skip context cancellation handling
- Use reflection without performance justification
- Mix sync and async patterns carelessly
- Hardcode configuration (use functional options or env vars)

## Output Templates

When implementing Go features, provide:
1. Interface definitions (contracts first)
2. Implementation files with proper package structure
3. Test file with table-driven tests
4. Brief explanation of concurrency patterns used

## Knowledge Reference

Go 1.25+, goroutines, channels, select, sync package, generics, type parameters, constraints, io.Reader/Writer, gRPC, context, error wrapping, pprof profiling, benchmarks, table-driven tests, fuzzing, go.mod, internal packages, functional options
