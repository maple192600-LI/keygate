package store

import (
	"context"
	"database/sql"
	"errors"
	"github.com/tabloy/keygate/internal/auth"
	"github.com/tabloy/keygate/internal/model"
	"github.com/uptrace/bun"
)

var ErrLocalSetupComplete = errors.New("SETUP_COMPLETE")
var ErrLocalCredentials = errors.New("INVALID_CREDENTIALS")
var ErrLocalRecoveryCredentials = errors.New("INVALID_RECOVERY_CREDENTIALS")

const (
	SigningServiceActorID         = "snail-signing-service"
	ClientActivationServiceActorID = "snail-client-activation-service"
)

func (s *Store) InitializeLocal(ctx context.Context, username, hash, siteName, contact string) (*model.User, error) {
	return s.initializeLocal(ctx, username, hash, siteName, contact, "")
}

func (s *Store) InitializeLocalWithRecovery(ctx context.Context, username, hash, siteName, contact, recoveryHash string) (*model.User, error) {
	if !auth.ValidPasswordHash(recoveryHash) {
		return nil, errors.New("local recovery credential hash is invalid")
	}
	return s.initializeLocal(ctx, username, hash, siteName, contact, recoveryHash)
}

func (s *Store) initializeLocal(ctx context.Context, username, hash, siteName, contact, recoveryHash string) (*model.User, error) {
	var result *model.User
	err := RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.ExecContext(ctx, "SELECT pg_advisory_xact_lock(8675309)"); err != nil {
			return err
		}
		needed, err := s.SetupNeeded(ctx, tx)
		if err != nil {
			return err
		}
		if !needed {
			return ErrLocalSetupComplete
		}
		result = &model.User{ID: NewID(), Username: username, PasswordHash: hash, AuthVersion: 1, Name: username, Role: model.RoleOwner}
		if _, err := tx.NewInsert().Model(result).Exec(ctx); err != nil {
			return err
		}
		productID := NewID()
		if _, err := tx.NewRaw("INSERT INTO products(id,name,slug,type,license_product_code) VALUES(?, '蜗牛财务', 'snail-finance', 'desktop', 'cn.snailfinance.desktop')", productID).Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.NewRaw("INSERT INTO plans(id,product_id,name,slug,license_model,license_type,max_activations,max_seats,grace_days,checkout_id) VALUES(?,?,'离线标准版','offline-standard','standard','perpetual',1,0,0,?)", NewID(), productID, ShortID()).Exec(ctx); err != nil {
			return err
		}
		for key, value := range map[string]string{"site_name": siteName, "snail_contact": contact, "setup_complete": "true"} {
			if _, err := tx.NewRaw("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value", key, value).Exec(ctx); err != nil {
				return err
			}
		}
		if recoveryHash != "" {
			if _, err := tx.NewRaw("INSERT INTO settings(key,value) VALUES('owner_recovery_credential_hash',?)", recoveryHash).Exec(ctx); err != nil {
				return err
			}
		}
		return LocalAuditIn(ctx, tx, result.ID, "owner", result.ID, "initialize", nil)
	})
	return result, err
}

func (s *Store) RotateLocalRecoveryCredential(ctx context.Context, ownerID, recoveryHash string) error {
	if !auth.ValidPasswordHash(recoveryHash) {
		return errors.New("local recovery credential hash is invalid")
	}
	return RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		var exists bool
		if err := tx.NewRaw("SELECT EXISTS(SELECT 1 FROM users WHERE id=? AND role='owner' AND username IS NOT NULL)", ownerID).Scan(ctx, &exists); err != nil {
			return err
		}
		if !exists {
			return ErrLocalCredentials
		}
		if _, err := tx.NewRaw("INSERT INTO settings(key,value) VALUES('owner_recovery_credential_hash',?) ON CONFLICT(key) DO UPDATE SET value=EXCLUDED.value", recoveryHash).Exec(ctx); err != nil {
			return err
		}
		return LocalAuditIn(ctx, tx, ownerID, "owner-recovery-credential", ownerID, "rotate", nil)
	})
}

func (s *Store) RecoverLocalOwner(ctx context.Context, username, recoveryCredential, passwordHash string) error {
	if !auth.ValidPasswordHash(passwordHash) || recoveryCredential == "" {
		return ErrLocalRecoveryCredentials
	}
	return RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		if _, err := tx.NewRaw("SELECT pg_advisory_xact_lock(8675309)").Exec(ctx); err != nil {
			return err
		}
		var savedHash string
		if err := tx.NewRaw("SELECT value FROM settings WHERE key='owner_recovery_credential_hash' FOR UPDATE").Scan(ctx, &savedHash); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrLocalRecoveryCredentials
			}
			return err
		}
		owner := new(model.User)
		if err := tx.NewSelect().Model(owner).Where("username=? AND role='owner'", username).For("UPDATE").Scan(ctx); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return ErrLocalRecoveryCredentials
			}
			return err
		}
		if !auth.VerifyPassword(savedHash, recoveryCredential) {
			return ErrLocalRecoveryCredentials
		}
		if _, err := tx.NewRaw("UPDATE users SET password_hash=?,auth_version=auth_version+1,updated_at=now() WHERE id=?", passwordHash, owner.ID).Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.NewRaw("DELETE FROM refresh_tokens WHERE user_id=?", owner.ID).Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.NewRaw("DELETE FROM settings WHERE key='owner_recovery_credential_hash'").Exec(ctx); err != nil {
			return err
		}
		return LocalAuditIn(ctx, tx, owner.ID, "owner", owner.ID, "recovery-password-reset", nil)
	})
}
func EnsureOfflineActor(ctx context.Context, db bun.IDB, actor string) error {
	_, err := db.NewRaw(`INSERT INTO offline_actors(id,display_name)
 SELECT id,COALESCE(NULLIF(username,''),NULLIF(name,''),'历史操作者') FROM users WHERE id=?
 ON CONFLICT(id) DO NOTHING`, actor).Exec(ctx)
	return err
}
func LocalAuditIn(ctx context.Context, db bun.IDB, actor, entity, id, action string, changes map[string]any) error {
	if err := EnsureOfflineActor(ctx, db, actor); err != nil {
		return err
	}
	_, err := db.NewInsert().Model(&model.AuditLog{ID: NewID(), Entity: entity, EntityID: id, Action: action, ActorType: "user", ActorID: actor, Changes: changes}).Exec(ctx)
	return err
}
func (s *Store) FindLocalOwner(ctx context.Context, username string) (*model.User, error) {
	u := new(model.User)
	err := s.DB.NewSelect().Model(u).Where("username = ?", username).Where("role = 'owner'").Scan(ctx)
	return u, err
}

// Create the refresh token under the same row lock as the credential recheck.
// A concurrent password change cannot turn an old password into a new session.
func (s *Store) CreateLocalSession(ctx context.Context, user *model.User, refreshHash string) error {
	return RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		locked := new(model.User)
		if err := tx.NewSelect().Model(locked).Where("id = ?", user.ID).For("UPDATE").Scan(ctx); err != nil {
			return err
		}
		if locked.Role != model.RoleOwner || locked.PasswordHash != user.PasswordHash || locked.AuthVersion != user.AuthVersion {
			return ErrLocalCredentials
		}
		if _, err := tx.NewRaw("INSERT INTO refresh_tokens(id,user_id,token_hash,expires_at) VALUES(?,?,?,now()+interval '30 days')", NewID(), user.ID, refreshHash).Exec(ctx); err != nil {
			return err
		}
		return LocalAuditIn(ctx, tx, user.ID, "session", user.ID, "login", nil)
	})
}
func (s *Store) ChangeLocalPassword(ctx context.Context, id, currentPassword, newHash string) error {
	return RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		user := new(model.User)
		if err := tx.NewSelect().Model(user).Where("id = ?", id).For("UPDATE").Scan(ctx); err != nil {
			return err
		}
		if user.Role != model.RoleOwner || !auth.VerifyPassword(user.PasswordHash, currentPassword) {
			return ErrLocalCredentials
		}
		if _, err := tx.NewRaw("UPDATE users SET password_hash=?,auth_version=auth_version+1,updated_at=now() WHERE id=?", newHash, id).Exec(ctx); err != nil {
			return err
		}
		if _, err := tx.NewRaw("DELETE FROM refresh_tokens WHERE user_id=?", id).Exec(ctx); err != nil {
			return err
		}
		return LocalAuditIn(ctx, tx, id, "owner", id, "change-password", nil)
	})
}
func (s *Store) LogoutLocal(ctx context.Context, id string) error {
	return RunInTx(ctx, s.DB, func(ctx context.Context, tx bun.Tx) error {
		result, err := tx.NewRaw("UPDATE users SET auth_version=auth_version+1 WHERE id=? AND role='owner'", id).Exec(ctx)
		if err != nil {
			return err
		}
		if n, _ := result.RowsAffected(); n != 1 {
			return sql.ErrNoRows
		}
		if _, err := tx.NewRaw("DELETE FROM refresh_tokens WHERE user_id=?", id).Exec(ctx); err != nil {
			return err
		}
		return LocalAuditIn(ctx, tx, id, "session", id, "logout", nil)
	})
}
