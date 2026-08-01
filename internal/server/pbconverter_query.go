package server

import (
	"chat/api/pbx"
	"chat/server/store/types"
)

// pbGetQuerySerialize 完成pbGet查询Serialize所需的内部处理。
func pbGetQuerySerialize(in *MsgGetQuery) *pbx.GetQuery {
	if in == nil {
		return nil
	}

	out := &pbx.GetQuery{
		What: in.What,
	}

	if in.Desc != nil {
		out.Desc = &pbx.GetOpts{
			IfModifiedSince: timeToInt64(in.Desc.IfModifiedSince),
			User:            in.Desc.User,
			Topic:           in.Desc.Topic,
			Limit:           int32(in.Desc.Limit),
			Forward:         in.Desc.Forward,
			Cursor:          in.Desc.Cursor,
		}
	}
	if in.Sub != nil {
		out.Sub = &pbx.GetOpts{
			IfModifiedSince: timeToInt64(in.Sub.IfModifiedSince),
			User:            in.Sub.User,
			Topic:           in.Sub.Topic,
			Limit:           int32(in.Sub.Limit),
			Forward:         in.Sub.Forward,
			Cursor:          in.Sub.Cursor,
		}
	}
	if in.Data != nil {
		out.Data = &pbx.GetOpts{
			BeforeId: int32(in.Data.BeforeId),
			SinceId:  int32(in.Data.SinceId),
			Limit:    int32(in.Data.Limit),
			Forward:  in.Data.Forward,
		}

		if len(in.Data.IdRanges) > 0 {
			out.Data.Ranges = make([]*pbx.SeqRange, len(in.Data.IdRanges))
			for i, dq := range in.Data.IdRanges {
				out.Data.Ranges[i] = &pbx.SeqRange{Low: int32(dq.LowId), Hi: int32(dq.HiId)}
			}
		}
	}
	if in.Del != nil {
		out.Del = &pbx.GetOpts{
			BeforeId: int32(in.Del.BeforeId),
			SinceId:  int32(in.Del.SinceId),
			Limit:    int32(in.Del.Limit),
			Forward:  in.Del.Forward,
		}
		if len(in.Del.IdRanges) > 0 {
			out.Del.Ranges = make([]*pbx.SeqRange, len(in.Del.IdRanges))
			for i, dq := range in.Del.IdRanges {
				out.Del.Ranges[i] = &pbx.SeqRange{Low: int32(dq.LowId), Hi: int32(dq.HiId)}
			}
		}
	}
	if in.Search != nil {
		out.Search = &pbx.SearchOpts{
			Query:      in.Search.Query,
			Scope:      in.Search.Scope,
			FromUserId: in.Search.From,
			Kinds:      in.Search.Kinds,
			MinDate:    timeToInt64(in.Search.MinDate),
			MaxDate:    timeToInt64(in.Search.MaxDate),
			Cursor:     in.Search.Cursor,
			Limit:      int32(in.Search.Limit),
		}
	}
	if in.Contacts != nil {
		out.Contacts = &pbx.ContactQuery{
			Since:           in.Contacts.Since,
			Limit:           int32(in.Contacts.Limit),
			Recommendations: in.Contacts.Recommendations,
		}
	}
	if in.Assets != nil {
		out.Assets = &pbx.AssetQuery{
			PackId:   in.Assets.PackId,
			Query:    in.Assets.Query,
			Kind:     in.Assets.Kind,
			Since:    in.Assets.Since,
			Limit:    int32(in.Assets.Limit),
			AssetIds: append([]string(nil), in.Assets.AssetIds...),
		}
	}
	if in.Readers != nil {
		out.Readers = &pbx.ReadParticipantsQuery{SeqId: int32(in.Readers.SeqId)}
	}
	if in.Previews != nil {
		out.Previews = &pbx.PreviewQuery{Topics: append([]string(nil), in.Previews.Topics...)}
	}
	return out
}

// pbGetQueryDeserialize 完成pbGet查询Deserialize所需的内部处理。
func pbGetQueryDeserialize(in *pbx.GetQuery) *MsgGetQuery {
	if in == nil {
		return nil
	}

	msg := MsgGetQuery{
		What: in.GetWhat(),
	}

	if desc := in.GetDesc(); desc != nil {
		msg.Desc = &MsgGetOpts{
			IfModifiedSince: int64ToTime(desc.GetIfModifiedSince()),
			User:            desc.GetUser(),
			Topic:           desc.GetTopic(),
			Limit:           int(desc.GetLimit()),
			Forward:         desc.GetForward(),
			Cursor:          desc.GetCursor(),
		}
	}
	if sub := in.GetSub(); sub != nil {
		msg.Sub = &MsgGetOpts{
			IfModifiedSince: int64ToTime(sub.GetIfModifiedSince()),
			User:            sub.GetUser(),
			Topic:           sub.GetTopic(),
			Limit:           int(sub.GetLimit()),
			Forward:         sub.GetForward(),
			Cursor:          sub.GetCursor(),
		}
	}
	if data := in.GetData(); data != nil {
		msg.Data = &MsgGetOpts{
			BeforeId: int(data.GetBeforeId()),
			SinceId:  int(data.GetSinceId()),
			Limit:    int(data.GetLimit()),
			Forward:  data.GetForward(),
		}

		if ranges := data.GetRanges(); len(ranges) > 0 {
			msg.Data.IdRanges = make([]MsgRange, len(ranges))
			for i, sr := range ranges {
				msg.Data.IdRanges[i].LowId = int(sr.GetLow())
				msg.Data.IdRanges[i].HiId = int(sr.GetHi())
			}
		}
	}
	if del := in.GetDel(); del != nil {
		msg.Del = &MsgGetOpts{
			BeforeId: int(del.GetBeforeId()),
			SinceId:  int(del.GetSinceId()),
			Limit:    int(del.GetLimit()),
			Forward:  del.GetForward(),
		}
		if ranges := del.GetRanges(); len(ranges) > 0 {
			msg.Del.IdRanges = make([]MsgRange, len(ranges))
			for i, sr := range ranges {
				msg.Del.IdRanges[i].LowId = int(sr.GetLow())
				msg.Del.IdRanges[i].HiId = int(sr.GetHi())
			}
		}
	}
	if search := in.GetSearch(); search != nil {
		msg.Search = &MsgSearchOpts{
			Query:   search.GetQuery(),
			Scope:   search.GetScope(),
			From:    search.GetFromUserId(),
			Kinds:   search.GetKinds(),
			MinDate: int64ToTime(search.GetMinDate()),
			MaxDate: int64ToTime(search.GetMaxDate()),
			Cursor:  search.GetCursor(),
			Limit:   int(search.GetLimit()),
		}
	}
	if contacts := in.GetContacts(); contacts != nil {
		msg.Contacts = &types.ContactQuery{
			Since:           contacts.GetSince(),
			Limit:           int(contacts.GetLimit()),
			Recommendations: contacts.GetRecommendations(),
		}
	}
	if assets := in.GetAssets(); assets != nil {
		msg.Assets = &types.AssetQuery{
			PackId:   assets.GetPackId(),
			Query:    assets.GetQuery(),
			Kind:     assets.GetKind(),
			Since:    assets.GetSince(),
			Limit:    int(assets.GetLimit()),
			AssetIds: append([]string(nil), assets.GetAssetIds()...),
		}
	}
	if readers := in.GetReaders(); readers != nil {
		msg.Readers = &MsgGetReaders{SeqId: int(readers.GetSeqId())}
	}
	if previews := in.GetPreviews(); previews != nil {
		msg.Previews = &MsgPreviewQuery{Topics: append([]string(nil), previews.GetTopics()...)}
	}

	return &msg
}

// pbSetDescSerialize 完成pbSetDescSerialize所需的内部处理。
func pbSetDescSerialize(in *MsgSetDesc) *pbx.SetDesc {
	if in == nil {
		return nil
	}

	if in.DefaultAcs != nil || in.Public != nil || in.Trusted != nil || in.Private != nil {
		return &pbx.SetDesc{
			DefaultAcs: pbDefaultAcsSerialize(in.DefaultAcs),
			Public:     interfaceToBytes(in.Public),
			Trusted:    interfaceToBytes(in.Trusted),
			Private:    interfaceToBytes(in.Private),
		}
	}

	return nil
}

// pbSetDescDeserialize 完成pbSetDescDeserialize所需的内部处理。
func pbSetDescDeserialize(in *pbx.SetDesc) *MsgSetDesc {
	if in == nil {
		return nil
	}

	defacs := pbDefaultAcsDeserialize(in.GetDefaultAcs())
	public := in.GetPublic()
	trusted := in.GetTrusted()
	private := in.GetPrivate()

	if defacs != nil || public != nil || private != nil || trusted != nil {
		return &MsgSetDesc{
			DefaultAcs: defacs,
			Public:     bytesToInterface(public),
			Trusted:    bytesToInterface(trusted),
			Private:    bytesToInterface(private),
		}
	}

	return nil
}

// pbSetQuerySerialize 完成pbSet查询Serialize所需的内部处理。
func pbSetQuerySerialize(in *MsgSetQuery) *pbx.SetQuery {
	if in == nil {
		return nil
	}

	out := &pbx.SetQuery{
		Desc: pbSetDescSerialize(in.Desc),
	}

	if in.Sub != nil {
		out.Sub = &pbx.SetSub{
			UserId: in.Sub.User,
			Mode:   in.Sub.Mode,
			Role:   in.Sub.Role,
		}
	}

	out.Tags = in.Tags

	out.Cred = pbClientCredSerialize(in.Cred)
	out.Aux = interfaceMapToByteMap(in.Aux)
	out.Contact = pbContactMutationSerialize(in.Contact)
	out.Asset = pbAssetMutationSerialize(in.Asset)

	return out
}

// pbSetQueryDeserialize 完成pbSet查询Deserialize所需的内部处理。
func pbSetQueryDeserialize(in *pbx.SetQuery) *MsgSetQuery {
	if in == nil {
		return nil
	}

	var msg *MsgSetQuery

	if desc := in.GetDesc(); desc != nil {
		msg = &MsgSetQuery{}
		msg.Desc = pbSetDescDeserialize(desc)
	}

	if sub := in.GetSub(); sub != nil {
		user := sub.GetUserId()
		mode := sub.GetMode()
		role := sub.GetRole()

		if user != "" || mode != "" || role != "" {
			if msg == nil {
				msg = &MsgSetQuery{}
			}

			msg.Sub = &MsgSetSub{
				User: sub.GetUserId(),
				Mode: sub.GetMode(),
				Role: sub.GetRole(),
			}
		}
	}

	if tags := in.GetTags(); tags != nil {
		if msg == nil {
			msg = &MsgSetQuery{}
		}
		msg.Tags = tags
	}

	if cred := in.GetCred(); cred != nil {
		if msg == nil {
			msg = &MsgSetQuery{}
		}
		msg.Cred = pbClientCredDeserialize(cred)
	}
	if aux := in.GetAux(); aux != nil {
		if msg == nil {
			msg = &MsgSetQuery{}
		}
		msg.Aux = byteMapToInterfaceMap(aux)
	}
	if contact := in.GetContact(); contact != nil {
		if msg == nil {
			msg = &MsgSetQuery{}
		}
		msg.Contact = pbContactMutationDeserialize(contact)
	}
	if asset := in.GetAsset(); asset != nil {
		if msg == nil {
			msg = &MsgSetQuery{}
		}
		msg.Asset = pbAssetMutationDeserialize(asset)
	}

	return msg
}
