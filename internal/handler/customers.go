package handler

import (
	"database/sql"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/pkg/response"
	"net/mail"
	"strconv"
	"strings"
)

type CommercialCustomerHandler struct{ Store *store.Store }

func localPage(c *gin.Context) (int, int, bool) {
	page, size := 1, 20
	var err error
	if c.Query("page") != "" {
		page, err = strconv.Atoi(c.Query("page"))
		if err != nil || page < 1 || page > 1000000 {
			response.BadRequest(c, "页码无效")
			return 0, 0, false
		}
	}
	if c.Query("pageSize") != "" {
		size, err = strconv.Atoi(c.Query("pageSize"))
		if err != nil || size < 1 || size > 100 {
			response.BadRequest(c, "每页数量须为 1 至 100")
			return 0, 0, false
		}
	}
	return page, size, true
}
func (h *CommercialCustomerHandler) List(c *gin.Context) {
	page, size, ok := localPage(c)
	if !ok {
		return
	}
	items, count, err := h.Store.CommercialCustomers(c, strings.TrimSpace(c.Query("q")), page, size)
	if err != nil {
		localInternal(c, err)
		return
	}
	response.OK(c, gin.H{"items": items, "total": count, "page": page, "pageSize": size})
}
func (h *CommercialCustomerHandler) Get(c *gin.Context) {
	customer, err := h.Store.CommercialCustomer(c, c.Param("id"))
	if errors.Is(err, sql.ErrNoRows) {
		response.NotFound(c, "客户档案不存在")
		return
	}
	if err != nil {
		localInternal(c, err)
		return
	}
	response.OK(c, customer)
}

type customerDraft struct {
	DisplayName   string  `json:"displayName"`
	CustomerType  string  `json:"customerType"`
	ContactName   string  `json:"contactName"`
	ContactMethod string  `json:"contactMethod"`
	Email         *string `json:"email"`
	SourceText    string  `json:"sourceText"`
	Notes         string  `json:"notes"`
}

func (h *CommercialCustomerHandler) Save(c *gin.Context) {
	var draft customerDraft
	if !LocalJSON(c, &draft) {
		return
	}
	draft.DisplayName = strings.TrimSpace(draft.DisplayName)
	draft.ContactName = strings.TrimSpace(draft.ContactName)
	draft.ContactMethod = strings.TrimSpace(draft.ContactMethod)
	if len([]rune(draft.DisplayName)) < 1 || len([]rune(draft.DisplayName)) > 100 || (draft.CustomerType != "company" && draft.CustomerType != "person") || len([]rune(draft.ContactName)) > 80 || len([]rune(draft.ContactMethod)) > 200 || len([]rune(draft.SourceText)) > 200 || len([]rune(draft.Notes)) > 2000 {
		response.BadRequest(c, "请填写客户名称与类型，检查联系方式和备注长度")
		return
	}
	if draft.Email != nil {
		email := strings.TrimSpace(*draft.Email)
		if email == "" {
			draft.Email = nil
		} else {
			address, err := mail.ParseAddress(email)
			if err != nil || address.Address != email || len(email) > 254 {
				response.BadRequest(c, "邮箱格式不正确，也可以留空")
				return
			}
			draft.Email = &email
		}
	}
	customer := &model.CommercialCustomer{ID: store.NewID(), DisplayName: draft.DisplayName, CustomerType: draft.CustomerType, ContactName: draft.ContactName, ContactMethod: draft.ContactMethod, Email: draft.Email, SourceText: draft.SourceText, Notes: draft.Notes, IssuanceEnabled: true}
	create := c.Param("id") == ""
	if !create {
		previous, err := h.Store.CommercialCustomer(c, c.Param("id"))
		if errors.Is(err, sql.ErrNoRows) {
			response.NotFound(c, "客户档案不存在")
			return
		}
		if err != nil {
			localInternal(c, err)
			return
		}
		customer.ID = previous.ID
		customer.IssuanceEnabled = previous.IssuanceEnabled
	}
	if err := h.Store.SaveCommercialCustomer(c, customer, c.GetString("user_id"), create); err != nil {
		localInternal(c, err)
		return
	}
	saved, err := h.Store.CommercialCustomer(c, customer.ID)
	if err != nil {
		localInternal(c, err)
		return
	}
	if create {
		response.Created(c, saved)
	} else {
		response.OK(c, saved)
	}
}
