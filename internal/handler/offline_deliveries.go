package handler

import (
	"database/sql"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/pkg/response"
	"strings"
)

type OfflineDeliveryHandler struct{ Store *store.Store }

func (h *OfflineDeliveryHandler) List(c *gin.Context) {
	rows, err := h.Store.OfflineDeliveries(c, c.Param("id"))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.NotFound(c, "授权签发原件不存在")
		} else {
			localOfflineError(c, err)
		}
		return
	}
	response.OK(c, gin.H{"items": rows})
}
func (h *OfflineDeliveryHandler) Record(c *gin.Context) {
	var input struct {
		OperationID string `json:"operationId"`
		Method      string `json:"method"`
		Recipient   string `json:"recipient"`
		DeliveredAt string `json:"deliveredAt"`
		Notes       string `json:"notes"`
	}
	if !LocalJSON(c, &input) {
		return
	}
	input.Recipient = strings.TrimSpace(input.Recipient)
	stamp, err := offline.ParseUTC(input.DeliveredAt)
	if err != nil || !offline.ValidUUID4(input.OperationID) || len([]rune(input.Recipient)) < 1 || len([]rune(input.Recipient)) > 200 || len([]rune(input.Notes)) > 2000 {
		response.BadRequest(c, "请填写收件对象、实际交付时间，并检查备注长度")
		return
	}
	if input.Method != "wechat" && input.Method != "email" && input.Method != "usb" && input.Method != "other" {
		response.BadRequest(c, "请选择交付方式")
		return
	}
	row, replayed, err := h.Store.RecordOfflineDelivery(c, &store.OfflineDelivery{IssuanceID: c.Param("id"), OperationID: input.OperationID, Method: input.Method, Recipient: input.Recipient, DeliveredAt: stamp, Notes: input.Notes, CreatedBy: c.GetString("user_id")})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			response.NotFound(c, "授权签发原件不存在")
		} else {
			localOfflineError(c, err)
		}
		return
	}
	response.OK(c, gin.H{"delivery": row, "replayed": replayed})
}
