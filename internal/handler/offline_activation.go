package handler

import (
	"github.com/gin-gonic/gin"
	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/pkg/response"
)

type ClientActivationHandler struct {
	Service *service.ClientActivationService
}

func (h *ClientActivationHandler) Activate(c *gin.Context) {
	var input service.ClientActivationCommand
	if !LocalJSON(c, &input) {
		return
	}
	result, err := h.Service.Activate(c, input)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	response.OK(c, result)
}

func (h *ClientActivationHandler) Sync(c *gin.Context) {
	var input service.ClientSyncCommand
	if !LocalJSON(c, &input) {
		return
	}
	result, err := h.Service.Sync(c, input)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	response.OK(c, result)
}
