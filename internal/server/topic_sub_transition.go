package server

import (
	"errors"

	"chat/server/store/types"
)

type selfSubscriptionTransitionInput struct {
	category      types.TopicCat
	isTopicOwner  bool
	oldWant       types.AccessMode
	oldGiven      types.AccessMode
	requested     types.AccessMode
	defaultAccess types.AccessMode
	p2pAccess     types.AccessMode
}

type selfSubscriptionTransition struct {
	want        types.AccessMode
	given       types.AccessMode
	ownerChange bool
}

// 计算已有用户修改自身订阅时的权限变化。
// 此函数只负责状态计算，不处理持久化和通知，以保持调用方原有的执行顺序。
func planExistingSelfSubscription(input selfSubscriptionTransitionInput) (selfSubscriptionTransition, error) {
	result := selfSubscriptionTransition{
		want:  input.oldWant,
		given: input.oldGiven,
	}
	requested := input.requested

	if requested != types.ModeUnset {
		if input.isTopicOwner && (!requested.IsOwner() || !requested.IsJoiner()) {
			return selfSubscriptionTransition{}, errors.New("群主不可取消群主权限或禁言自身")
		}

		if result.given.IsOwner() {
			result.ownerChange = requested.IsOwner() && !input.oldWant.IsOwner()

			if requested.IsOwner() && !result.given.BetterEqual(requested) {
				result.given |= requested
			}
		} else if requested.IsOwner() {
			return selfSubscriptionTransition{}, errors.New("非群主无法转让群权")
		} else if input.category == types.TopicCatGrp && result.given.IsAdmin() && requested.IsAdmin() {
			grantable := requested &^ types.ModeDelete
			if !result.given.BetterEqual(grantable) {
				result.given |= grantable
			}
		}

		switch input.category {
		case types.TopicCatP2P:
			requested = (requested & input.p2pAccess) | types.ModeApprove
		case types.TopicCatSys:
			requested &= (requested & types.ModeCSys) | types.ModeWrite
		}
	}

	if requested == types.ModeUnset {
		if !input.oldWant.IsJoiner() {
			result.want = result.given | input.defaultAccess
		}
	} else if result.want != requested {
		result.want = requested
	}

	return result, nil
}
