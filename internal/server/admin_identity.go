package server

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"

	"chat/server/auth"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

const externalIdentityAuthScheme = "external"

var (
	externalIdentityProviderPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{0,31}$`)
	externalIdentityIDPattern       = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)
)

type adminIdentitySessionRequest struct {
	Provider        string               `json:"provider"`
	ExternalID      string               `json:"external_id"`
	ProfileVersion  int64                `json:"profile_version"`
	Profile         adminIdentityProfile `json:"profile"`
	TokenTTLSeconds int                  `json:"token_ttl_seconds"`
}

type adminIdentityProfile struct {
	Name   string `json:"name"`
	Avatar string `json:"avatar"`
}

type adminIdentitySessionResponse struct {
	IMUID     string `json:"im_uid"`
	Token     []byte `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Name      string `json:"name"`
	Avatar    string `json:"avatar"`
}

func (handler *adminHTTPHandler) createIdentitySession(wrt http.ResponseWriter, req *http.Request,
	requestID string) {
	var input adminIdentitySessionRequest
	if !handler.decode(wrt, req, &input, requestID) {
		return
	}
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.Profile.Name = strings.TrimSpace(input.Profile.Name)
	input.Profile.Avatar = strings.TrimSpace(input.Profile.Avatar)
	if !externalIdentityProviderPattern.MatchString(input.Provider) ||
		!externalIdentityIDPattern.MatchString(input.ExternalID) ||
		len([]rune(input.Profile.Name)) > 160 || len(input.Profile.Avatar) > 4096 ||
		input.ProfileVersion < 0 {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_identity_session", requestID)
		return
	}

	ttl := input.TokenTTLSeconds
	if ttl == 0 {
		ttl = 900
	}
	if ttl < 60 || ttl > 3600 {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_identity_token_ttl", requestID)
		return
	}

	uid, err := ensureExternalIdentity(input)
	if err != nil {
		logs.Warn.Printf("external identity provisioning failed for %s:%s: %v",
			input.Provider, input.ExternalID, err)
		handler.writeError(wrt, http.StatusInternalServerError, "identity_provision_failed", requestID)
		return
	}
	user, err := store.Users.Get(uid)
	if err != nil || user == nil {
		logs.Warn.Printf("external identity user lookup failed for %s:%s (uid=%s): %v",
			input.Provider, input.ExternalID, uid.UserId(), err)
		handler.writeError(wrt, http.StatusInternalServerError, "identity_user_unavailable", requestID)
		return
	}
	if user.State != types.StateOK {
		handler.writeError(wrt, http.StatusForbidden, "identity_suspended", requestID)
		return
	}

	tokenHandler := store.Store.GetLogicalAuthHandler("token")
	if tokenHandler == nil || !tokenHandler.IsInitialized() {
		handler.writeError(wrt, http.StatusServiceUnavailable, "token_auth_unavailable", requestID)
		return
	}
	token, expires, err := tokenHandler.GenSecret(&auth.Rec{
		Uid:       uid,
		AuthLevel: auth.LevelAuth,
		Lifetime:  auth.Duration(time.Duration(ttl) * time.Second),
		Features:  auth.FeatureValidated,
	})
	if err != nil {
		logs.Warn.Printf("external identity token generation failed for %s: %v", uid.UserId(), err)
		handler.writeError(wrt, http.StatusInternalServerError, "identity_token_failed", requestID)
		return
	}

	handler.writeData(wrt, http.StatusOK, adminIdentitySessionResponse{
		IMUID: uid.UserId(), Token: token, ExpiresAt: expires.Unix(),
		Name: input.Profile.Name, Avatar: input.Profile.Avatar,
	}, requestID)
}

func ensureExternalIdentity(input adminIdentitySessionRequest) (types.Uid, error) {
	unique := externalIdentityUnique(input.Provider, input.ExternalID)
	uid, _, _, _, err := store.Users.GetAuthUniqueRecord(externalIdentityAuthScheme, unique)
	if err != nil && !errors.Is(err, types.ErrNotFound) {
		return types.ZeroUid, err
	}
	// SQL 适配器在认证记录不存在时返回 (ZeroUid, nil)，部分其他适配器返回 ErrNotFound。
	// 两种结果都表示首次登录；只有非零 UID 才代表已经绑定的外部身份。
	if !uid.IsZero() {
		return uid, updateExternalIdentityProfile(uid, input)
	}

	public := map[string]any{
		"fn": input.Profile.Name,
	}
	if input.Profile.Avatar != "" {
		public["photo"] = input.Profile.Avatar
	}
	trusted := map[string]any{
		"identity_provider": input.Provider,
		"external_id":       input.ExternalID,
		"profile_version":   input.ProfileVersion,
	}
	created, err := store.Users.Create(&types.User{
		State:   types.StateOK,
		Access:  types.DefaultAccess{Auth: types.ModeCAuth, Anon: types.ModeNone},
		Public:  public,
		Trusted: trusted,
	}, nil)
	if err != nil {
		return types.ZeroUid, err
	}
	uid = created.Uid()
	if err = store.Users.AddAuthRecord(uid, auth.LevelAuth, externalIdentityAuthScheme,
		unique, []byte{0}, time.Time{}); err == nil {
		return uid, nil
	}

	// 并发请求可能已经抢先写入同一个外部身份。
	winner, _, _, _, lookupErr := store.Users.GetAuthUniqueRecord(externalIdentityAuthScheme, unique)
	if lookupErr == nil && !winner.IsZero() {
		if deleteErr := store.Users.Delete(uid, true); deleteErr != nil {
			logs.Warn.Printf("failed to delete duplicate provisioned user %s: %v", uid.UserId(), deleteErr)
		}
		return winner, updateExternalIdentityProfile(winner, input)
	}
	_ = store.Users.Delete(uid, true)
	return types.ZeroUid, err
}

// 旧版 MySQL 部署将 auth.uname 限制为 32 个字符。存储层会为逻辑值添加
// "external:" 前缀，因此使用 128 位 URL 安全摘要可将完整键控制在
// 31 个字符，同时隐藏商城用户 ID。
func externalIdentityUnique(provider, externalID string) string {
	digest := sha256.Sum256([]byte(provider + "\x00" + externalID))
	return base64.RawURLEncoding.EncodeToString(digest[:16])
}

func updateExternalIdentityProfile(uid types.Uid, input adminIdentitySessionRequest) error {
	public := map[string]any{"fn": input.Profile.Name}
	if input.Profile.Avatar != "" {
		public["photo"] = input.Profile.Avatar
	}
	return store.Users.Update(uid, map[string]any{
		"Public": public,
		"Trusted": map[string]any{
			"identity_provider": input.Provider,
			"external_id":       input.ExternalID,
			"profile_version":   input.ProfileVersion,
		},
	})
}
