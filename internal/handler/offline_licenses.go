package handler

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/pkg/response"
	"mime"
)

type OfflineLicenseHandler struct {
	Service *service.OfflineIssuanceService
}

func (h *OfflineLicenseHandler) Preview(c *gin.Context) {
	var intent service.OfflineIntent
	if !LocalJSON(c, &intent) {
		return
	}
	result, err := h.Service.Preview(c, intent)
	if err != nil {
		h.fail(c, err)
		return
	}
	response.OK(c, result)
}
func (h *OfflineLicenseHandler) Issue(action string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var command service.OfflineIssueCommand
		if !LocalJSON(c, &command) {
			return
		}
		if command.Action != "" && command.Action != action {
			response.BadRequest(c, "签发动作与入口不一致")
			return
		}
		command.Action = action
		if action != "initial" {
			if command.LicenseID != "" && command.LicenseID != c.Param("id") {
				response.BadRequest(c, "授权与当前入口不一致")
				return
			}
			command.LicenseID = c.Param("id")
		}
		result, err := h.Service.Issue(c, c.GetString("user_id"), command)
		if err != nil {
			var business *offline.Error
			if errors.As(err, &business) || errors.Is(err, sql.ErrNoRows) {
				h.fail(c, err)
			} else {
				response.Err(c, 500, "ISSUANCE_RESULT_UNKNOWN", "签发结果未确认，请查询本次操作结果后再继续")
			}
			return
		}
		response.OK(c, result)
	}
}
func (h *OfflineLicenseHandler) File(c *gin.Context) {
	row, err := h.Service.Store.LicenseIssuance(c, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	sum := sha256.Sum256(row.RawBytes)
	if hex.EncodeToString(sum[:]) != row.SHA256 {
		response.Err(c, 500, "ISSUANCE_ARCHIVE_DAMAGED", "授权原件校验失败，请保留资料并从经验证的备份恢复")
		return
	}
	c.Header("Content-Disposition", mime.FormatMediaType("attachment", map[string]string{"filename": row.FileName}))
	c.Header("X-Content-SHA256", row.SHA256)
	c.Data(200, "application/octet-stream", row.RawBytes)
}
func (h *OfflineLicenseHandler) Code(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	code, err := h.Service.Store.OfflineLicenseCode(c, c.GetString("user_id"), c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	response.OK(c, gin.H{"licenseCode": code})
}
func (h *OfflineLicenseHandler) Operation(c *gin.Context) {
	result, err := h.Service.Operation(c, c.Param("operationId"))
	if err != nil {
		h.fail(c, err)
		return
	}
	response.OK(c, result)
}
func (h *OfflineLicenseHandler) fail(c *gin.Context, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		response.NotFound(c, "客户、申请、授权或签发记录不存在")
		return
	}
	localOfflineError(c, err)
}
