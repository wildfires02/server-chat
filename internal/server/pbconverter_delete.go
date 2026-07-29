package server

import (
	"chat/api/pbx"
)

// pbDelQuerySerialize 完成pbDel查询Serialize所需的内部处理。
func pbDelQuerySerialize(in []MsgRange) []*pbx.SeqRange {
	if in == nil {
		return nil
	}

	out := make([]*pbx.SeqRange, len(in))
	for i, dq := range in {
		out[i] = &pbx.SeqRange{Low: int32(dq.LowId), Hi: int32(dq.HiId)}
	}

	return out
}

// pbDelQueryDeserialize 完成pbDel查询Deserialize所需的内部处理。
func pbDelQueryDeserialize(in []*pbx.SeqRange) []MsgRange {
	if in == nil {
		return nil
	}

	out := make([]MsgRange, len(in))
	for i, sr := range in {
		out[i].LowId = int(sr.GetLow())
		out[i].HiId = int(sr.GetHi())
	}

	return out
}

// pbDelValuesSerialize 完成pbDelValuesSerialize所需的内部处理。
func pbDelValuesSerialize(in *MsgDelValues) *pbx.DelValues {
	if in == nil {
		return nil
	}

	return &pbx.DelValues{
		DelId:  int32(in.DelId),
		DelSeq: pbDelQuerySerialize(in.DelSeq),
	}
}

// pbDelValuesDeserialize 完成pbDelValuesDeserialize所需的内部处理。
func pbDelValuesDeserialize(in *pbx.DelValues) *MsgDelValues {
	if in == nil {
		return nil
	}

	return &MsgDelValues{
		DelId:  int(in.GetDelId()),
		DelSeq: pbDelQueryDeserialize(in.GetDelSeq()),
	}
}
