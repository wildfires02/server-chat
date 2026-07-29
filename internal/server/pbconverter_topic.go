package server

import (
	"chat/api/pbx"
	"chat/server/store/types"
)

// pbTopicDescSerialize 完成pbTopicDescSerialize所需的内部处理。
func pbTopicDescSerialize(desc *MsgTopicDesc) *pbx.TopicDesc {
	if desc == nil {
		return nil
	}
	out := &pbx.TopicDesc{
		CreatedAt: timeToInt64(desc.CreatedAt),
		UpdatedAt: timeToInt64(desc.UpdatedAt),
		TouchedAt: timeToInt64(desc.TouchedAt),
		State:     desc.State,
		Online:    desc.Online,
		IsChan:    desc.IsChan,
		Defacs:    pbDefaultAcsSerialize(desc.DefaultAcs),
		Acs:       pbAccessModeSerialize(desc.Acs),
		SeqId:     int32(desc.SeqId),
		ReadId:    int32(desc.ReadSeqId),
		RecvId:    int32(desc.RecvSeqId),
		DelId:     int32(desc.DelId),
		SubCount:  int32(desc.SubCnt),
		Public:    interfaceToBytes(desc.Public),
		Trusted:   interfaceToBytes(desc.Trusted),
		Private:   interfaceToBytes(desc.Private),
	}
	if desc.LastSeen != nil {
		out.LastSeenTime = timeToInt64(desc.LastSeen.When)
		out.LastSeenUserAgent = desc.LastSeen.UserAgent
	}
	return out
}

// pbTopicDescDeserialize 完成pbTopicDescDeserialize所需的内部处理。
func pbTopicDescDeserialize(desc *pbx.TopicDesc) *MsgTopicDesc {
	if desc == nil {
		return nil
	}
	out := &MsgTopicDesc{
		CreatedAt:  int64ToTime(desc.GetCreatedAt()),
		UpdatedAt:  int64ToTime(desc.GetUpdatedAt()),
		TouchedAt:  int64ToTime(desc.GetTouchedAt()),
		State:      desc.GetState(),
		Online:     desc.GetOnline(),
		IsChan:     desc.GetIsChan(),
		DefaultAcs: pbDefaultAcsDeserialize(desc.GetDefacs()),
		Acs:        pbAccessModeDeserialize(desc.GetAcs()),
		SeqId:      int(desc.SeqId),
		ReadSeqId:  int(desc.ReadId),
		RecvSeqId:  int(desc.RecvId),
		DelId:      int(desc.DelId),
		SubCnt:     int(desc.SubCount),
		Public:     bytesToInterface(desc.Public),
		Trusted:    bytesToInterface(desc.Trusted),
		Private:    bytesToInterface(desc.Private),
	}

	if desc.GetLastSeenTime() > 0 {
		out.LastSeen = &MsgLastSeenInfo{
			When:      int64ToTime(desc.GetLastSeenTime()),
			UserAgent: desc.GetLastSeenUserAgent(),
		}
	}
	return out
}

// pbTopicSerializeToDesc 完成pbTopicSerializeToDesc所需的内部处理。
func pbTopicSerializeToDesc(topic *Topic) *pbx.TopicDesc {
	if topic == nil {
		return nil
	}
	return &pbx.TopicDesc{
		CreatedAt: timeToInt64(&topic.created),
		UpdatedAt: timeToInt64(&topic.updated),
		IsChan:    topic.isChan,
		Defacs: &pbx.DefaultAcsMode{
			Auth: topic.accessAuth.String(),
			Anon: topic.accessAnon.String(),
		},
		SeqId:    int32(topic.lastID),
		DelId:    int32(topic.delID),
		SubCount: int32(topic.subCnt),
		Public:   interfaceToBytes(topic.public),
		Trusted:  interfaceToBytes(topic.trusted),
	}
}

// pbTopicSubSliceSerialize 完成pbTopic订阅SliceSerialize所需的内部处理。
func pbTopicSubSliceSerialize(subs []MsgTopicSub) []*pbx.TopicSub {
	if len(subs) == 0 {
		return nil
	}

	out := make([]*pbx.TopicSub, len(subs))
	for i := range subs {
		out[i] = pbTopicSubSerialize(&subs[i])
	}
	return out
}

// pbTopicSubSerialize 完成pbTopic订阅Serialize所需的内部处理。
func pbTopicSubSerialize(sub *MsgTopicSub) *pbx.TopicSub {
	out := &pbx.TopicSub{
		UpdatedAt: timeToInt64(sub.UpdatedAt),
		DeletedAt: timeToInt64(sub.DeletedAt),
		Online:    sub.Online,
		Acs:       pbAccessModeSerialize(&sub.Acs),
		ReadId:    int32(sub.ReadSeqId),
		RecvId:    int32(sub.RecvSeqId),
		Public:    interfaceToBytes(sub.Public),
		Trusted:   interfaceToBytes(sub.Trusted),
		Private:   interfaceToBytes(sub.Private),
		UserId:    sub.User,
		Topic:     sub.Topic,
		TouchedAt: timeToInt64(sub.TouchedAt),
		SeqId:     int32(sub.SeqId),
		DelId:     int32(sub.DelId),
		SubCount:  int32(sub.SubCnt),
	}
	if sub.LastSeen != nil {
		out.LastSeenTime = timeToInt64(sub.LastSeen.When)
		out.LastSeenUserAgent = sub.LastSeen.UserAgent
	}
	return out
}

// pbTopicSubSliceDeserialize 完成pbTopic订阅SliceDeserialize所需的内部处理。
func pbTopicSubSliceDeserialize(subs []*pbx.TopicSub) []MsgTopicSub {
	if len(subs) == 0 {
		return nil
	}

	out := make([]MsgTopicSub, len(subs))
	for i := range subs {
		out[i] = MsgTopicSub{
			UpdatedAt: int64ToTime(subs[i].GetUpdatedAt()),
			DeletedAt: int64ToTime(subs[i].GetDeletedAt()),
			Online:    subs[i].GetOnline(),
			ReadSeqId: int(subs[i].GetReadId()),
			RecvSeqId: int(subs[i].GetRecvId()),
			Public:    bytesToInterface(subs[i].GetPublic()),
			Trusted:   bytesToInterface(subs[i].GetTrusted()),
			Private:   bytesToInterface(subs[i].GetPrivate()),
			User:      subs[i].GetUserId(),
			Topic:     subs[i].GetTopic(),
			TouchedAt: int64ToTime(subs[i].GetTouchedAt()),
			SeqId:     int(subs[i].GetSeqId()),
			DelId:     int(subs[i].GetDelId()),
			SubCnt:    int(subs[i].GetSubCount()),
		}
		if acs := subs[i].GetAcs(); acs != nil {
			out[i].Acs = *pbAccessModeDeserialize(acs)
		}
		if subs[i].GetLastSeenTime() > 0 {
			out[i].LastSeen = &MsgLastSeenInfo{
				When:      int64ToTime(subs[i].GetLastSeenTime()),
				UserAgent: subs[i].GetLastSeenUserAgent(),
			}
		}
	}
	return out
}

// pbSubSliceDeserialize 完成pb订阅SliceDeserialize所需的内部处理。
func pbSubSliceDeserialize(subs []*pbx.TopicSub) []types.Subscription {
	if len(subs) == 0 {
		return nil
	}

	out := make([]types.Subscription, len(subs))
	for i := range subs {
		out[i] = types.Subscription{
			ObjHeader: types.ObjHeader{
				UpdatedAt: *int64ToTime(subs[i].GetUpdatedAt()),
			},
			DeletedAt: int64ToTime(subs[i].GetDeletedAt()),
			User:      subs[i].GetUserId(),
			Topic:     subs[i].GetTopic(),
			DelId:     int(subs[i].GetDelId()),
			Private:   bytesToInterface(subs[i].GetPrivate()),
		}
		out[i].SetPublic(bytesToInterface(subs[i].GetPublic()))
		out[i].SetTrusted(bytesToInterface(subs[i].GetTrusted()))
		if acs := subs[i].GetAcs(); acs != nil {
			out[i].ModeGiven.UnmarshalText([]byte(acs.GetGiven()))
			out[i].ModeWant.UnmarshalText([]byte(acs.GetWant()))
		}
		if subs[i].GetLastSeenTime() > 0 {
			out[i].SetLastSeenAndUA(int64ToTime(subs[i].GetLastSeenTime()),
				subs[i].GetLastSeenUserAgent())
		}
	}
	return out
}
