package httputil

import (
	"errors"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/zagnu-labs/rcs-messenger/internal/apperr"
	"github.com/zagnu-labs/rcs-messenger/internal/logg"
)

// Response is a wire contract: lp-pwa, the admin portal and the mobile apps
// read success/data/error. Message is additive and omitempty.
type Response struct {
	Success bool        `json:"success"`
	Message string      `json:"message,omitempty"`
	Data    interface{} `json:"data,omitempty"`
	Error   string      `json:"error,omitempty"`
}

func OK(c *gin.Context, msg string, data interface{}) {
	c.JSON(http.StatusOK, Response{Success: true, Message: msg, Data: data})
}

func Created(c *gin.Context, msg string, data interface{}) {
	c.JSON(http.StatusCreated, Response{Success: true, Message: msg, Data: data})
}

func Success(c *gin.Context, data interface{}) {
	c.JSON(http.StatusOK, Response{Success: true, Data: data})
}

// Error renders an *apperr.AppError at its own status. Anything else is logged
// and reported as a bare 500 — internal error strings must not reach a client.
func Error(c *gin.Context, err error) {
	var appErr *apperr.AppError
	if errors.As(err, &appErr) {
		c.JSON(appErr.HTTPCode, Response{Success: false, Error: appErr.Message})
		return
	}

	logg.Error().Err(err).
		Str("path", c.FullPath()).
		Str("method", c.Request.Method).
		Msg("unhandled error")
	c.JSON(http.StatusInternalServerError, Response{Success: false, Error: "internal server error"})
}

func ErrorWithStatus(c *gin.Context, status int, message string) {
	c.JSON(status, Response{Success: false, Error: message})
}

func BadRequest(c *gin.Context, message string) {
	ErrorWithStatus(c, http.StatusBadRequest, message)
}

func Unauthorized(c *gin.Context, message string) {
	ErrorWithStatus(c, http.StatusUnauthorized, message)
}

func NotFound(c *gin.Context, message string) {
	ErrorWithStatus(c, http.StatusNotFound, message)
}

func Conflict(c *gin.Context, message string) {
	ErrorWithStatus(c, http.StatusConflict, message)
}

func InternalServerError(c *gin.Context, message string) {
	ErrorWithStatus(c, http.StatusInternalServerError, message)
}
