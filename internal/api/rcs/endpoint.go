package rcs

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/zagnu-labs/rcs-messenger/internal/api/httputil"
	"github.com/zagnu-labs/rcs-messenger/internal/rcs"
)

type Endpoint struct {
	service *rcs.Service
	pairing Pairer
}

func NewEndpoint(service *rcs.Service) *Endpoint {
	return &Endpoint{service: service}
}

func (e *Endpoint) SetPairing(p Pairer) *Endpoint {
	e.pairing = p
	return e
}

type sendRequest struct {
	To      string `json:"to"`
	Message string `json:"message"`
}

type checkRequest struct {
	To string `json:"to"`
}

func (e *Endpoint) Send(c *gin.Context) {
	var req sendRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request body")
		return
	}

	res, err := e.service.Send(c.Request.Context(), req.To, req.Message)
	if err != nil {
		httputil.Error(c, err)
		return
	}
	switch {
	case res.DryRun:
		httputil.OK(c, "dry run, nothing sent", res)
	case res.Sent:
		httputil.OK(c, "sent", res)
	default:
		httputil.OK(c, "send did not complete: "+res.FinalStatus, res)
	}
}

func (e *Endpoint) Inbound(c *gin.Context) {
	var req rcs.Inbound
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request body")
		return
	}

	res, err := e.service.HandleInbound(c.Request.Context(), req)
	if err != nil {
		httputil.Error(c, err)
		return
	}
	if res.OptOut && !res.Blocked {
		httputil.OK(c, "opt-out seen but not recorded", res)
		return
	}
	if res.OptOut {
		httputil.OK(c, "opted out", res)
		return
	}
	httputil.OK(c, "ok", res)
}

func (e *Endpoint) Check(c *gin.Context) {
	var req checkRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request body")
		return
	}

	res, err := e.service.Check(c.Request.Context(), req.To)
	if err != nil {
		httputil.Error(c, err)
		return
	}
	httputil.OK(c, "ok", res)
}

func (e *Endpoint) Thread(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	res, err := e.service.Thread(c.Request.Context(), c.Query("to"), limit)
	if err != nil {
		httputil.Error(c, err)
		return
	}
	httputil.OK(c, "ok", res)
}

type nudgeRequest struct {
	To     string `json:"to"`
	Action string `json:"action"`
}

func (e *Endpoint) Nudge(c *gin.Context) {
	var req nudgeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request body")
		return
	}

	res, err := e.service.Nudge(c.Request.Context(), req.To, req.Action)
	if err != nil {
		httputil.Error(c, err)
		return
	}
	if res.Changed {
		httputil.OK(c, "evidence changed", res)
		return
	}
	httputil.OK(c, "no change", res)
}

func (e *Endpoint) Results(c *gin.Context) {
	limit, _ := strconv.Atoi(c.Query("limit"))
	res, err := e.service.Results(c.Request.Context(), c.Query("to"), limit)
	if err != nil {
		httputil.Error(c, err)
		return
	}
	httputil.OK(c, "ok", res)
}

func (e *Endpoint) Health(c *gin.Context) {
	res, err := e.service.Health(c.Request.Context())
	if err != nil {
		httputil.Error(c, err)
		return
	}
	httputil.OK(c, "ok", gin.H{"ready": res.Ready(), "sender": res})
}
