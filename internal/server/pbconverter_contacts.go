package server

import (
	"chat/api/pbx"
	"chat/server/store/types"
)

func pbContactSerialize(in types.AddressBookContact) *pbx.Contact {
	return &pbx.Contact{
		UserId:    in.User,
		Alias:     in.Alias,
		Groups:    in.Groups,
		Status:    string(in.Status),
		CreatedAt: timeToInt64(&in.CreatedAt),
		UpdatedAt: timeToInt64(&in.UpdatedAt),
		Version:   in.Version,
		Request:   in.Request,
	}
}

func pbContactDeserialize(in *pbx.Contact) *types.AddressBookContact {
	if in == nil {
		return nil
	}
	out := &types.AddressBookContact{
		User:    in.GetUserId(),
		Alias:   in.GetAlias(),
		Groups:  in.GetGroups(),
		Status:  types.ContactStatus(in.GetStatus()),
		Version: in.GetVersion(),
		Request: in.GetRequest(),
	}
	if ts := int64ToTime(in.GetCreatedAt()); ts != nil {
		out.CreatedAt = *ts
	}
	if ts := int64ToTime(in.GetUpdatedAt()); ts != nil {
		out.UpdatedAt = *ts
	}
	return out
}

func pbContactGroupSerialize(in types.ContactGroup) *pbx.ContactGroup {
	return &pbx.ContactGroup{
		Id:        in.Id,
		Name:      in.Name,
		CreatedAt: timeToInt64(&in.CreatedAt),
		UpdatedAt: timeToInt64(&in.UpdatedAt),
		Version:   in.Version,
	}
}

func pbContactGroupDeserialize(in *pbx.ContactGroup) *types.ContactGroup {
	if in == nil {
		return nil
	}
	out := &types.ContactGroup{Id: in.GetId(), Name: in.GetName(), Version: in.GetVersion()}
	if ts := int64ToTime(in.GetCreatedAt()); ts != nil {
		out.CreatedAt = *ts
	}
	if ts := int64ToTime(in.GetUpdatedAt()); ts != nil {
		out.UpdatedAt = *ts
	}
	return out
}

func pbContactMutationSerialize(in *types.ContactMutation) *pbx.ContactMutation {
	if in == nil {
		return nil
	}
	out := &pbx.ContactMutation{
		Op:      in.Op,
		UserId:  in.User,
		GroupId: in.GroupId,
	}
	if in.Contact != nil {
		out.Contact = pbContactSerialize(*in.Contact)
	}
	if in.Group != nil {
		out.Group = pbContactGroupSerialize(*in.Group)
	}
	return out
}

func pbContactMutationDeserialize(in *pbx.ContactMutation) *types.ContactMutation {
	if in == nil {
		return nil
	}
	return &types.ContactMutation{
		Op:      in.GetOp(),
		Contact: pbContactDeserialize(in.GetContact()),
		Group:   pbContactGroupDeserialize(in.GetGroup()),
		User:    in.GetUserId(),
		GroupId: in.GetGroupId(),
	}
}

func pbContactSnapshotSerialize(in *types.ContactSnapshot) *pbx.ContactSnapshot {
	if in == nil {
		return nil
	}
	out := &pbx.ContactSnapshot{Version: in.Version, Reset_: in.Reset}
	for _, contact := range in.Contacts {
		out.Contacts = append(out.Contacts, pbContactSerialize(contact))
	}
	for _, group := range in.Groups {
		out.Groups = append(out.Groups, pbContactGroupSerialize(group))
	}
	for _, event := range in.Events {
		out.Events = append(out.Events, &pbx.ContactEvent{
			Version: event.Version,
			Type:    event.Type,
			Id:      event.Id,
			At:      timeToInt64(&event.At),
		})
	}
	for _, recommendation := range in.Recommendations {
		out.Recommendations = append(out.Recommendations, &pbx.ContactRecommendation{
			UserId:        recommendation.User,
			MutualFriends: int32(recommendation.MutualFriends),
		})
	}
	return out
}

func pbContactSnapshotDeserialize(in *pbx.ContactSnapshot) *types.ContactSnapshot {
	if in == nil {
		return nil
	}
	out := &types.ContactSnapshot{Version: in.GetVersion(), Reset: in.GetReset_()}
	for _, contact := range in.GetContacts() {
		if converted := pbContactDeserialize(contact); converted != nil {
			out.Contacts = append(out.Contacts, *converted)
		}
	}
	for _, group := range in.GetGroups() {
		if converted := pbContactGroupDeserialize(group); converted != nil {
			out.Groups = append(out.Groups, *converted)
		}
	}
	for _, event := range in.GetEvents() {
		converted := types.ContactEvent{
			Version: event.GetVersion(),
			Type:    event.GetType(),
			Id:      event.GetId(),
		}
		if ts := int64ToTime(event.GetAt()); ts != nil {
			converted.At = *ts
		}
		out.Events = append(out.Events, converted)
	}
	for _, recommendation := range in.GetRecommendations() {
		out.Recommendations = append(out.Recommendations, types.ContactRecommendation{
			User:          recommendation.GetUserId(),
			MutualFriends: int(recommendation.GetMutualFriends()),
		})
	}
	return out
}
