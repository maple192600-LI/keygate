package handler

import (
	"context"
	"encoding/json"
	"github.com/gin-gonic/gin"
	"github.com/tabloy/keygate/internal/auth"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/offline"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/pkg/response"
	"github.com/uptrace/bun"
	"sync"
	"time"
)

type keyImportPreview struct {
	raw     []byte
	keyID   string
	expires time.Time
	ownerID string
}
type OfflineKeyHandler struct {
	Store    *store.Store
	Keys     *offline.KeyStore
	mu       sync.Mutex
	previews map[string]keyImportPreview
}

func (h *OfflineKeyHandler) ownerPassword(c *gin.Context, password string) bool {
	owner := c.MustGet("local_owner").(*model.User)
	if !auth.VerifyPassword(owner.PasswordHash, password) {
		response.Err(c, 401, "OWNER_PASSWORD_INVALID", "管理密码不正确")
		return false
	}
	return true
}
func (h *OfflineKeyHandler) historyKey(ctx context.Context) (string, error) {
	physical, err := h.Keys.Status()
	if err != nil {
		return "", err
	}
	expected, err := h.Store.ExpectedSigningIdentity(ctx, physical)
	return expected.KeyID, err
}
func (h *OfflineKeyHandler) audit(c *gin.Context, keyID, action string) error {
	return store.RunInTx(c, h.Store.DB, func(ctx context.Context, tx bun.Tx) error {
		physical, err := h.Keys.Status()
		if err != nil {
			return err
		}
		if _, err = h.Store.ExpectedSigningIdentity(ctx, physical); err != nil {
			return err
		}
		raw, err := json.Marshal(physical)
		if err != nil {
			return err
		}
		if _, err = tx.ExecContext(ctx, "INSERT INTO settings(key,value) VALUES('snail_public_identity',?) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value", string(raw)); err != nil {
			return err
		}
		return store.LocalAuditIn(ctx, tx, c.GetString("user_id"), "offline-signing-identity", keyID, action, nil)
	})
}
func (h *OfflineKeyHandler) Public(c *gin.Context) {
	public, err := h.Keys.Status()
	if err != nil {
		localOfflineError(c, err)
		return
	}
	expected, err := h.Store.ExpectedSigningIdentity(c, public)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	if public.State == "uncreated" && expected.KeyID != "" {
		public.KeyID = expected.KeyID
		public.PublicKeyPEM = expected.PublicKeyPEM
	}
	response.OK(c, gin.H{"identity": public, "hasHistory": expected.KeyID != ""})
}
func (h *OfflineKeyHandler) Create(c *gin.Context) {
	var input struct {
		OwnerPassword string `json:"ownerPassword"`
		Passphrase    string `json:"passphrase"`
	}
	if !LocalJSON(c, &input) || !h.ownerPassword(c, input.OwnerPassword) {
		return
	}
	history, err := h.historyKey(c)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	if history != "" {
		response.Err(c, 409, "SIGNING_IDENTITY_EXISTS", "已有签发身份登记，请导入原签发身份，不能新建替换")
		return
	}
	public, err := h.Keys.Create(input.Passphrase, c.MustGet("local_session_expiry").(time.Time))
	if err != nil {
		localOfflineError(c, err)
		return
	}
	if err = h.audit(c, public.KeyID, "create"); err != nil {
		h.Keys.Lock()
		response.Err(c, 500, "SIGNING_IDENTITY_RESULT_UNKNOWN", "身份已保存，但操作记录核对失败；请刷新身份状态后再解锁")
		return
	}
	response.Created(c, public)
}
func (h *OfflineKeyHandler) Unlock(c *gin.Context) {
	var input struct {
		Passphrase string `json:"passphrase"`
	}
	if !LocalJSON(c, &input) {
		return
	}
	public, err := h.Keys.Unlock(input.Passphrase, c.MustGet("local_session_expiry").(time.Time))
	if err != nil {
		localOfflineError(c, err)
		return
	}
	history, err := h.historyKey(c)
	if err != nil || (history != "" && history != public.KeyID) {
		h.Keys.Lock()
		if err == nil {
			err = &offline.Error{Code: "SIGNING_IDENTITY_INVALID", Message: "身份与已有签发历史不一致"}
		}
		localOfflineError(c, err)
		return
	}
	if err = h.audit(c, public.KeyID, "unlock"); err != nil {
		h.Keys.Lock()
		localOfflineError(c, err)
		return
	}
	response.OK(c, public)
}
func (h *OfflineKeyHandler) Lock(c *gin.Context) {
	h.Keys.Lock()
	response.OK(c, gin.H{"locked": true})
}
func (h *OfflineKeyHandler) Recovery(c *gin.Context) {
	var input struct {
		RequestFile string `json:"requestFile"`
	}
	// The route already requires the current owner session and signing still
	// requires the unlocked original key. Do not ask for a second login here.
	if !LocalJSON(c, &input) {
		return
	}
	r, err := offline.ParseRecoveryRequest([]byte(input.RequestFile))
	if err != nil {
		localOfflineError(c, err)
		return
	}
	expected, err := h.historyKey(c)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	id := store.NewID()
	raw, p, err := h.Keys.SignRecovery(r, id, time.Now())
	if err != nil {
		localOfflineError(c, err)
		return
	}
	if expected != "" && expected != p.KeyID {
		localOfflineError(c, &offline.Error{Code: "SIGNING_IDENTITY_INVALID", Message: "签发身份与已登记公钥不一致"})
		return
	}
	err = store.RunInTx(c, h.Store.DB, func(ctx context.Context, tx bun.Tx) error {
		return store.LocalAuditIn(ctx, tx, c.GetString("user_id"), "offline-license-recovery", id, "sign", map[string]any{
			"requestId": r.RequestID, "installationId": r.InstallationID, "stateFingerprint": r.StateFingerprint, "baselineAt": p.BaselineAt})
	})
	if err != nil {
		localOfflineError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="snail-license-recovery.snailrepair"`)
	c.Data(200, "application/json", raw)
}
func (h *OfflineKeyHandler) ExportEncrypted(c *gin.Context) {
	raw, err := h.Keys.EncryptedIdentity()
	if err != nil {
		localOfflineError(c, err)
		return
	}
	c.Header("Content-Disposition", `attachment; filename="snail-signing-identity.encrypted.json"`)
	c.Data(200, "application/json", raw)
}
func (h *OfflineKeyHandler) ImportPreview(c *gin.Context) {
	var input struct {
		IdentityFile string `json:"identityFile"`
		Passphrase   string `json:"passphrase"`
	}
	if !LocalJSON(c, &input) {
		return
	}
	container, private, err := offline.DecryptContainer([]byte(input.IdentityFile), input.Passphrase)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	for i := range private {
		private[i] = 0
	}
	history, err := h.historyKey(c)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	if history != "" && history != container.KeyID {
		response.Err(c, 409, "SIGNING_IDENTITY_MISMATCH", "身份与已有签发记录不一致")
		return
	}
	current, err := h.Keys.Status()
	if err != nil {
		localOfflineError(c, err)
		return
	}
	if current.KeyID != "" && current.KeyID != container.KeyID {
		response.Err(c, 409, "SIGNING_IDENTITY_MISMATCH", "当前已有另一签发身份，不能覆盖")
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.previews == nil {
		h.previews = map[string]keyImportPreview{}
	}
	for id, item := range h.previews {
		if time.Now().After(item.expires) {
			delete(h.previews, id)
		}
	}
	if len(h.previews) >= 16 {
		response.Err(c, 429, "TOO_MANY_PREVIEWS", "预览过多，请稍后再试")
		return
	}
	id := store.NewID()
	h.previews[id] = keyImportPreview{[]byte(input.IdentityFile), container.KeyID, time.Now().Add(10 * time.Minute), c.GetString("user_id")}
	response.OK(c, gin.H{"previewId": id, "keyId": container.KeyID, "publicKeyPem": container.PublicKeyPEM, "matchesHistory": history == "" || history == container.KeyID})
}
func (h *OfflineKeyHandler) Import(c *gin.Context) {
	var input struct {
		PreviewID     string `json:"previewId"`
		OwnerPassword string `json:"ownerPassword"`
	}
	if !LocalJSON(c, &input) || !h.ownerPassword(c, input.OwnerPassword) {
		return
	}
	h.mu.Lock()
	preview, ok := h.previews[input.PreviewID]
	h.mu.Unlock()
	if !ok || preview.ownerID != c.GetString("user_id") || !time.Now().Before(preview.expires) {
		response.Err(c, 409, "PREVIEW_EXPIRED", "身份预览已失效，请重新导入预览")
		return
	}
	history, err := h.historyKey(c)
	if err != nil {
		localOfflineError(c, err)
		return
	}
	if history != "" && history != preview.keyID {
		response.Err(c, 409, "SIGNING_IDENTITY_MISMATCH", "身份与签发历史不一致")
		return
	}
	if err = h.Keys.Import(preview.raw, history); err != nil {
		localOfflineError(c, err)
		return
	}
	if err = h.audit(c, preview.keyID, "import"); err != nil {
		response.Err(c, 500, "SIGNING_IDENTITY_RESULT_UNKNOWN", "身份已保存，但操作记录未确认；请刷新状态后继续")
		return
	}
	h.mu.Lock()
	delete(h.previews, input.PreviewID)
	h.mu.Unlock()
	response.OK(c, gin.H{"imported": true, "keyId": preview.keyID, "state": "locked"})
}
