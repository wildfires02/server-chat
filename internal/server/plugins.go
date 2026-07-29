// External services contacted through RPC
package server

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"chat/api/pbx"
	"chat/server/logs"
	"chat/server/store/types"
	"google.golang.org/grpc"
)

const (
	// plgHi 指定plgHi。
	plgHi = 1 << iota
	// plgAcc 指定plgAcc。
	plgAcc
	// plgLogin 指定plg登录。
	plgLogin
	// plgSub 指定plg订阅。
	plgSub
	// plgLeave 指定plgLeave。
	plgLeave
	// plgPub 指定plgPub。
	plgPub
	// plgGet 指定plgGet。
	plgGet
	// plgSet 指定plgSet。
	plgSet
	// plgDel 指定plgDel。
	plgDel
	// plgNote 指定plg事件通知。
	plgNote
	// plgData 指定plg数据。
	plgData
	// plgMeta 指定plg元数据。
	plgMeta
	// plgPres 指定plgPres。
	plgPres
	// plgInfo 指定plg通知。
	plgInfo

	// plgClientMask 指定plg客户端Mask。
	plgClientMask = plgHi | plgAcc | plgLogin | plgSub | plgLeave | plgPub | plgGet | plgSet | plgDel | plgNote
	// plgServerMask 指定plg服务端Mask。
	plgServerMask = plgData | plgMeta | plgPres | plgInfo
)

const (
	// plgActCreate 指定plgActCreate。
	plgActCreate = 1 << iota
	// plgActUpd 指定plgActUpd。
	plgActUpd
	// plgActDel 指定plgActDel。
	plgActDel

	// plgActMask 指定plgActMask。
	plgActMask = plgActCreate | plgActUpd | plgActDel
)

const (
	// plgTopicMe 指定plgTopicMe。
	plgTopicMe = 1 << iota
	// plgTopicFnd 指定plgTopicFnd。
	plgTopicFnd
	// plgTopicP2P 指定plgTopicP2P。
	plgTopicP2P
	// plgTopicGrp 指定plgTopicGrp。
	plgTopicGrp
	// plgTopicSys 指定plgTopicSys。
	plgTopicSys
	// plgTopicSlf 指定plgTopicSlf。
	plgTopicSlf
	// plgTopicNew 指定plgTopicNew。
	plgTopicNew
	// plgTopicNch 指定plgTopicNch。
	plgTopicNch

	// plgTopicCatMask 指定plgTopicCatMask。
	plgTopicCatMask = plgTopicMe | plgTopicFnd | plgTopicP2P | plgTopicGrp | plgTopicSys | plgTopicSlf
)

const (
	// plgFilterByTopicType 指定plg过滤条件ByTopicType。
	plgFilterByTopicType = 1 << iota
	// plgFilterByPacket 指定plg过滤条件ByPacket。
	plgFilterByPacket
	// plgFilterByAction 指定plg过滤条件ByAction。
	plgFilterByAction
)

var (
	// plgPacketNames 保存plgPacketNames的共享实例或运行状态。
	plgPacketNames = []string{
		"hi", "acc", "login", "sub", "leave", "pub", "get", "set", "del", "note",
		"data", "meta", "pres", "info",
	}

	// plgTopicCatNames 保存plgTopicCatNames的共享实例或运行状态。
	plgTopicCatNames = []string{"me", "fnd", "p2p", "grp", "sys", "slf", "new", "nch"}
)

// PluginFilter 是定义过滤类型的枚举。
type PluginFilter struct {
	// byPacket 保存byPacket。
	byPacket int
	// byTopicType 保存byTopicType。
	byTopicType int
	// byAction 保存byAction。
	byAction int
}

// ParsePluginFilter 解析过滤器配置字符串。
func ParsePluginFilter(s *string, filterBy int) (*PluginFilter, error) {
	if s == nil {
		return nil, nil
	}

	parseByName := func(parts []string, options []string, def int) (int, error) {
		var result int

		// 遍历过滤器各部分
		for _, inp := range parts {
			if inp != "" {
				inp = strings.ToLower(inp)
				// 拆分类似 "hi,login,pres" 或 "me,p2p,fnd" 的字符串
				values := strings.Split(inp, ",")
				// 对于输入字符串中的每个值，尝试在选项集中查找
				for _, val := range values {
					i := 0
					// 遍历选项，即在数据包名称切片中查找 "hi"
					for i = range options {
						if options[i] == val {
							result |= 1 << uint(i)
							break
						}
					}

					if result != 0 && i == len(options) {
						// 输入中包含已知和未知的混合选项
						return 0, errors.New("plugin: unknown value in filter " + val)
					}
				}

				if result != 0 {
					// 已找到并正确解析
					break
				}
			}
		}

		// 如果过滤器值未定义，使用默认值。
		if result == 0 {
			result = def
		}

		return result, nil
	}

	parseAction := func(parts []string) int {
		var result int
		for _, inp := range parts {
		Loop:
			for _, char := range inp {
				switch char {
				case 'c', 'C':
					result |= plgActCreate
				case 'u', 'U':
					result |= plgActUpd
				case 'd', 'D':
					result |= plgActDel
				default:
					// 未知符号，说明这不是动作字符串。
					result = 0
					break Loop
				}
			}

			if result != 0 {
				// 已找到并解析了动作。
				break
			}
		}
		if result == 0 {
			result = plgActMask
		}
		return result
	}

	filter := PluginFilter{}
	parts := strings.Split(*s, ";")
	var err error

	if filterBy&plgFilterByPacket != 0 {
		if filter.byPacket, err = parseByName(parts, plgPacketNames, plgClientMask); err != nil {
			return nil, err
		}
	}

	if filterBy&plgFilterByTopicType != 0 {
		if filter.byTopicType, err = parseByName(parts, plgTopicCatNames, plgTopicCatMask); err != nil {
			return nil, err
		}
	}

	if filterBy&plgFilterByAction != 0 {
		filter.byAction = parseAction(parts)
	}

	return &filter, nil
}

// pluginRPCFilterConfig 定义单个 RPC 调用的过滤器。过滤器字符串格式如下：
// <逗号分隔的数据包名称列表> ; <逗号分隔的 Topic 或 Topic 类型列表> ; <动作（C U D 的组合）>
// 例如：
// "acc,login;;CU" - 捕获 {acc} 或 {login} 数据包；不按 Topic 过滤，Create 或 Update 动作
// "pub,pres;me,p2p;"
type pluginRPCFilterConfig struct {
	// 按数据包名称、Topic 类型过滤 [或精确名称 - 暂不支持]。二维："pub,pres;p2p,me"
	FireHose *string `json:"fire_hose"`

	// 按 CUD 过滤，[精确用户名 - 暂不支持]。一维："C"
	Account *string `json:"account"`
	// 按 CUD、Topic 类型 [、精确名称] 过滤："p2p;CU"
	Topic *string `json:"topic"`
	// 按 CUD、Topic 类型 [、精确 Topic 名称、精确用户名] 过滤："CU"
	Subscription *string `json:"subscription"`
	// 按 C.D、Topic 类型 [、精确 Topic 名称、精确用户名] 过滤："grp;CD"
	Message *string `json:"message"`

	// 调用 Find 服务，true 或 false
	Find bool
}

// pluginConfig 保存plugin配置的数据和运行状态。
type pluginConfig struct {
	// Enabled 指示是否启用或满足Enabled。
	Enabled bool `json:"enabled"`
	// 唯一的服务名称
	Name string `json:"name"`
	// 超时等待的微秒数
	Timeout int64 `json:"timeout"`
	// RPC 调用的过滤器：何时调用 vs 何时跳过
	Filters pluginRPCFilterConfig `json:"filters"`
	// 插件失败时服务器应做什么：HTTP 错误码
	FailureCode int `json:"failure_code"`
	// 与错误码配套的 HTTP 错误消息
	FailureMessage string `json:"failure_text"`
	// 插件服务器地址，格式为 "tcp://localhost:123" 或 "unix://path_to_socket_file"
	ServiceAddr string `json:"service_addr"`
}

// Plugin 定义 gRPC 插件的客户端参数。
type Plugin struct {
	// name 保存名称。
	name string
	// timeout 保存超时时间。
	timeout time.Duration
	// 各方法的过滤器
	filterFireHose *PluginFilter
	// filterAccount 保存过滤条件Account。
	filterAccount *PluginFilter
	// filterTopic 保存过滤条件Topic。
	filterTopic *PluginFilter
	// filterSubscription 保存过滤条件订阅。
	filterSubscription *PluginFilter
	// filterMessage 保存过滤条件消息。
	filterMessage *PluginFilter
	// filterFind 保存过滤条件Find。
	filterFind bool
	// failureCode 保存failureCode。
	failureCode int
	// failureText 保存failureText。
	failureText string
	// network 保存network。
	network string
	// addr 保存addr。
	addr string

	// conn 保存连接。
	conn *grpc.ClientConn
	// client 保存客户端。
	client pbx.PluginClient
}

// pluginsInit 完成pluginsInit所需的内部处理。
func pluginsInit(configString json.RawMessage) {
	// 检查是否定义了任何插件
	if len(configString) == 0 {
		return
	}

	var config []pluginConfig
	if err := json.Unmarshal(configString, &config); err != nil {
		logs.Err.Fatal(err)
	}

	nameIndex := make(map[string]bool)
	globals.plugins = make([]Plugin, len(config))
	count := 0
	for i := range config {
		conf := &config[i]
		if !conf.Enabled {
			continue
		}

		if nameIndex[conf.Name] {
			logs.Err.Fatalf("plugins: duplicate name '%s'", conf.Name)
		}

		globals.plugins[count] = Plugin{
			name:        conf.Name,
			timeout:     time.Duration(conf.Timeout) * time.Microsecond,
			failureCode: conf.FailureCode,
			failureText: conf.FailureMessage,
		}
		var err error
		if globals.plugins[count].filterFireHose, err =
			ParsePluginFilter(conf.Filters.FireHose, plgFilterByTopicType|plgFilterByPacket); err != nil {
			logs.Err.Fatal("plugins: bad FireHose filter", err)
		}
		if globals.plugins[count].filterAccount, err =
			ParsePluginFilter(conf.Filters.Account, plgFilterByAction); err != nil {
			logs.Err.Fatal("plugins: bad Account filter", err)
		}
		if globals.plugins[count].filterTopic, err =
			ParsePluginFilter(conf.Filters.Topic, plgFilterByTopicType|plgFilterByAction); err != nil {
			logs.Err.Fatal("plugins: bad Topic filter", err)
		}
		if globals.plugins[count].filterSubscription, err =
			ParsePluginFilter(conf.Filters.Subscription, plgFilterByTopicType|plgFilterByAction); err != nil {
			logs.Err.Fatal("plugins: bad Subscription filter", err)
		}
		if globals.plugins[count].filterMessage, err =
			ParsePluginFilter(conf.Filters.Message, plgFilterByTopicType|plgFilterByAction); err != nil {
			logs.Err.Fatal("plugins: bad Message filter", err)
		}

		globals.plugins[count].filterFind = conf.Filters.Find

		if parts := strings.SplitN(conf.ServiceAddr, "://", 2); len(parts) < 2 {
			logs.Err.Fatal("plugins: invalid server address format", conf.ServiceAddr)
		} else {
			globals.plugins[count].network = parts[0]
			globals.plugins[count].addr = parts[1]
		}

		globals.plugins[count].conn, err = grpc.Dial(globals.plugins[count].addr, grpc.WithInsecure())
		if err != nil {
			logs.Err.Fatalf("plugins: connection failure %v", err)
		}

		globals.plugins[count].client = pbx.NewPluginClient(globals.plugins[count].conn)

		nameIndex[conf.Name] = true
		count++
	}

	globals.plugins = globals.plugins[:count]
	if len(globals.plugins) == 0 {
		logs.Info.Println("plugins: no active plugins found")
		globals.plugins = nil
	} else {
		var names []string
		for i := range globals.plugins {
			names = append(names, globals.plugins[i].name+"("+globals.plugins[i].addr+")")
		}

		logs.Info.Println("plugins: active", "'"+strings.Join(names, "', '")+"'")
	}
}

// pluginsShutdown 完成pluginsShutdown所需的内部处理。
func pluginsShutdown() {
	if globals.plugins == nil {
		return
	}

	for i := range globals.plugins {
		globals.plugins[i].conn.Close()
	}
}

// pluginGenerateClientReq 完成pluginGenerate客户端Req所需的内部处理。
func pluginGenerateClientReq(sess *Session, msg *ClientComMessage) *pbx.ClientReq {
	cmsg := pbCliSerialize(msg)
	if cmsg == nil {
		return nil
	}
	return &pbx.ClientReq{
		Msg: cmsg,
		Sess: &pbx.Session{
			SessionId:  sess.sid,
			UserId:     sess.uid.UserId(),
			AuthLevel:  pbx.AuthLevel(sess.authLvl),
			UserAgent:  sess.userAgent,
			RemoteAddr: sess.remoteAddr,
			DeviceId:   sess.deviceID,
			Language:   sess.lang,
		},
	}
}

// pluginFireHose 完成pluginFireHose所需的内部处理。
func pluginFireHose(sess *Session, msg *ClientComMessage) (*ClientComMessage, *ServerComMessage) {
	if globals.plugins == nil {
		// 返回原始消息以继续处理，不做修改
		return msg, nil
	}

	var req *pbx.ClientReq

	id, topic := pluginIDAndTopic(msg)
	ts := time.Now().UTC().Round(time.Millisecond)
	for i := range globals.plugins {
		p := &globals.plugins[i]
		if !pluginDoFiltering(p.filterFireHose, msg) {
			// 插件对 FireHose 不感兴趣
			continue
		}

		if req == nil {
			// 仅在需要时生成请求
			req = pluginGenerateClientReq(sess, msg)
			if req == nil {
				// 失败：序列化消息。很可能消息无效。
				break
			}
		}

		var ctx context.Context
		var cancel context.CancelFunc
		if p.timeout > 0 {
			ctx, cancel = context.WithTimeout(context.Background(), p.timeout)
			defer cancel()
		} else {
			ctx = context.Background()
		}
		if resp, err := p.client.FireHose(ctx, req); err == nil {
			respStatus := resp.GetStatus()
			// CONTINUE 表示默认处理
			if respStatus == pbx.RespCode_CONTINUE {
				continue
			}
			// DROP 表示停止处理该消息
			if respStatus == pbx.RespCode_DROP {
				return nil, nil
			}
			// REPLACE：ClientMsg 被插件更新，使用新消息继续处理。
			if respStatus == pbx.RespCode_REPLACE {
				return pbCliDeserialize(resp.GetClmsg()), nil
			}

			// RESPOND：插件提供了替代响应消息，使用它。
			return nil, pbServDeserialize(resp.GetSrvmsg())

		} else if p.failureCode != 0 {
			// 插件失败且已配置为停止后续处理。
			logs.Err.Println("plugin: failed,", p.name, err)
			return nil, &ServerComMessage{
				Ctrl: &MsgServerCtrl{
					Id:        id,
					Code:      p.failureCode,
					Text:      p.failureText,
					Topic:     topic,
					Timestamp: ts,
				},
			}
		} else {
			// 插件失败但已配置为忽略失败。
			logs.Warn.Println("plugin: failure ignored,", p.name, err)
		}
	}

	return msg, nil
}

// 请求插件执行搜索。
func pluginFind(user types.Uid, query string) (string, []types.Subscription, error) {
	if globals.plugins == nil {
		return query, nil, nil
	}

	find := &pbx.SearchQuery{
		UserId: user.UserId(),
		Query:  query,
	}
	for i := range globals.plugins {
		p := &globals.plugins[i]
		if !p.filterFind {
			// 插件无法处理 Find 请求
			continue
		}

		var ctx context.Context
		var cancel context.CancelFunc
		if p.timeout > 0 {
			ctx, cancel = context.WithTimeout(context.Background(), p.timeout)
			defer cancel()
		} else {
			ctx = context.Background()
		}
		resp, err := p.client.Find(ctx, find)
		if err != nil {
			logs.Warn.Println("plugins: Find call failed", p.name, err)
			return "", nil, err
		}
		respStatus := resp.GetStatus()
		// CONTINUE means default processing
		if respStatus == pbx.RespCode_CONTINUE {
			continue
		}
		// DROP 表示停止处理该请求
		if respStatus == pbx.RespCode_DROP {
			return "", nil, nil
		}
		// REPLACE：查询字符串已更改，使用新字符串继续处理。
		if respStatus == pbx.RespCode_REPLACE {
			return resp.GetQuery(), nil, nil
		}
		// RESPOND：插件提供了具体响应，使用它。
		return "", pbSubSliceDeserialize(resp.GetResult()), nil
	}

	return query, nil, nil
}

// pluginAccount 完成pluginAccount所需的内部处理。
func pluginAccount(user *types.User, action int) {
	if globals.plugins == nil {
		return
	}

	var event *pbx.AccountEvent
	for i := range globals.plugins {
		p := &globals.plugins[i]
		if p.filterAccount == nil || p.filterAccount.byAction&action == 0 {
			// 插件对 Account 动作不感兴趣
			continue
		}

		if event == nil {
			event = &pbx.AccountEvent{
				Action: pluginActionToCrud(action),
				UserId: user.Uid().UserId(),
				DefaultAcs: pbDefaultAcsSerialize(&MsgDefaultAcsMode{
					Auth: user.Access.Auth.String(),
					Anon: user.Access.Anon.String(),
				}),
				Public: interfaceToBytes(user.Public),
				Tags:   user.Tags,
			}
		}

		var ctx context.Context
		var cancel context.CancelFunc
		if p.timeout > 0 {
			ctx, cancel = context.WithTimeout(context.Background(), p.timeout)
			defer cancel()
		} else {
			ctx = context.Background()
		}
		if _, err := p.client.Account(ctx, event); err != nil {
			logs.Warn.Println("plugins: Account call failed", p.name, err)
		}
	}
}

// pluginTopic 完成pluginTopic所需的内部处理。
func pluginTopic(topic *Topic, action int) {
	if globals.plugins == nil {
		return
	}

	var event *pbx.TopicEvent
	for i := range globals.plugins {
		p := &globals.plugins[i]
		if p.filterTopic == nil || p.filterTopic.byAction&action == 0 {
			// 插件对 Topic 动作不感兴趣
			continue
		}

		if event == nil {
			event = &pbx.TopicEvent{
				Action: pluginActionToCrud(action),
				Name:   topic.name,
				Desc:   pbTopicSerializeToDesc(topic),
			}
		}

		var ctx context.Context
		var cancel context.CancelFunc
		if p.timeout > 0 {
			ctx, cancel = context.WithTimeout(context.Background(), p.timeout)
			defer cancel()
		} else {
			ctx = context.Background()
		}
		if _, err := p.client.Topic(ctx, event); err != nil {
			logs.Warn.Println("plugins: Topic call failed", p.name, err)
		}
	}
}

// pluginSubscription 完成plugin订阅所需的内部处理。
func pluginSubscription(sub *types.Subscription, action int) {
	if globals.plugins == nil {
		return
	}

	var event *pbx.SubscriptionEvent
	for i := range globals.plugins {
		p := &globals.plugins[i]
		if p.filterSubscription == nil || p.filterSubscription.byAction&action == 0 {
			// 插件对 Subscription 动作不感兴趣
			continue
		}

		if event == nil {
			event = &pbx.SubscriptionEvent{
				Action: pluginActionToCrud(action),
				Topic:  sub.Topic,
				UserId: sub.User,

				DelId:  int32(sub.DelId),
				ReadId: int32(sub.ReadSeqId),
				RecvId: int32(sub.RecvSeqId),

				Mode: &pbx.AccessMode{
					Want:  sub.ModeWant.String(),
					Given: sub.ModeGiven.String(),
				},

				Private: interfaceToBytes(sub.Private),
			}
		}

		var ctx context.Context
		var cancel context.CancelFunc
		if p.timeout > 0 {
			ctx, cancel = context.WithTimeout(context.Background(), p.timeout)
			defer cancel()
		} else {
			ctx = context.Background()
		}
		if _, err := p.client.Subscription(ctx, event); err != nil {
			logs.Warn.Println("plugins: Subscription call failed", p.name, err)
		}
	}
}

// 消息已接受投递
func pluginMessage(data *MsgServerData, action int) {
	if globals.plugins == nil || action != plgActCreate {
		return
	}

	var event *pbx.MessageEvent
	for i := range globals.plugins {
		p := &globals.plugins[i]
		if p.filterMessage == nil || p.filterMessage.byAction&action == 0 {
			// 插件对 Message 动作不感兴趣
			continue
		}

		if event == nil {
			event = &pbx.MessageEvent{
				Action: pluginActionToCrud(action),
				Msg:    pbServDataSerialize(data).Data,
			}
		}

		var ctx context.Context
		var cancel context.CancelFunc
		if p.timeout > 0 {
			ctx, cancel = context.WithTimeout(context.Background(), p.timeout)
			defer cancel()
		} else {
			ctx = context.Background()
		}
		if _, err := p.client.Message(ctx, event); err != nil {
			logs.Warn.Println("plugins: Message call failed", p.name, err)
		}
	}
}

// 返回 false 表示跳过，true 表示处理
func pluginDoFiltering(filter *PluginFilter, msg *ClientComMessage) bool {
	filterByTopic := func(topic string, flt int) bool {
		if topic == "" || flt == plgTopicCatMask {
			return true
		}

		tt := topic
		if len(tt) > 3 {
			tt = topic[:3]
		}
		switch tt {
		case "me":
			return flt&plgTopicMe != 0
		case "fnd":
			return flt&plgTopicFnd != 0
		case "usr":
			return flt&plgTopicP2P != 0
		case "grp":
			return flt&plgTopicGrp != 0
		case "sys":
			return flt&plgTopicSys != 0
		case "slf":
			return flt&plgTopicSlf != 0
		case "new":
			return flt&plgTopicNew != 0
		case "nch":
			return flt&plgTopicNch != 0
		}
		return false
	}

	// 检查插件是否对此调用有任何过滤器
	if filter == nil || filter.byPacket == 0 {
		return false
	}
	// 检查插件是否需要所有消息
	if filter.byPacket == plgClientMask && filter.byTopicType == plgTopicCatMask {
		return true
	}
	// 检查各个位
	if msg.Hi != nil {
		return filter.byPacket&plgHi != 0
	}
	if msg.Acc != nil {
		return filter.byPacket&plgAcc != 0
	}
	if msg.Login != nil {
		return filter.byPacket&plgLogin != 0
	}
	if msg.Sub != nil {
		return filter.byPacket&plgSub != 0 && filterByTopic(msg.Sub.Topic, filter.byTopicType)
	}
	if msg.Leave != nil {
		return filter.byPacket&plgLeave != 0 && filterByTopic(msg.Leave.Topic, filter.byTopicType)
	}
	if msg.Pub != nil {
		return filter.byPacket&plgPub != 0 && filterByTopic(msg.Pub.Topic, filter.byTopicType)
	}
	if msg.Get != nil {
		return filter.byPacket&plgGet != 0 && filterByTopic(msg.Get.Topic, filter.byTopicType)
	}
	if msg.Set != nil {
		return filter.byPacket&plgSet != 0 && filterByTopic(msg.Set.Topic, filter.byTopicType)
	}
	if msg.Del != nil {
		return filter.byPacket&plgDel != 0 && filterByTopic(msg.Del.Topic, filter.byTopicType)
	}
	if msg.Note != nil {
		return filter.byPacket&plgNote != 0 && filterByTopic(msg.Note.Topic, filter.byTopicType)
	}
	return false
}

// pluginActionToCrud 完成pluginActionToCrud所需的内部处理。
func pluginActionToCrud(action int) pbx.Crud {
	switch action {
	case plgActCreate:
		return pbx.Crud_CREATE
	case plgActUpd:
		return pbx.Crud_UPDATE
	case plgActDel:
		return pbx.Crud_DELETE
	}
	panic("plugin: unknown action")
}

// pluginIDAndTopic 提取消息 ID 和 Topic 名称。
func pluginIDAndTopic(msg *ClientComMessage) (string, string) {
	if msg.Hi != nil {
		return msg.Hi.Id, ""
	}
	if msg.Acc != nil {
		return msg.Acc.Id, ""
	}
	if msg.Login != nil {
		return msg.Login.Id, ""
	}
	if msg.Sub != nil {
		return msg.Sub.Id, msg.Sub.Topic
	}
	if msg.Leave != nil {
		return msg.Leave.Id, msg.Leave.Topic
	}
	if msg.Pub != nil {
		return msg.Pub.Id, msg.Pub.Topic
	}
	if msg.Get != nil {
		return msg.Get.Id, msg.Get.Topic
	}
	if msg.Set != nil {
		return msg.Set.Id, msg.Set.Topic
	}
	if msg.Del != nil {
		return msg.Del.Id, msg.Del.Topic
	}
	if msg.Note != nil {
		return "", msg.Note.Topic
	}
	return "", ""
}
