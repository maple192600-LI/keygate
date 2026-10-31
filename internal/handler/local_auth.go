package handler

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/tabloy/keygate/internal/auth"
	"github.com/tabloy/keygate/internal/model"
	"github.com/tabloy/keygate/internal/store"
	"github.com/tabloy/keygate/pkg/response"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"
)

type LocalAuthHandler struct {
	Store        *store.Store
	Secret       string
	dummyHash    string
	OnSessionEnd func()
}
type localClaims struct {
	UserID      string `json:"uid"`
	AuthVersion int64  `json:"auth_version"`
	jwt.RegisteredClaims
}

func NewLocalAuthHandler(s *store.Store, secret string) *LocalAuthHandler {
	dummy, err := auth.HashPassword("local-login-dummy-password")
	if err != nil {
		panic(err)
	}
	return &LocalAuthHandler{Store: s, Secret: secret, dummyHash: dummy}
}
func localUser(u *model.User) gin.H {
	var email any
	if u.Email != "" {
		email = u.Email
	}
	return gin.H{"id": u.ID, "username": u.Username, "name": u.Name, "email": email, "role": u.Role, "is_admin": u.Role == model.RoleOwner}
}

// HTTP requests never accept paths or executable input. Unknown JSON fields and
// trailing documents are rejected before any mutation.
func LocalJSON(c *gin.Context, target any) bool {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, 128*1024)
	d := json.NewDecoder(c.Request.Body)
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		response.BadRequest(c, "输入格式无效，请核对后重试")
		return false
	}
	if err := d.Decode(new(any)); err != io.EOF {
		response.BadRequest(c, "输入包含多余内容")
		return false
	}
	return true
}
func (h *LocalAuthHandler) Setup(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
		SiteName string `json:"siteName"`
		Contact  string `json:"contact"`
	}
	if !LocalJSON(c, &req) {
		return
	}
	username := strings.ToLower(strings.TrimSpace(req.Username))
	site := strings.TrimSpace(req.SiteName)
	contact := strings.TrimSpace(req.Contact)
	if !regexp.MustCompile(`^[a-z0-9_-]{3,32}$`).MatchString(username) || site == "" || len([]rune(site)) > 100 || contact == "" || len([]rune(contact)) > 200 {
		response.BadRequest(c, "请填写管理用户名、商家名称和联系方式；用户名为 3 至 32 位字母、数字、下划线或短横线")
		return
	}
	hash, err := auth.HashPassword(req.Password)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	recoveryCredential, err := localRecoveryCredential()
	if err != nil {
		localInternal(c, err)
		return
	}
	recoveryHash, err := auth.HashPassword(recoveryCredential)
	if err != nil {
		localInternal(c, err)
		return
	}
	user, err := h.Store.InitializeLocalWithRecovery(c, username, hash, site, contact, recoveryHash)
	if errors.Is(err, store.ErrLocalSetupComplete) {
		response.Err(c, 409, "SETUP_COMPLETE", "管理端已完成首次设置，请登录")
		return
	}
	if err != nil {
		localInternal(c, err)
		return
	}
	result := localUser(user)
	result["recoveryCredential"] = recoveryCredential
	response.Created(c, result)
}

func localRecoveryCredential() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	defer clear(raw)
	return "SNAIL-RECOVERY-" + strings.ToUpper(hex.EncodeToString(raw)), nil
}

func (h *LocalAuthHandler) ResetRecovery(c *gin.Context) {
	var req struct {
		Username           string `json:"username"`
		RecoveryCredential string `json:"recoveryCredential"`
		NewPassword        string `json:"newPassword"`
	}
	if !LocalJSON(c, &req) {
		return
	}
	newHash, err := auth.HashPassword(req.NewPassword)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	username := strings.ToLower(strings.TrimSpace(req.Username))
	credential := strings.ToUpper(strings.TrimSpace(req.RecoveryCredential))
	if !regexp.MustCompile(`^[a-z0-9_-]{3,32}$`).MatchString(username) || len(credential) != len("SNAIL-RECOVERY-")+64 || !strings.HasPrefix(credential, "SNAIL-RECOVERY-") {
		response.Err(c, 401, "RECOVERY_INVALID", "恢复凭据无效或已使用")
		return
	}
	if err = h.Store.RecoverLocalOwner(c, username, credential, newHash); err != nil {
		if errors.Is(err, store.ErrLocalRecoveryCredentials) {
			response.Err(c, 401, "RECOVERY_INVALID", "恢复凭据无效或已使用")
		} else {
			localInternal(c, err)
		}
		return
	}
	response.OK(c, gin.H{"passwordReset": true})
}

func (h *LocalAuthHandler) RecoveryStatus(c *gin.Context) {
	_, err := h.Store.GetSetting(c, "owner_recovery_credential_hash")
	if errors.Is(err, sql.ErrNoRows) {
		response.OK(c, gin.H{"configured": false})
		return
	}
	if err != nil {
		localInternal(c, err)
		return
	}
	response.OK(c, gin.H{"configured": true})
}

func (h *LocalAuthHandler) RotateRecovery(c *gin.Context) {
	var req struct {
		OwnerPassword string `json:"ownerPassword"`
	}
	if !LocalJSON(c, &req) {
		return
	}
	owner := c.MustGet("local_owner").(*model.User)
	if !auth.VerifyPassword(owner.PasswordHash, req.OwnerPassword) {
		response.Err(c, 401, "OWNER_PASSWORD_INVALID", "管理密码不正确")
		return
	}
	credential, err := localRecoveryCredential()
	if err != nil {
		localInternal(c, err)
		return
	}
	hash, err := auth.HashPassword(credential)
	if err != nil {
		localInternal(c, err)
		return
	}
	if err = h.Store.RotateLocalRecoveryCredential(c, owner.ID, hash); err != nil {
		localInternal(c, err)
		return
	}
	response.OK(c, gin.H{"recoveryCredential": credential})
}
func (h *LocalAuthHandler) Login(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !LocalJSON(c, &req) {
		return
	}
	user, err := h.Store.FindLocalOwner(c, strings.ToLower(strings.TrimSpace(req.Username)))
	hash := h.dummyHash
	if err == nil {
		hash = user.PasswordHash
	} else if !errors.Is(err, sql.ErrNoRows) {
		localInternal(c, err)
		return
	}
	valid := auth.VerifyPassword(hash, req.Password)
	if err != nil || !valid {
		response.Err(c, 401, "INVALID_CREDENTIALS", "用户名或密码不正确")
		return
	}
	refresh := randomHex(32)
	if err = h.Store.CreateLocalSession(c, user, hashToken(refresh)); err != nil {
		if errors.Is(err, store.ErrLocalCredentials) {
			response.Err(c, 401, "INVALID_CREDENTIALS", "用户名或密码不正确")
		} else {
			localInternal(c, err)
		}
		return
	}
	if err = h.setSession(c, user); err != nil {
		localInternal(c, err)
		return
	}
	setSecureCookie(c, "refresh_token", refresh, 30*24*3600, "/api/v1/auth/refresh", false, true)
	response.OK(c, localUser(user))
}
func (h *LocalAuthHandler) setSession(c *gin.Context, u *model.User) error {
	now := time.Now()
	token, err := jwt.NewWithClaims(jwt.SigningMethodHS256, localClaims{UserID: u.ID, AuthVersion: u.AuthVersion, RegisteredClaims: jwt.RegisteredClaims{Issuer: "snail-license-center/local", Subject: u.ID, Audience: jwt.ClaimStrings{"local-offline-owner"}, IssuedAt: jwt.NewNumericDate(now), ExpiresAt: jwt.NewNumericDate(now.Add(24 * time.Hour))}}).SignedString([]byte(h.Secret))
	if err != nil {
		return err
	}
	setSecureCookie(c, "session", token, 24*3600, "/", false, true)
	return nil
}
func (h *LocalAuthHandler) RequireOwner() gin.HandlerFunc {
	return func(c *gin.Context) {
		raw, err := c.Cookie("session")
		if err != nil || raw == "" {
			response.Unauthorized(c, "请先登录管理端")
			c.Abort()
			return
		}
		claims := new(localClaims)
		token, err := jwt.ParseWithClaims(raw, claims, func(*jwt.Token) (any, error) { return []byte(h.Secret), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("snail-license-center/local"), jwt.WithAudience("local-offline-owner"), jwt.WithExpirationRequired())
		if err != nil || !token.Valid || claims.UserID == "" || claims.Subject != claims.UserID {
			response.Unauthorized(c, "登录已失效，请重新登录")
			c.Abort()
			return
		}
		user, err := h.Store.FindUserByID(c, claims.UserID)
		if err != nil || user.Role != model.RoleOwner || user.Username == "" || user.AuthVersion != claims.AuthVersion {
			response.Unauthorized(c, "登录已失效，请重新登录")
			c.Abort()
			return
		}
		c.Set("user_id", user.ID)
		c.Set("local_owner", user)
		c.Set("local_session_expiry", claims.ExpiresAt.Time)
		c.Next()
	}
}
func (h *LocalAuthHandler) Me(c *gin.Context) {
	response.OK(c, localUser(c.MustGet("local_owner").(*model.User)))
}
func clearLocalCookies(c *gin.Context) {
	setSecureCookie(c, "session", "", -1, "/", false, true)
	setSecureCookie(c, "refresh_token", "", -1, "/api/v1/auth/refresh", false, true)
}
func (h *LocalAuthHandler) ChangePassword(c *gin.Context) {
	var req struct {
		Current string `json:"currentPassword"`
		New     string `json:"newPassword"`
	}
	if !LocalJSON(c, &req) {
		return
	}
	hash, err := auth.HashPassword(req.New)
	if err != nil {
		response.BadRequest(c, err.Error())
		return
	}
	if err = h.Store.ChangeLocalPassword(c, c.GetString("user_id"), req.Current, hash); err != nil {
		if errors.Is(err, store.ErrLocalCredentials) {
			response.Err(c, 401, "INVALID_CREDENTIALS", "当前密码不正确")
		} else {
			localInternal(c, err)
		}
		return
	}
	clearLocalCookies(c)
	if h.OnSessionEnd != nil {
		h.OnSessionEnd()
	}
	response.OK(c, gin.H{"signedOut": true})
}
func (h *LocalAuthHandler) Refresh(c *gin.Context) {
	raw, err := c.Cookie("refresh_token")
	if err != nil || raw == "" {
		response.Unauthorized(c, "请重新登录管理端")
		return
	}
	next := randomHex(32)
	renewal, err := h.Store.RenewRefreshToken(c, hashToken(raw), hashToken(next), time.Now().Add(refreshTokenTTL))
	if errors.Is(err, store.ErrRefreshTokenReused) {
		if renewal != nil {
			_ = h.Store.LogoutLocal(c, renewal.Token.UserID)
		}
		clearLocalCookies(c)
		if h.OnSessionEnd != nil {
			h.OnSessionEnd()
		}
		response.Unauthorized(c, "登录已失效，请重新登录")
		return
	}
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || errors.Is(err, store.ErrRefreshUserNotFound) {
			response.Unauthorized(c, "登录已失效，请重新登录")
		} else {
			localInternal(c, err)
		}
		return
	}
	if renewal.User.Role != model.RoleOwner || renewal.User.Username == "" {
		response.Unauthorized(c, "管理账号无效")
		return
	}
	if err = h.setSession(c, renewal.User); err != nil {
		localInternal(c, err)
		return
	}
	setSecureCookie(c, "refresh_token", next, int(time.Until(renewal.ExpiresAt).Seconds()), "/api/v1/auth/refresh", false, true)
	response.OK(c, gin.H{"refreshed": true})
}
func (h *LocalAuthHandler) Logout(c *gin.Context) {
	if err := h.Store.LogoutLocal(c, c.GetString("user_id")); err != nil {
		localInternal(c, err)
		return
	}
	clearLocalCookies(c)
	if h.OnSessionEnd != nil {
		h.OnSessionEnd()
	}
	response.OK(c, gin.H{"signedOut": true})
}
