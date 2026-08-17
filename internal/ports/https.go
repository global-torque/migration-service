package ports

import (
	"net/http"

	"github.com/labstack/echo/v4"
	"github.com/webdevelop-pro/lib/logger"
	"github.com/webdevelop-pro/lib/server"
)

type HttpServer struct {
	log logger.Logger
}

func NewHttpServer() HttpServer {
	return HttpServer{
		log: logger.NewComponentLogger("api_handler", nil),
	}
}

func InitHandlers(srv *server.HttpServer) {
	srv.AddRoute(server.Route{
		Method: http.MethodPost,
		Path:   "/liveness",
		Handle: func(c echo.Context) error {
			return c.JSON(http.StatusOK, nil)
		},
	})
	srv.AddRoute(server.Route{
		Method: http.MethodPost,
		Path:   "/healtchcheck",
		Handle: func(c echo.Context) error {
			return c.JSON(http.StatusOK, nil)
		},
	})
	srv.AddRoute(server.Route{
		Method: http.MethodPost,
		Path:   "/readiness",
		Handle: func(c echo.Context) error {
			return c.JSON(http.StatusBadRequest, nil)
		},
	})
}
