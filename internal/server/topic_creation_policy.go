package server

import (
	"strings"

	"chat/server/store"
	"chat/server/store/types"
)

func canCreateManagedTopic(uid types.Uid) (bool, error) {
	if uid.IsZero() {
		return false, types.ErrUserNotFound
	}
	user, err := store.Users.Get(uid)
	if err != nil {
		return false, err
	}
	if user == nil {
		return false, types.ErrUserNotFound
	}
	return topicCreationAllowedByTrusted(user.Trusted), nil
}

// topicCreationAllowedByTrusted restricts identities managed by the commerce
// identity provider. Native/server test identities retain the base protocol's
// group creation behavior.
func topicCreationAllowedByTrusted(value any) bool {
	trusted := externalIdentityObject(value)
	provider, _ := trusted["identity_provider"].(string)
	externalID, _ := trusted["external_id"].(string)
	isManagedIdentity := strings.TrimSpace(provider) != "" || strings.TrimSpace(externalID) != ""
	if !isManagedIdentity {
		return true
	}
	return trustedBoolean(trusted["staff"]) || trustedBoolean(trusted["agent_verified"])
}

func trustedBoolean(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case string:
		return strings.EqualFold(strings.TrimSpace(typed), "true") || strings.TrimSpace(typed) == "1"
	case float64:
		return typed == 1
	case int:
		return typed == 1
	case int64:
		return typed == 1
	default:
		return false
	}
}
