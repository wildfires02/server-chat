package server

import (
	"errors"

	"chat/server/auth"
	"chat/server/store"
	"chat/server/store/types"
)

// replyGetAssets 返回客户端可见的贴纸、动态 Emoji 与 GIF 素材目录。
func (t *Topic) replyGetAssets(sess *Session, asUid types.Uid, authLevel auth.Level, msg *ClientComMessage) error {
	now := types.TimeNow()
	if t.cat != types.TopicCatMe || msg.Get.Assets == nil {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("assets can only be queried on the me topic")
	}
	catalog, err := store.Assets.Query(*msg.Get.Assets, authLevel == auth.LevelRoot)
	if err != nil {
		sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
		return err
	}
	sess.queueOut(&ServerComMessage{Meta: &MsgServerMeta{
		Id:        msg.Id,
		Topic:     t.original(asUid),
		Timestamp: &now,
		Assets:    catalog,
	}})
	return nil
}

// replySetAsset 允许 root 用户发布、下架或删除素材。
func (t *Topic) replySetAsset(sess *Session, asUid types.Uid, authLevel auth.Level, msg *ClientComMessage) error {
	now := types.TimeNow()
	if t.cat != types.TopicCatMe || msg.Set.Asset == nil {
		sess.queueOut(ErrOperationNotAllowedReply(msg, now))
		return errors.New("assets can only be updated on the me topic")
	}
	if authLevel != auth.LevelRoot {
		sess.queueOut(ErrPermissionDeniedReply(msg, now))
		return types.ErrPermissionDenied
	}
	if asset := msg.Set.Asset.Asset; asset != nil {
		refs := []string{asset.URL}
		if asset.Preview != "" {
			refs = append(refs, asset.Preview)
		}
		for _, variant := range asset.Variants {
			refs = append(refs, variant.URL)
		}
		if err := store.ValidateFileAttachments(asUid, refs); err != nil {
			sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
			return err
		}
		if err := store.PopulateMediaAssetMetadata(asset); err != nil {
			sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
			return err
		}
	}
	if pack := msg.Set.Asset.Pack; pack != nil && pack.Cover != "" {
		if err := store.ValidateFileAttachments(asUid, []string{pack.Cover}); err != nil {
			sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
			return err
		}
	}
	catalog, err := store.Assets.Apply(*msg.Set.Asset)
	if err != nil {
		sess.queueOut(decodeStoreErrorExplicitTs(err, msg.Id, msg.Original, now, msg.Timestamp, nil))
		return err
	}
	sess.queueOut(&ServerComMessage{Meta: &MsgServerMeta{
		Id:        msg.Id,
		Topic:     t.original(asUid),
		Timestamp: &now,
		Assets:    catalog,
	}})
	t.presSubsOnline("assets", "", nilPresParams, nilPresFilters, sess.sid)
	return nil
}
