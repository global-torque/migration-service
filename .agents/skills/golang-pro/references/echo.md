# Echo HTTP Servers With go-common/server

Use `github.com/webdevelop-pro/go-common/server` for Echo services in this repo. The package standardizes Echo v4.15.2 setup, route registration, middleware order, validation, logging, metrics, and error responses.

## Server Construction

`server.NewServer()` is the default constructor:

- Loads `server.Config` through `configurator`.
- Requires `CORS_ALLOWED_ORIGINS`; startup fails when it is empty.
- Creates `echo.New()`.
- Installs CORS before the default middleware set.
- Adds `/healthcheck` unless `HTTP_HEALTHCHECK=false`.
- Sets `e.Validator = validator.New()`, so `c.Validate(&dto)` uses the go-common validator wrapper.
- Disables Echo's banner, port log, and native logger.
- Sets `Echo.HTTPErrorHandler` to the repo handler.

Use `server.InitAndRun()` in Fx applications. It provides the server, adds default middleware, registers grouped routes, and starts/stops the `http.Server` through `fx.Lifecycle`.

```go
func main() {
    fx.New(
        server.NewHandlerGroups(NewUserHandler),
        server.InitAndRun(),
    ).Run()
}
```

Echo itself warns not to mutate the `Echo` instance or add routes after the server has started. Register middleware and routes during construction.

## Routes

Expose route groups by implementing `route.Configurator`.

```go
type UserHandler struct {
    app UserApp
}

func NewUserHandler(app UserApp) *UserHandler {
    return &UserHandler{app: app}
}

func (h *UserHandler) GetRoutes() []route.Route {
    return []route.Route{
        {
            Method:  http.MethodPost,
            Path:    "/v1/users",
            Handler: h.createUser,
        },
        {
            Method:      http.MethodDelete,
            Path:        "/v1/users/:id",
            Handler:     h.deleteUser,
            Middlewares: []echo.MiddlewareFunc{requireAdmin},
        },
    }
}
```

`HTTPServer.InitRoutes` iterates `GetRoutes()` and calls `Echo.Add(method, path, handler, middlewares...)`. Keep path ownership inside the handler group and keep middleware route-specific when it is not a global concern.

## Handler Pattern

Bind, validate, call the app/service with the request context, and return a repo error type or JSON response.

```go
type CreateUserRequest struct {
    Email string `json:"email" validate:"required,email"`
}

func (h *UserHandler) createUser(c echo.Context) error {
    var req CreateUserRequest
    if err := c.Bind(&req); err != nil {
        return server.ErrorBadRequestResponse(c, err)
    }
    if err := c.Validate(&req); err != nil {
        return err
    }

    user, err := h.app.Create(c.Request().Context(), req)
    if err != nil {
        return err
    }

    return c.JSON(http.StatusCreated, user)
}
```

Echo's default binder binds path params first, then query params only for `GET`, `DELETE`, and `HEAD`, then the request body. Use the matching tags:

- Path: `param:"id"`
- Query: `query:"limit"`
- JSON body: `json:"email"`
- Form body: `form:"email"`

Always validate after binding. Binding parses transport data; validation decides whether the resulting DTO is acceptable.

## Error Responses

The central `HTTPErrorHandler` handles:

- `*response.Error`: writes `StatusCode` and `Message`.
- `*echo.HTTPError`: converts Echo's code/message into `response.ErrorMessages`.
- Unknown errors: writes HTTP 500 with the standard status text.

Prefer returning `*response.Error` from application code when the client should receive a specific status and message. Avoid returning bare `fmt.Errorf` from handlers for expected client errors.

Use the helper functions intentionally:

- `server.ErrorBadRequestResponse(c, err)` normalizes bind/decode errors as HTTP 400.
- `server.ErrorResponse(c, err)` writes a `response.Error` immediately, but returns HTTP 501 for non-`response.Error` inputs.

For ordinary Echo flow, returning errors to the central handler is simpler than writing the response manually.

## Default Middleware

`AddDefaultMiddlewares` installs the shared request pipeline:

- `BodyLimit`, default `20M`, configurable with `HTTP_BODY_LIMIT`.
- `middleware.SetIPAddress`
- `middleware.SetRequestTime`
- `RequestIDWithConfig`, storing `X-Request-Id` in Echo and `context.Context`.
- `middleware.SetLogger`, after request context enrichment.
- Prometheus middleware and `/metrics`, unless `HTTP_PROMETHEUS=false`.
- `BodyDump`, unless `HTTP_BODY_DUMP=false`, with the repo skipper.
- `RequestLogger`, only when `HTTP_REQUEST_LOGGER=true`.
- `Recover`, unless `HTTP_REQUEST_RECOVER=false`.

CORS is configured in `NewServer()` before this middleware set. The local `originAllowlist` performs exact matches from comma-separated `CORS_ALLOWED_ORIGINS`; do not rely on wildcard behavior unless the implementation changes.

## Context And Logging

Use `c.Request().Context()` for app calls. The server middleware enriches the context with request ID, IP address, request creation time, and logger data. Use the repo logger helpers from context instead of Echo's disabled native logger.

```go
ctx := c.Request().Context()
log := logger.FromCtx(ctx, "users")
log.Info().Msg("creating user")
```

## Startup And Shutdown

`StartServer` listens on `Config.Host:Config.Port`, builds an `http.Server` with read, read-header, write, and idle timeouts, and shuts down through the Fx stop hook. Keep long-running request work context-aware so shutdown can drain cleanly.

Important config fields:

- `HOST`
- `PORT`
- `CORS_ALLOWED_ORIGINS`
- `READ_TIMEOUT_SECONDS`
- `READ_HEADER_TIMEOUT_SECONDS`
- `WRITE_TIMEOUT_SECONDS`
- `IDLE_TIMEOUT_SECONDS`
- `STARTUP_GRACE_MILLISECONDS`

## Tests

For handler tests, create an Echo instance, install the same validator, create a request/recorder, and call the handler.

```go
func TestCreateUser(t *testing.T) {
    e := echo.New()
    e.Validator = validator.New()

    body := strings.NewReader(`{"email":"bad"}`)
    req := httptest.NewRequest(http.MethodPost, "/v1/users", body)
    req.Header.Set(echo.HeaderContentType, echo.MIMEApplicationJSON)
    rec := httptest.NewRecorder()
    c := e.NewContext(req, rec)

    err := handler.createUser(c)

    var respErr *response.Error
    require.ErrorAs(t, err, &respErr)
    assert.Equal(t, http.StatusBadRequest, respErr.StatusCode)
    assert.Equal(t, map[string][]string{
        "email": {"not a valid email address"},
    }, respErr.Message.Map())
}
```

When testing returned errors instead of immediate response helpers, pass the error into `e.HTTPErrorHandler` or instantiate `HTTPServer` so the repo handler is active.

Sources:

- https://github.com/labstack/echo/tree/v4.15.2
- https://github.com/labstack/echo/blob/v4.15.2/echo.go
- https://github.com/labstack/echo/blob/v4.15.2/context.go
- https://github.com/labstack/echo/blob/v4.15.2/bind.go
- `server/http.go`
- `server/error_handler.go`
- `server/route/route.go`
