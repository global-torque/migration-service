package ports

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/webdevelop-pro/go-common/server"
	"github.com/webdevelop-pro/go-common/server/route"
)

func InitHandlers(srv *server.HTTPServer) {
	srv.AddRoute(&route.Route{
		Method: http.MethodPost,
		Path:   "/liveness",
		Handler: func(c echo.Context) error {
			return c.JSON(http.StatusOK, nil)
		},
	})
	srv.AddRoute(&route.Route{
		Method: http.MethodPost,
		Path:   "/healtchcheck",
		Handler: func(c echo.Context) error {
			return c.JSON(http.StatusOK, nil)
		},
	})
	srv.AddRoute(&route.Route{
		Method: http.MethodPost,
		Path:   "/readiness",
		Handler: func(c echo.Context) error {
			return c.JSON(http.StatusBadRequest, nil)
		},
	})
}
