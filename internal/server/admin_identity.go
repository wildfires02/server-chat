package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"sync"
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
	externalIdentityProfileLocks    [256]sync.Mutex
)

type adminIdentitySessionRequest struct {
	Provider        string               `json:"provider"`
	ExternalID      string               `json:"external_id"`
	ProfileVersion  int64                `json:"profile_version"`
	Profile         adminIdentityProfile `json:"profile"`
	TokenTTLSeconds int                  `json:"token_ttl_seconds"`
}

type adminIdentityProfile struct {
	Name          string `json:"name"`
	Avatar        string `json:"avatar"`
	Role          string `json:"role,omitempty"`
	Badge         string `json:"badge,omitempty"`
	ChannelID     uint   `json:"channel_id,omitempty"`
	Staff         bool   `json:"staff,omitempty"`
	AgentVerified bool   `json:"agent_verified,omitempty"`
}

type adminIdentitySessionResponse struct {
	IMUID     string `json:"im_uid"`
	Token     []byte `json:"token"`
	ExpiresAt int64  `json:"expires_at"`
	Name      string `json:"name"`
	Avatar    string `json:"avatar"`
}

type adminIdentityProfileResponse struct {
	IMUID          string `json:"im_uid"`
	ProfileVersion int64  `json:"profile_version"`
	Name           string `json:"name"`
	Avatar         string `json:"avatar"`
}

type adminIdentityDeviceRequest struct {
	Provider    string `json:"provider"`
	ExternalID  string `json:"external_id"`
	DeviceToken string `json:"device_token"`
	OldToken    string `json:"old_device_token,omitempty"`
	Platform    string `json:"platform"`
	Lang        string `json:"lang,omitempty"`
}

type adminIdentityDeviceResponse struct {
	IMUID    string `json:"im_uid"`
	Synced   bool   `json:"synced"`
	Platform string `json:"platform"`
}

func normalizeAdminIdentityInput(input *adminIdentitySessionRequest) bool {
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.Profile.Name = strings.TrimSpace(input.Profile.Name)
	input.Profile.Avatar = strings.TrimSpace(input.Profile.Avatar)
	return externalIdentityProviderPattern.MatchString(input.Provider) &&
		externalIdentityIDPattern.MatchString(input.ExternalID) &&
		len([]rune(input.Profile.Name)) <= 160 && len(input.Profile.Avatar) <= 4096 &&
		input.ProfileVersion >= 0
}

func normalizeAdminIdentityDeviceInput(input *adminIdentityDeviceRequest) bool {
	input.Provider = strings.ToLower(strings.TrimSpace(input.Provider))
	input.ExternalID = strings.TrimSpace(input.ExternalID)
	input.DeviceToken = strings.TrimSpace(input.DeviceToken)
	input.OldToken = strings.TrimSpace(input.OldToken)
	input.Platform = strings.ToLower(strings.TrimSpace(input.Platform))
	input.Lang = strings.ToLower(strings.TrimSpace(input.Lang))
	switch input.Platform {
	case "ios", "iphone", "ipad", "darwin":
		input.Platform = "ios"
	case "android":
		input.Platform = "android"
	default:
		return false
	}
	return externalIdentityProviderPattern.MatchString(input.Provider) &&
		externalIdentityIDPattern.MatchString(input.ExternalID) &&
		input.DeviceToken != "" && len(input.DeviceToken) <= 4096 &&
		len(input.OldToken) <= 4096 && len(input.Lang) <= 32
}

// updateIdentityDevice 接收商城服务保存的原生 App 推送令牌。
// 浏览器不注册 Web Push；商城服务在 Flutter 刷新 FCM Token 时调用本接口，
// 因而老用户无需先打开聊天页面也能收到离线消息和来电通知。
func (handler *adminHTTPHandler) updateIdentityDevice(wrt http.ResponseWriter, req *http.Request,
	requestID string) {
	var input adminIdentityDeviceRequest
	if !handler.decode(wrt, req, &input, requestID) {
		return
	}
	if !normalizeAdminIdentityDeviceInput(&input) {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_identity_device", requestID)
		return
	}

	uid, _, _, _, err := store.Users.GetAuthUniqueRecord(externalIdentityAuthScheme,
		externalIdentityUnique(input.Provider, input.ExternalID))
	if err != nil || uid.IsZero() {
		handler.writeError(wrt, http.StatusNotFound, "identity_not_found", requestID)
		return
	}
	if err = store.Devices.Update(uid, input.OldToken, &types.DeviceDef{
		DeviceId: input.DeviceToken,
		Platform: input.Platform,
		LastSeen: time.Now().UTC(),
		Lang:     input.Lang,
	}); err != nil {
		logs.Warn.Printf("external identity device sync failed for %s:%s: %v",
			input.Provider, input.ExternalID, err)
		handler.writeError(wrt, http.StatusServiceUnavailable, "identity_device_sync_failed", requestID)
		return
	}
	userChannelsSubUnsub(uid, input.DeviceToken, true)
	handler.writeData(wrt, http.StatusOK, adminIdentityDeviceResponse{
		IMUID: uid.UserId(), Synced: true, Platform: input.Platform,
	}, requestID)
}

func (handler *adminHTTPHandler) createIdentitySession(wrt http.ResponseWriter, req *http.Request,
	requestID string) {
	var input adminIdentitySessionRequest
	if !handler.decode(wrt, req, &input, requestID) {
		return
	}
	if !normalizeAdminIdentityInput(&input) {
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

	profile := externalIdentityProfileFromUser(user)
	handler.writeData(wrt, http.StatusOK, adminIdentitySessionResponse{
		IMUID: uid.UserId(), Token: token, ExpiresAt: expires.Unix(),
		Name: profile.Name, Avatar: profile.Avatar,
	}, requestID)
}

// updateIdentityProfile 将外部身份提供方的用户资料同步到 server-chat。
// 此接口不签发登录令牌，只允许携带管理员令牌的可信服务调用。
func (handler *adminHTTPHandler) updateIdentityProfile(wrt http.ResponseWriter, req *http.Request,
	requestID string) {
	var input adminIdentitySessionRequest
	if !handler.decode(wrt, req, &input, requestID) {
		return
	}
	if !normalizeAdminIdentityInput(&input) {
		handler.writeError(wrt, http.StatusBadRequest, "invalid_identity_profile", requestID)
		return
	}

	uid, err := ensureExternalIdentity(input)
	if err != nil {
		logs.Warn.Printf("external identity profile sync failed for %s:%s: %v",
			input.Provider, input.ExternalID, err)
		handler.writeError(wrt, http.StatusInternalServerError, "identity_profile_sync_failed", requestID)
		return
	}
	user, err := store.Users.Get(uid)
	if err != nil || user == nil {
		handler.writeError(wrt, http.StatusInternalServerError, "identity_user_unavailable", requestID)
		return
	}
	if user.State != types.StateOK {
		handler.writeError(wrt, http.StatusForbidden, "identity_suspended", requestID)
		return
	}
	profile := externalIdentityProfileFromUser(user)
	handler.writeData(wrt, http.StatusOK, adminIdentityProfileResponse{
		IMUID: uid.UserId(), ProfileVersion: externalIdentityProfileVersion(user.Trusted),
		Name: profile.Name, Avatar: profile.Avatar,
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
		"fn":              input.Profile.Name,
		"external_id":     input.ExternalID,
		"user_id":         input.ExternalID,
		"payment_user_id": input.ExternalID,
	}
	if input.Profile.Role != "" {
		public["role"] = input.Profile.Role
	}
	if input.Profile.Badge != "" {
		public["badge"] = input.Profile.Badge
	}
	if input.Profile.Avatar != "" {
		public["photo"] = input.Profile.Avatar
	}
	trusted := map[string]any{
		"identity_provider": input.Provider,
		"external_id":       input.ExternalID,
		"profile_version":   input.ProfileVersion,
		"role":              input.Profile.Role,
		"badge":             input.Profile.Badge,
		"channel_id":        input.Profile.ChannelID,
		"staff":             input.Profile.Staff,
		"agent_verified":    input.Profile.AgentVerified,
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
	lock := &externalIdentityProfileLocks[uint64(uid)%uint64(len(externalIdentityProfileLocks))]
	lock.Lock()
	defer lock.Unlock()
	return updateExternalIdentityProfileWithStore(store.Users, uid, input)
}

type externalIdentityProfileStore interface {
	Get(uid types.Uid) (*types.User, error)
	Update(uid types.Uid, update map[string]any) error
}

func updateExternalIdentityProfileWithStore(users externalIdentityProfileStore, uid types.Uid,
	input adminIdentitySessionRequest) error {
	user, err := users.Get(uid)
	if err != nil {
		return err
	}
	if user == nil {
		return types.ErrNotFound
	}
	if externalIdentityProfileVersion(user.Trusted) > input.ProfileVersion {
		return nil
	}

	public := externalIdentityObject(user.Public)
	public["fn"] = input.Profile.Name
	public["external_id"] = input.ExternalID
	public["user_id"] = input.ExternalID
	public["payment_user_id"] = input.ExternalID
	if input.Profile.Role != "" {
		public["role"] = input.Profile.Role
		if input.Profile.Badge == "" {
			delete(public, "badge")
		} else {
			public["badge"] = input.Profile.Badge
		}
	}
	if input.Profile.Avatar == "" {
		delete(public, "photo")
	} else {
		public["photo"] = input.Profile.Avatar
	}
	trusted := externalIdentityObject(user.Trusted)
	trusted["identity_provider"] = input.Provider
	trusted["external_id"] = input.ExternalID
	trusted["profile_version"] = input.ProfileVersion
	if input.Profile.Role != "" {
		trusted["role"] = input.Profile.Role
		trusted["badge"] = input.Profile.Badge
		trusted["channel_id"] = input.Profile.ChannelID
		trusted["staff"] = input.Profile.Staff
		trusted["agent_verified"] = input.Profile.AgentVerified
	}
	return users.Update(uid, map[string]any{"Public": public, "Trusted": trusted})
}

func externalIdentityObject(value any) map[string]any {
	result := make(map[string]any)
	switch typed := value.(type) {
	case map[string]any:
		for key, item := range typed {
			result[key] = item
		}
	case []byte:
		_ = json.Unmarshal(typed, &result)
	case json.RawMessage:
		_ = json.Unmarshal(typed, &result)
	case string:
		_ = json.Unmarshal([]byte(typed), &result)
	}
	return result
}

func externalIdentityProfileVersion(trusted any) int64 {
	value := externalIdentityObject(trusted)["profile_version"]
	switch typed := value.(type) {
	case int:
		return int64(typed)
	case int64:
		return typed
	case float64:
		return int64(typed)
	case json.Number:
		version, _ := typed.Int64()
		return version
	}
	return 0
}

func externalIdentityProfileFromUser(user *types.User) adminIdentityProfile {
	if user == nil {
		return adminIdentityProfile{}
	}
	public := externalIdentityObject(user.Public)
	name, _ := public["fn"].(string)
	avatar, _ := public["photo"].(string)
	return adminIdentityProfile{Name: name, Avatar: avatar}
}
