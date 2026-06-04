# Data Validation With go-common/validator

Use `github.com/webdevelop-pro/go-common/validator` for request DTOs and boundary data. It wraps `github.com/go-playground/validator/v10` and returns this repo's `response.Error` shape, so callers get field-keyed JSON errors instead of raw validator messages.

## Local Wrapper

Current local behavior:

- `validator.New()` creates one `*validator.Validator` with an internal `*validator.Validate`.
- It registers `ParamName`, so validation errors use `json`, then `param`, then `form` tags as field names. A `json:"-"` field is omitted from the reported name.
- It registers the repo-specific `path` tag. The current rule accepts letters, digits, `/`, `_`, and `-`.
- `Validate(i)` validates with HTTP 400 Bad Request.
- `Verify(i, status)` validates with a custom HTTP status.
- Failures are returned as `*response.Error` with `Message` shaped like `map[string][]string`.

Prefer one validator instance per Echo server or validation boundary. Register all custom rules during construction, before requests are validated.

## DTO Pattern

Put validation tags on input DTOs, not domain entities, unless the type is explicitly an input contract.

```go
type CreateUserRequest struct {
    Email  string `json:"email" validate:"required,email"`
    Status string `json:"status" validate:"required,oneof=active invited disabled"`
    Limit  int    `query:"limit" json:"limit" validate:"omitempty,gte=1,lte=100"`
}

func (h *Handler) createUser(c echo.Context) error {
    var req CreateUserRequest
    if err := c.Bind(&req); err != nil {
        return server.ErrorBadRequestResponse(c, err)
    }
    if err := c.Validate(&req); err != nil {
        return err
    }

    return h.app.Create(c.Request().Context(), req)
}
```

Echo binds query fields from `query` tags, but the local validator field-name function does not read `query`. Add a matching `json` tag when a query-only field should still produce a stable response key.

## Tag Selection

Use validator tags for data shape and simple invariants:

- Required values: `required`
- Optional values with constraints: `omitempty,email`, `omitempty,gte=1,lte=100`
- Strings and slices: `min`, `max`, `len`, `oneof`
- Numeric bounds: `gt`, `gte`, `lt`, `lte`
- Formats: `email`, `uuid`, `uuid4`, `e164`, `url`, `uri`, `datetime`
- Collections: `dive`, plus `keys`/`endkeys` for map keys
- Cross-field checks: `eqfield`, `nefield`, `gtfield`, `required_with`, `required_without`
- Repo-specific path values: `path`

Use service-layer code for business rules that need database state, authorization, time windows, or external calls.

## Optional And Zero Values

`required` rejects the type's zero value. Use pointers when presence matters but zero is valid.

```go
type UpdateQuotaRequest struct {
    Limit *int `json:"limit" validate:"required,gte=0"`
}
```

Use `omitempty` when the field may be absent or zero, but must satisfy rules when provided.

## Nested Values

Validate nested structs by tagging the parent and child fields. For slices and maps, use `dive`.

```go
type MemberRequest struct {
    Email string `json:"email" validate:"required,email"`
}

type TeamRequest struct {
    Name    string          `json:"name" validate:"required,min=2,max=80"`
    Members []MemberRequest `json:"members" validate:"required,min=1,dive"`
}
```

The upstream library recommends `validator.New(validator.WithRequiredStructEnabled())` for v11-compatible required-struct behavior. The local wrapper currently calls `validator.New()` without that option, so do not assume nested struct `required` semantics changed unless `validator.New()` is updated and covered with tests.

## Custom Rules

For repo-wide validation tags, add the rule in `validator.New()` and test both success and failure cases.

```go
func New() *Validator {
    v := valid.New()
    v.RegisterTagNameFunc(ParamName)

    if err := v.RegisterValidation("path", isPath); err != nil {
        panic(err)
    }

    return &Validator{validator: v}
}
```

Do not register validators from handlers. Upstream registration APIs are intended to run before validation starts, and mutating shared validator configuration at request time risks races and inconsistent behavior.

## Errors

When `Validate` fails, assert or unwrap as `*response.Error`:

```go
err := validator.New().Validate(input)
if err != nil {
    var respErr *response.Error
    if errors.As(err, &respErr) {
        return respErr.Message.Map()
    }
}
```

The local wrapper has custom readable messages for `required`, `email`, `len`, `min`, `max`, `gt`, `gte`, `oneof`, `eq`, `ssn`, `dirpath`, and `path`. Other tags fall back to the upstream field error string, so add a `beautifulMsg` case when exposing a new tag to end users.

## Tests

Use table-driven tests against the public wrapper and assert `response.Error.Message.Map()`.

```go
func TestCreateUserValidation(t *testing.T) {
    validate := validator.New()

    tests := []struct {
        name string
        in   CreateUserRequest
        want map[string][]string
    }{
        {
            name: "email required",
            in:   CreateUserRequest{Status: "active"},
            want: map[string][]string{"email": {"missing data for required field"}},
        },
    }

    for _, tt := range tests {
        t.Run(tt.name, func(t *testing.T) {
            err := validate.Validate(tt.in)

            var respErr *response.Error
            if !errors.As(err, &respErr) {
                t.Fatalf("expected response error, got %T", err)
            }
            assert.Equal(t, tt.want, respErr.Message.Map())
        })
    }
}
```

Sources:

- https://pkg.go.dev/github.com/go-playground/validator/v10
- https://github.com/go-playground/validator
- `validator/validate.go`
- `validator/custom_tags.go`
- `validator/validate_test.go`
