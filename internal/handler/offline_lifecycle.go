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

type OfflineLifecycleHandler struct{ Store *store.Store }

func (h *OfflineLifecycleHandler) fail(c *gin.Context, err error) {
	if errors.Is(err, sql.ErrNoRows) {
		response.NotFound(c, "客户或授权不存在")
		return
	}
	localOfflineError(c, err)
}
func (h *OfflineLifecycleHandler) List(c *gin.Context) {
	page, size, ok := localPage(c)
	if !ok {
		return
	}
	term, delivery := c.Query("termState"), c.Query("deliveryState")
	if term != "" && term != "not_yet_valid" && term != "valid" && term != "expired" && term != "perpetual" && term != "active" || delivery != "" && delivery != "pending" && delivery != "recorded" {
		response.BadRequest(c, "筛选状态无效")
		return
	}
	rows, count, err := h.Store.OfflineLicenses(c, store.OfflineLicenseFilter{Expiring: c.Query("expiring") == "30", Search: strings.TrimSpace(c.Query("q")), CustomerID: c.Query("customerId"), TermState: term, DeliveryState: delivery, Page: page, Size: size})
	if err != nil {
		h.fail(c, err)
		return
	}
	response.OK(c, gin.H{"items": rows, "total": count, "page": page, "pageSize": size})
}
func (h *OfflineLifecycleHandler) Get(c *gin.Context) {
	row, err := h.Store.OfflineLicense(c, c.Param("id"))
	if err != nil {
		h.fail(c, err)
		return
	}
	issuances, bindings, deliveries, err := h.Store.OfflineLicenseHistory(c, row.ID)
	if err != nil {
		h.fail(c, err)
		return
	}
	response.OK(c, gin.H{"license": row, "issuances": issuances, "bindings": bindings, "deliveries": deliveries})
}
func (h *OfflineLifecycleHandler) Overview(c *gin.Context) {
	row, err := h.Store.OfflineOverview(c)
	if err != nil {
		h.fail(c, err)
		return
	}
	response.OK(c, row)
}
func (h *OfflineLifecycleHandler) State(kind string) gin.HandlerFunc {
	return func(c *gin.Context) {
		var input store.OfflineStateCommand
		if !LocalJSON(c, &input) {
			return
		}
		input.Reason = strings.TrimSpace(input.Reason)
		if !offline.ValidUUID4(input.OperationID) || input.ExpectedEnabled == nil || input.Reason == "" || len([]rune(input.Reason)) > 1000 {
			response.BadRequest(c, "请填写本次调整原因，并重新核对当前状态")
			return
		}
		result, err := h.Store.SetOfflineIssuanceState(c, c.GetString("user_id"), kind, c.Param("id"), input)
		if err != nil {
			h.fail(c, err)
			return
		}
		response.OK(c, result)
	}
}
