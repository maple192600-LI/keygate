package handler

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/tabloy/keygate/pkg/response"
	"net/http"
	"sync/atomic"
	"time"
)

// One native Go service owns the local database. All local HTTP mutations,
// including credentials and signing identity, share this cancellable gate.
// Authentication runs after admission, so queued requests recheck auth_version.
type LocalWriteGate struct {
	token   chan struct{}
	stopped atomic.Bool
}

func NewLocalWriteGate() *LocalWriteGate {
	g := &LocalWriteGate{token: make(chan struct{}, 1)}
	g.token <- struct{}{}
	return g
}
func (g *LocalWriteGate) Stop()   { g.stopped.Store(true) }
func (g *LocalWriteGate) Resume() { g.stopped.Store(false) }
func (g *LocalWriteGate) Middleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if g.stopped.Load() {
			response.Err(c, 503, "RESTORE_SWITCHING", "正在切换经验证的资料，请稍后重新登录")
			c.Abort()
			return
		}
		if c.Request.Method == http.MethodGet || c.Request.Method == http.MethodHead {
			c.Next()
			return
		}
		ctx, cancel := context.WithTimeout(c.Request.Context(), 60*time.Second)
		defer cancel()
		select {
		case <-ctx.Done():
			response.Err(c, 409, "MANAGEMENT_BUSY", "管理端正在备份或处理其他写入，请稍后按原操作重试")
			c.Abort()
			return
		case <-g.token:
		}
		defer func() { g.token <- struct{}{} }()
		if g.stopped.Load() {
			response.Err(c, 503, "RESTORE_SWITCHING", "正在切换经验证的资料，请稍后重新登录")
			c.Abort()
			return
		}
		c.Next()
	}
}
