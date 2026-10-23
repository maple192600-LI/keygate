package handler

import (
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/pkg/response"
	"strings"
)

type OfflineRequestHandler struct{ Store *store.Store }

func (h *OfflineRequestHandler) Import(c *gin.Context) {
	var input struct {
		RequestFile string `json:"requestFile"`
		DeviceCode  string `json:"deviceCode"`
		ProductID   string `json:"productId"`
		DeviceLabel string `json:"deviceLabel"`
	}
	if !LocalJSON(c, &input) {
		return
	}
	raw := []byte(input.RequestFile)
	if input.RequestFile != "" && input.DeviceCode != "" {
		response.BadRequest(c, "请选择申请文件或填写完整设备码，不要同时提交两种来源")
		return
	}
	if input.RequestFile == "" {
		code := strings.TrimSpace(input.DeviceCode)
		if !strings.HasPrefix(code, "STQ-WIN1-") || len(code) != 73 {
			response.BadRequest(c, "请填写 STQ-WIN1- 开头的完整设备码")
			return
		}
		if input.ProductID == "" {
			input.ProductID = offline.ProductID
		}
		raw, _ = json.Marshal(offline.Request{FormatVersion: 2, RequestID: store.NewID(), ProductID: input.ProductID, Device: offline.Device{Type: offline.MachineType, Digest: strings.TrimPrefix(code, "STQ-WIN1-")}, DeviceLabel: input.DeviceLabel, AppVersion: ""})
	}
	req, fingerprint, err := offline.ParseRequest(raw, offline.ProductID)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	saved, replayed, err := h.Store.ImportOfflineRequest(c, &model.OfflineRequest{RequestID: req.RequestID, ProductCode: req.ProductID, DeviceType: req.Device.Type, DeviceDigest: req.Device.Digest, DeviceLabel: req.DeviceLabel, RawBytes: raw, Fingerprint: fingerprint, CreatedBy: c.GetString("user_id")})
	if errors.Is(err, store.ErrOfflineRequestConflict) {
		response.Err(c, 409, "REQUEST_CONFLICT", "同一申请编号已保存不同内容，请重新取得原申请")
		return
	}
	if err != nil {
		localInternal(c, err)
		return
	}
	response.OK(c, gin.H{"request": saved, "replayed": replayed})
}
func localOfflineError(c *gin.Context, err error) {
	var business *offline.Error
	if errors.As(err, &business) {
		status := 400
		if business.Code == "SIGNING_KEY_LOCKED" {
			status = 423
		}
		if business.Code == "REVISION_CONFLICT" || business.Code == "OPERATION_CONFLICT" || business.Code == "ISSUANCE_STOPPED" {
			status = 409
		}
		response.Err(c, status, business.Code, business.Message)
		return
	}
	localInternal(c, err)
}
