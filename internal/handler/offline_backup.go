package handler

import (
	"context"
	"github.com/gin-gonic/gin"
	"github.com/tabloy/keygate/internal/auth"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/service"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/pkg/response"
	"github.com/uptrace/bun"
	"io"
	"net/http"
	"strings"
	"time"
)

type bundlePreview struct {
	bundle *store.BusinessBundle
	owner  string
	expiry time.Time
}
type restorePreview struct {
	candidate *service.RestoreCandidate
	owner     string
}

// All methods are owner-only and run inside LocalWriteGate, including previews.
type OfflineBackupHandler struct {
	Service  *service.OfflineBackupService
	Gate     *LocalWriteGate
	Activate func(*service.RestoreCandidate) error
	bundles  map[string]bundlePreview
	restores map[string]restorePreview
}

func (h *OfflineBackupHandler) clean(ctx context.Context) error {
	for id, p := range h.bundles {
		if time.Now().After(p.expiry) {
			delete(h.bundles, id)
		}
	}
	for id, p := range h.restores {
		if time.Now().After(p.candidate.ExpiresAt) {
			if err := h.Service.DiscardRestore(ctx, p.candidate); err != nil {
				return err
			}
			delete(h.restores, id)
		}
	}
	return nil
}
func (h *OfflineBackupHandler) Close(ctx context.Context) error {
	for id, p := range h.restores {
		if err := h.Service.DiscardRestore(ctx, p.candidate); err != nil {
			return err
		}
		delete(h.restores, id)
	}
	return nil
}
func (h *OfflineBackupHandler) ExportBusiness(c *gin.Context) {
	raw, err := h.Service.ExportBusiness(c)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="snail-management.snailbundle"`)
	c.Data(200, "application/json", raw)
}
func uploadArchive(c *gin.Context) ([]byte, bool) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, offline.BackupLimit+1024*1024)
	// Keep the bounded upload in memory, without leaving password-bearing form
	// fields or archive files in the OS multipart temporary directory.
	reader, err := c.Request.MultipartReader()
	if err != nil {
		response.BadRequest(c, "请选择完整文件")
		return nil, false
	}
	var raw []byte
	fields := map[string]string{}
	for {
		part, err := reader.NextPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			response.BadRequest(c, "文件未能完整读取")
			return nil, false
		}
		name := part.FormName()
		limit := int64(1024)
		if name == "file" {
			limit = offline.BackupLimit
		}
		value, err := io.ReadAll(io.LimitReader(part, limit+1))
		if err != nil || int64(len(value)) > limit || name == "" {
			response.BadRequest(c, "文件或输入超过允许大小")
			return nil, false
		}
		if name == "file" {
			if raw != nil {
				response.BadRequest(c, "只能选择一个文件")
				return nil, false
			}
			raw = value
		} else {
			_, duplicate := fields[name]
			if name != "password" || duplicate {
				response.BadRequest(c, "文件输入包含未知字段")
				return nil, false
			}
			fields[name] = string(value)
		}
	}
	if len(raw) == 0 {
		response.BadRequest(c, "请选择完整文件")
		return nil, false
	}
	c.Set("backup_password", fields["password"])
	return raw, true
}
func (h *OfflineBackupHandler) PreviewBusiness(c *gin.Context) {
	if err := h.clean(c); err != nil {
		localOfflineError(c, err)
		return
	}
	if len(h.bundles) >= 2 {
		response.Err(c, 409, "TOO_MANY_PREVIEWS", "请先完成或取消已有预览")
		return
	}
	raw, ok := uploadArchive(c)
	if !ok {
		return
	}
	bundle, err := store.ParseBusinessBundle(raw)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	public, err := h.Service.PublicIdentity(c)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	if public.KeyID != "" && public.KeyID != bundle.PublicIdentity.KeyID {
		response.Err(c, 409, "SIGNING_IDENTITY_MISMATCH", "数据包与本机签发身份不一致")
		return
	}
	summary, err := h.Service.Store.ImportBusinessBundle(c, bundle, c.GetString("user_id"), false)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	if h.bundles == nil {
		h.bundles = map[string]bundlePreview{}
	}
	id := store.NewID()
	h.bundles[id] = bundlePreview{bundle, c.GetString("user_id"), time.Now().Add(10 * time.Minute)}
	response.OK(c, gin.H{"previewId": id, "summary": summary})
}
func (h *OfflineBackupHandler) ConfirmBusiness(c *gin.Context) {
	var input struct {
		PreviewID     string `json:"previewId"`
		OwnerPassword string `json:"ownerPassword"`
	}
	if !LocalJSON(c, &input) || !h.password(c, input.OwnerPassword) {
		return
	}
	p, ok := h.bundles[input.PreviewID]
	if !ok || p.owner != c.GetString("user_id") || time.Now().After(p.expiry) {
		response.Err(c, 409, "PREVIEW_EXPIRED", "预览已失效，请重新选择文件")
		return
	}
	current, err := h.Service.PublicIdentity(c)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	if current.KeyID != "" && current.KeyID != p.bundle.PublicIdentity.KeyID {
		response.Err(c, 409, "SIGNING_IDENTITY_MISMATCH", "本机签发身份已变化，请重新预览")
		return
	}
	summary, err := h.Service.Store.ImportBusinessBundle(c, p.bundle, c.GetString("user_id"), true)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	delete(h.bundles, input.PreviewID)
	response.OK(c, gin.H{"imported": true, "summary": summary})
}
func (h *OfflineBackupHandler) Backup(c *gin.Context) {
	var input struct {
		Password        string `json:"password"`
		IncludeIdentity bool   `json:"includeIdentity"`
		ConfirmIdentity bool   `json:"confirmIdentity"`
	}
	if !LocalJSON(c, &input) {
		return
	}
	raw, err := h.Service.Backup(c, input.Password, input.IncludeIdentity, input.ConfirmIdentity)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="snail-management.snailbackup"`)
	c.Data(200, "application/json", raw)
}
func (h *OfflineBackupHandler) PreviewRestore(c *gin.Context) {
	if h.Activate == nil {
		response.Err(c, 409, "RESTORE_RUNTIME_REQUIRED", "请从独立管理客户端恢复资料")
		return
	}
	if err := h.clean(c); err != nil {
		localOfflineError(c, err)
		return
	}
	if len(h.restores) >= 2 {
		response.Err(c, 409, "TOO_MANY_PREVIEWS", "请先完成或取消已有预览")
		return
	}
	raw, ok := uploadArchive(c)
	if !ok {
		return
	}
	candidate, err := h.Service.PreviewRestore(c, raw, c.GetString("backup_password"))
	if err != nil {
		localOfflineError(c, err)
		return
	}
	if h.restores == nil {
		h.restores = map[string]restorePreview{}
	}
	h.restores[candidate.ID] = restorePreview{candidate, c.GetString("user_id")}
	response.OK(c, candidate)
}
func (h *OfflineBackupHandler) password(c *gin.Context, value string) bool {
	owner := c.MustGet("local_owner").(*model.User)
	if !auth.VerifyPassword(owner.PasswordHash, value) {
		response.Err(c, 401, "OWNER_PASSWORD_INVALID", "当前管理密码不正确")
		return false
	}
	return true
}
func (h *OfflineBackupHandler) ConfirmRestore(c *gin.Context) {
	var input struct {
		PreviewID         string `json:"previewId"`
		OwnerPassword     string `json:"ownerPassword"`
		SigningPassphrase string `json:"signingPassphrase"`
	}
	if !LocalJSON(c, &input) || !h.password(c, input.OwnerPassword) {
		return
	}
	p, ok := h.restores[input.PreviewID]
	if !ok || p.owner != c.GetString("user_id") || p.candidate.Store == nil || time.Now().After(p.candidate.ExpiresAt) {
		response.Err(c, 409, "PREVIEW_EXPIRED", "恢复预览已失效，请重新选择文件")
		return
	}
	if err := h.Service.ValidateRestoreContinuity(c, p.candidate, input.SigningPassphrase); err != nil {
		localOfflineError(c, err)
		return
	}
	// The restored account and JWTs are invalidated before selecting this DB.
	err := store.RunInTx(c, p.candidate.Store.DB, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.ExecContext(ctx, "UPDATE users SET auth_version=auth_version+1 WHERE role='owner'"); err != nil {
			return err
		}
		if _, err := tx.ExecContext(ctx, "DELETE FROM refresh_tokens"); err != nil {
			return err
		}
		owner := c.MustGet("local_owner").(*model.User)
		if _, err := tx.ExecContext(ctx, "INSERT INTO offline_actors(id,display_name) VALUES(?,?) ON CONFLICT(id) DO NOTHING", owner.ID, owner.Username); err != nil {
			return err
		}
		// One preview is one restore operation, including a retry after runtime
		// publication failed. Its audit exists only in the private candidate
		// until selection succeeds; retry must not invent a second restore.
		if _, err := tx.ExecContext(ctx, `INSERT INTO audit_logs(id,entity,entity_id,action,actor_id,actor_type,changes) VALUES(?,'backup',?,'restore',?,'user','{}') ON CONFLICT(id) DO NOTHING`, p.candidate.ID, p.candidate.ID, owner.ID); err != nil {
			return err
		}
		var matching int
		if err := tx.NewRaw("SELECT count(*) FROM audit_logs WHERE id=? AND entity='backup' AND entity_id=? AND action='restore' AND actor_id=?", p.candidate.ID, p.candidate.ID, owner.ID).Scan(ctx, &matching); err != nil {
			return err
		}
		if matching != 1 {
			return &offline.Error{Code: "OPERATION_CONFLICT", Message: "恢复操作号与已有记录冲突"}
		}
		return nil
	})
	if err != nil {
		localOfflineError(c, err)
		return
	}
	h.Gate.Stop()
	if err = h.Activate(p.candidate); err != nil {
		h.Gate.Resume()
		localOfflineError(c, err)
		return
	}
	h.Service.Keys.Lock()
	delete(h.restores, input.PreviewID)
	response.OK(c, gin.H{"restored": true, "previewId": p.candidate.ID})
}
func (h *OfflineBackupHandler) Cancel(c *gin.Context) {
	var input struct {
		PreviewID string `json:"previewId"`
	}
	if !LocalJSON(c, &input) {
		return
	}
	if p, ok := h.bundles[input.PreviewID]; ok && p.owner == c.GetString("user_id") {
		delete(h.bundles, input.PreviewID)
	}
	if p, ok := h.restores[input.PreviewID]; ok && p.owner == c.GetString("user_id") {
		if err := h.Service.DiscardRestore(c, p.candidate); err != nil {
			localOfflineError(c, err)
			return
		}
		delete(h.restores, input.PreviewID)
	}
	response.OK(c, gin.H{"cancelled": true})
}
func (h *OfflineBackupHandler) Settings(c *gin.Context) {
	if c.Request.Method == "GET" {
		values, err := h.Service.Store.GetSettings(c)
		if err != nil {
			localOfflineError(c, err)
			return
		}
		response.OK(c, gin.H{"siteName": values["site_name"], "contact": values["snail_contact"]})
		return
	}
	var input struct {
		SiteName string `json:"siteName"`
		Contact  string `json:"contact"`
	}
	if !LocalJSON(c, &input) {
		return
	}
	input.SiteName = strings.TrimSpace(input.SiteName)
	input.Contact = strings.TrimSpace(input.Contact)
	if input.SiteName == "" || len([]rune(input.SiteName)) > 100 || input.Contact == "" || len([]rune(input.Contact)) > 200 {
		response.BadRequest(c, "请填写商家名称和联系方式")
		return
	}
	err := store.RunInTx(c, h.Service.Store.DB, func(ctx context.Context, tx bun.Tx) error {
		for key, value := range map[string]string{"site_name": input.SiteName, "snail_contact": input.Contact} {
			if _, err := tx.ExecContext(ctx, "UPDATE settings SET value=? WHERE key=?", value, key); err != nil {
				return err
			}
		}
		return store.LocalAuditIn(ctx, tx, c.GetString("user_id"), "merchant", "", "update", nil)
	})
	if err != nil {
		localOfflineError(c, err)
		return
	}
	response.OK(c, input)
}
func (h *OfflineBackupHandler) Operations(c *gin.Context) {
	var rows []struct {
		ID        string    `json:"id"`
		Entity    string    `json:"entity"`
		EntityID  string    `json:"entityId"`
		Action    string    `json:"action"`
		Actor     string    `json:"actor"`
		CreatedAt time.Time `json:"createdAt"`
	}
	err := h.Service.Store.DB.NewRaw(`SELECT a.id,a.entity,a.entity_id,a.action,COALESCE(p.display_name,'本机恢复') AS actor,a.created_at FROM audit_logs a LEFT JOIN offline_actors p ON p.id=a.actor_id ORDER BY a.created_at DESC,a.id DESC LIMIT 200`).Scan(c, &rows)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	if rows == nil {
		rows = make([]struct {
			ID        string    `json:"id"`
			Entity    string    `json:"entity"`
			EntityID  string    `json:"entityId"`
			Action    string    `json:"action"`
			Actor     string    `json:"actor"`
			CreatedAt time.Time `json:"createdAt"`
		}, 0)
	}
	response.OK(c, rows)
}
