// Package fcm 提供消息推送实现。
package fcm

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	fcmv1 "google.golang.org/api/fcm/v1"

	"chat/server/drafty"
	"chat/server/logs"
	"chat/server/push"
	"chat/server/push/common"
	"chat/server/store"
	t "chat/server/store/types"
	"maps"
)

const (
	// VOIP 推送通知的 TTL（秒）。
	voipTimeToLive = 10
	// 普通推送通知的 TTL（秒）。
	defaultTimeToLive = 3600
)

// payloadToData 完成载荷To数据所需的内部处理。
func payloadToData(pl *push.Payload) (map[string]string, error) {
	if pl == nil {
		return nil, errors.New("empty push payload")
	}
	data := make(map[string]string)
	var err error
	data["what"] = pl.What
	if pl.Silent {
		data["silent"] = "true"
	}
	data["topic"] = pl.Topic
	data["ts"] = pl.Timestamp.Format(time.RFC3339Nano)
	// 必须使用 "xfrom" 因为 "from" 是保留字。Google 没有在任何地方记录这一点。
	data["xfrom"] = pl.From
	switch pl.What {
	case push.ActMsg:
		data["seq"] = strconv.Itoa(pl.SeqId)
		if pl.ContentType != "" {
			data["mime"] = pl.ContentType
		}

		// 将 Drafty 内容转换为纯文本（0.16 及以下版本的客户端）。
		data["content"], err = drafty.PlainText(pl.Content)
		if err != nil {
			return nil, err
		}
		// 将长字符串截断为 128 个字符。
		// 先检查字节长度，不浪费时间转换短字符串。
		if len(data["content"]) > push.MaxPayloadLength {
			runes := []rune(data["content"])
			if len(runes) > push.MaxPayloadLength {
				data["content"] = string(runes[:push.MaxPayloadLength]) + "…"
			}
		}

		// 0.17 及以上版本客户端的富内容。
		data["rc"], err = drafty.Preview(pl.Content, push.MaxPayloadLength)

		if pl.Webrtc != "" {
			data["webrtc"] = pl.Webrtc
			if pl.AudioOnly {
				data["aonly"] = "true"
			}
			// 视频通话推送通知为静默推送。
			data["silent"] = "true"
		}
		if pl.Replace != "" {
			// 消息编辑通知也应为静默推送。
			data["silent"] = "true"
			data["replace"] = pl.Replace
		}
		if err != nil {
			return nil, err
		}
	case push.ActSub:
		data["modeWant"] = pl.ModeWant.String()
		data["modeGiven"] = pl.ModeGiven.String()
	case push.ActRead:
		data["seq"] = strconv.Itoa(pl.SeqId)
		data["silent"] = "true"
	default:
		return nil, errors.New("unknown push type")
	}
	return data, nil
}

// clonePayload 返回载荷的独立副本。
func clonePayload(src map[string]string) map[string]string {
	dst := make(map[string]string, len(src))
	maps.Copy(dst, src)
	return dst
}

// PrepareV1Notifications 为提供的回执创建可发布到推送通知服务器的通知载荷。
func PrepareV1Notifications(rcpt *push.Receipt, config *configType) ([]*fcmv1.Message, []t.Uid) {
	data, err := payloadToData(&rcpt.Payload)
	if err != nil {
		logs.Warn.Println("fcm push: could not parse payload:", err)
		return nil, nil
	}

	// 要发送推送的设备 ID。
	var devices map[t.Uid][]t.DeviceDef
	// 要推送的设备 ID 数量。
	var count int
	// 消息发送时在 Topic 中在线的设备。
	skipDevices := make(map[string]struct{})
	if len(rcpt.To) > 0 {
		// 用于查询数据库的 UID 列表

		uids := make([]t.Uid, len(rcpt.To))
		i := 0
		for uid, to := range rcpt.To {
			uids[i] = uid
			i++
			// 部分设备已在线并接收了消息。跳过它们。
			for _, deviceID := range to.Devices {
				skipDevices[deviceID] = struct{}{}
			}
		}
		devices, count, err = store.Devices.GetAll(uids...)
		if err != nil {
			logs.Warn.Println("fcm push: db error", err)
			return nil, nil
		}
	}
	if count == 0 && rcpt.Channel == "" {
		return nil, nil
	}

	if config == nil {
		// config 在从 tnpg 适配器调用时为 nil；提供一个空白配置以简化处理。
		config = &configType{}
	}

	var messages []*fcmv1.Message
	var uids []t.Uid
	for uid, devList := range devices {
		topic := rcpt.Payload.Topic
		userData := data
		tcat := t.GetTopicCat(topic)
		if rcpt.To[uid].Delivered > 0 || tcat == t.TopicCatP2P {
			userData = clonePayload(data)
			// 修复 P2P 推送的 Topic 名称。
			if tcat == t.TopicCatP2P {
				topic, _ = t.P2PNameForUser(uid, topic)
				userData["topic"] = topic
			}
			// 对已交互接收数据的用户静默推送。
			if rcpt.To[uid].Delivered > 0 {
				userData["silent"] = "true"
			}
		}

		for i := range devList {
			d := &devList[i]
			if _, ok := skipDevices[d.DeviceId]; !ok && d.DeviceId != "" {
				msg := fcmv1.Message{
					Token: d.DeviceId,
					Data:  userData,
				}

				switch d.Platform {
				case "android":
					msg.Android = androidNotificationConfig(rcpt.Payload.What, topic, userData, config)
				case "ios":
					msg.Apns = apnsNotificationConfig(rcpt.Payload.What, topic, userData, rcpt.To[uid].Unread, config)
				case "web":
					msg.Webpush = webpushNotificationConfig(rcpt.Payload.What, topic, userData, config)
				case "":
					// 忽略
				default:
					logs.Warn.Println("fcm: unknown device platform", d.Platform)
				}

				uids = append(uids, uid)
				messages = append(messages, &msg)
			}
		}
	}

	if rcpt.Channel != "" {
		topic := rcpt.Channel
		userData := clonePayload(data)
		userData["topic"] = topic
		// 频道接收者不应知道消息发送者的 ID。
		delete(userData, "xfrom")
		msg := fcmv1.Message{
			Topic: topic,
			Data:  userData,
		}

		// 我们不知道接收者的平台，必须为所有平台提供载荷。
		msg.Android = androidNotificationConfig(rcpt.Payload.What, topic, userData, config)
		msg.Apns = apnsNotificationConfig(rcpt.Payload.What, topic, userData, 0, config)
		msg.Webpush = webpushNotificationConfig(rcpt.Payload.What, topic, userData, config)
		messages = append(messages, &msg)
		// UID 在处理 Topic 推送时不使用，但应保持与消息相同的计数。
		uids = append(uids, t.ZeroUid)
	}

	return messages, uids
}

// DevicesForUser 加载指定用户的设备 ID。
func DevicesForUser(uid t.Uid) []string {
	ddef, count, err := store.Devices.GetAll(uid)
	if err != nil {
		logs.Warn.Println("fcm devices for user: db error", err)
		return nil
	}

	if count == 0 {
		return nil
	}

	devices := make([]string, count)
	for i, dd := range ddef[uid] {
		devices[i] = dd.DeviceId
	}
	return devices
}

// ChannelsForUser 加载用户具有 P 权限的频道订阅。
func ChannelsForUser(uid t.Uid) []string {
	channels, err := store.Users.GetChannels(uid)
	if err != nil {
		logs.Warn.Println("fcm channels for user: db error", err)
		return nil
	}
	return channels
}

// androidNotificationConfig 完成androidNotification配置所需的内部处理。
func androidNotificationConfig(what, topic string, data map[string]string, config *configType) *fcmv1.AndroidConfig {
	timeToLive := strconv.Itoa(defaultTimeToLive) + "s"
	if config != nil && config.TimeToLive > 0 {
		timeToLive = strconv.Itoa(config.TimeToLive) + "s"
	}

	if what == push.ActRead {
		return &fcmv1.AndroidConfig{
			Priority:     string(common.AndroidPriorityNormal),
			Notification: nil,
			Ttl:          timeToLive,
		}
	}

	_, videoCall := data["webrtc"]
	if videoCall {
		timeToLive = "0s"
	}

	// 发送优先级。
	priority := string(common.AndroidPriorityHigh)
	ac := &fcmv1.AndroidConfig{
		Priority: priority,
		Ttl:      timeToLive,
	}

	// 当包含此通知类型且应用不在前台时，
	// Android 不会唤醒应用也不会调用 FirebaseMessagingService:onMessageReceived。
	// 参见讨论：https://github.com/firebase/quickstart-js/issues/71
	if config.Android == nil || !config.Android.Enabled {
		return ac
	}

	body := config.Android.GetStringField(what, "Body")
	if body == "$content" {
		body = data["content"]
	}

	// 客户端显示优先级。
	priority = string(common.AndroidNotificationPriorityHigh)
	if videoCall {
		priority = string(common.AndroidNotificationPriorityMax)
	}

	ac.Notification = &fcmv1.AndroidNotification{
		// Android 使用 Tag 值将通知分组：
		// 每个 Topic 只显示一个通知。
		Tag:                  topic,
		NotificationPriority: priority,
		Visibility:           string(common.AndroidVisibilityPrivate),
		TitleLocKey:          config.Android.GetStringField(what, "TitleLocKey"),
		Title:                config.Android.GetStringField(what, "Title"),
		BodyLocKey:           config.Android.GetStringField(what, "BodyLocKey"),
		Body:                 body,
		Icon:                 config.Android.GetStringField(what, "Icon"),
		Color:                config.Android.GetStringField(what, "Color"),
		ClickAction:          config.Android.GetStringField(what, "ClickAction"),
	}

	return ac
}

// apnsShouldPresentAlert 完成apnsShouldPresentAlert所需的内部处理。
func apnsShouldPresentAlert(what, callStatus, isSilent string, config *configType) bool {
	return config.Apns != nil && config.Apns.Enabled && what != push.ActRead && callStatus == "" && isSilent == ""
}

// apnsNotificationConfig 完成apnsNotification配置所需的内部处理。
func apnsNotificationConfig(what, topic string, data map[string]string, unread int, config *configType) *fcmv1.ApnsConfig {
	callStatus := data["webrtc"]
	expires := time.Now().UTC().Add(time.Duration(defaultTimeToLive) * time.Second)
	if config.TimeToLive > 0 {
		expires = time.Now().UTC().Add(time.Duration(config.TimeToLive) * time.Second)
	}
	bundleId := config.ApnsBundleID
	pushType := common.ApnsPushTypeAlert
	priority := 10
	interruptionLevel := common.InterruptionLevelTimeSensitive
	if callStatus == "started" {
		// 仅在新通话启动时发送 VOIP 强提醒。若 BundleID 配置了 .voip 则使用 Voip 推送，否则使用 Critical 强提醒降级
		interruptionLevel = common.InterruptionLevelCritical
		if strings.HasSuffix(bundleId, ".voip") {
			pushType = common.ApnsPushTypeVoip
		} else {
			pushType = common.ApnsPushTypeAlert
		}
		expires = time.Now().UTC().Add(time.Duration(voipTimeToLive) * time.Second)
	} else if what == push.ActRead {
		priority = 5
		interruptionLevel = common.InterruptionLevelPassive
		pushType = common.ApnsPushTypeBackground
	}

	apsPayload := common.Aps{
		Badge:             unread,
		ContentAvailable:  1,
		MutableContent:    1,
		InterruptionLevel: interruptionLevel,
		Sound:             "default",
		ThreadID:          topic,
	}

	// 不为已读通知和视频通话显示提醒。
	if apnsShouldPresentAlert(what, callStatus, data["silent"], config) {
		body := config.Apns.GetStringField(what, "Body")
		if body == "$content" {
			body = data["content"]
		}

		apsPayload.Alert = &common.ApsAlert{
			Action:          config.Apns.GetStringField(what, "Action"),
			ActionLocKey:    config.Apns.GetStringField(what, "ActionLocKey"),
			Body:            body,
			LaunchImage:     config.Apns.GetStringField(what, "LaunchImage"),
			LocKey:          config.Apns.GetStringField(what, "LocKey"),
			Title:           config.Apns.GetStringField(what, "Title"),
			Subtitle:        config.Apns.GetStringField(what, "Subtitle"),
			TitleLocKey:     config.Apns.GetStringField(what, "TitleLocKey"),
			SummaryArg:      config.Apns.GetStringField(what, "SummaryArg"),
			SummaryArgCount: config.Apns.GetIntField(what, "SummaryArgCount"),
		}
	}

	payload, err := json.Marshal(map[string]any{"aps": apsPayload})
	if err != nil {
		return nil
	}
	headers := map[string]string{
		common.HeaderApnsExpiration: strconv.FormatInt(expires.Unix(), 10),
		common.HeaderApnsPriority:   strconv.Itoa(priority),
		common.HeaderApnsTopic:      bundleId,
		common.HeaderApnsCollapseID: topic,
		common.HeaderApnsPushType:   string(pushType),
	}

	ac := &fcmv1.ApnsConfig{
		Headers: headers,
		Payload: payload,
	}

	return ac
}

// webpushNotificationConfig 完成webpushNotification配置所需的内部处理。
func webpushNotificationConfig(what, topic string, data map[string]string, config *configType) *fcmv1.WebpushConfig {
	if config == nil || config.Webpush == nil || !config.Webpush.Enabled {
		return nil
	}

	timeToLive := strconv.Itoa(defaultTimeToLive) + "s"
	if config.TimeToLive > 0 {
		timeToLive = strconv.Itoa(config.TimeToLive) + "s"
	}

	wc := &fcmv1.WebpushConfig{
		Headers: map[string]string{
			"TTL": timeToLive,
		},
	}

	if what == push.ActRead {
		return wc
	}

	body := config.Webpush.GetStringField(what, "Body")
	if body == "$content" {
		body = data["content"]
	}

	notification := map[string]string{
		"title": config.Webpush.GetStringField(what, "Title"),
		"body":  body,
		"icon":  config.Webpush.GetStringField(what, "Icon"),
		"tag":   topic,
	}

	notificationRaw, err := json.Marshal(notification)
	if err != nil {
		return nil
	}
	wc.Notification = notificationRaw

	return wc
}
