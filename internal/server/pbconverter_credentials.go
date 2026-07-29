package server

import (
	"chat/api/pbx"
)

// pbClientCredSerialize 完成pb客户端凭据Serialize所需的内部处理。
func pbClientCredSerialize(in *MsgCredClient) *pbx.ClientCred {
	if in == nil {
		return nil
	}

	return &pbx.ClientCred{
		Method:   in.Method,
		Value:    in.Value,
		Response: in.Response,
		Params:   interfaceMapToByteMap(in.Params),
	}
}

// pbClientCredsSerialize 完成pb客户端CredsSerialize所需的内部处理。
func pbClientCredsSerialize(in []MsgCredClient) []*pbx.ClientCred {
	if in == nil {
		return nil
	}

	out := make([]*pbx.ClientCred, len(in))
	for i := range in {
		out[i] = pbClientCredSerialize(&in[i])
	}

	return out
}

// pbClientCredDeserialize 完成pb客户端凭据Deserialize所需的内部处理。
func pbClientCredDeserialize(in *pbx.ClientCred) *MsgCredClient {
	if in == nil {
		return nil
	}

	return &MsgCredClient{
		Method:   in.GetMethod(),
		Value:    in.GetValue(),
		Response: in.GetResponse(),
		Params:   byteMapToInterfaceMap(in.GetParams()),
	}
}

// pbClientCredsDeserialize 完成pb客户端CredsDeserialize所需的内部处理。
func pbClientCredsDeserialize(in []*pbx.ClientCred) []MsgCredClient {
	if in == nil {
		return nil
	}

	out := make([]MsgCredClient, len(in))
	for i, cr := range in {
		out[i] = *pbClientCredDeserialize(cr)
	}

	return out
}

// pbServerCredsSerialize 完成pb服务端CredsSerialize所需的内部处理。
func pbServerCredsSerialize(in []*MsgCredServer) []*pbx.ServerCred {
	if in == nil {
		return nil
	}

	out := make([]*pbx.ServerCred, len(in))
	for i, cr := range in {
		out[i] = &pbx.ServerCred{
			Method: cr.Method,
			Value:  cr.Value,
		}
	}

	return out
}

// pbServerCredsDeserialize 完成pb服务端CredsDeserialize所需的内部处理。
func pbServerCredsDeserialize(in []*pbx.ServerCred) []*MsgCredServer {
	if in == nil {
		return nil
	}

	out := make([]*MsgCredServer, len(in))
	for i, cr := range in {
		out[i] = &MsgCredServer{
			Method: cr.GetMethod(),
			Value:  cr.GetValue(),
			Done:   cr.GetDone(),
		}
	}

	return out
}
