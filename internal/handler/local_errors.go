package handler

import (
	"errors"
	"fmt"
	"github.com/gin-gonic/gin"
	"github.com/tabloy/keygate/pkg/response"
	"github.com/uptrace/bun/driver/pgdriver"
	"log/slog"
)

func localInternal(c *gin.Context, err error) {
	// PostgreSQL DETAIL can contain a whole failing row, including a license
	// original. Log classification and SQLSTATE without secrets or raw records.
	fields := []any{"route", c.FullPath(), "error_type", fmt.Sprintf("%T", err)}
	var pgerr pgdriver.Error
	if errors.As(err, &pgerr) {
		fields = append(fields, "sql_state", pgerr.Field('C'), "constraint", pgerr.Field('n'))
	}
	slog.Error("本地管理操作失败", fields...)
	response.Err(c, 500, "INTERNAL_ERROR", "本地资料操作未完成，请保留输入并重试；已有资料保留")
}
