// Package fcm 实现通过 Google Firebase Cloud Messaging (FCM HTTP v1 API) 推送移动端（Android, iOS, Web）通知的插件。
package fcm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"

	fbase "firebase.google.com/go/v4"
	legacy "firebase.google.com/go/v4/messaging"
	fcmv1 "google.golang.org/api/fcm/v1"

	"chat/server/logs"
	"chat/server/push"
	"chat/server/push/common"
	"chat/server/store"
	"chat/server/store/types"

	"golang.org/x/oauth2/google"
	"google.golang.org/api/option"
)

// handler 保存处理器的共享实例或运行状态。
var handler Handler

const (
	// 输入 Channel 缓冲区大小
	bufferSize = 1024

	// 单批次发送的推送消息数（FCM 批处理限制）
	pushBatchSize = 100

	// 单批次发送的订阅/取消订阅请求数
	subBatchSize = 1000
)

// Handler FCM 推送处理器，实现 push.Handler 接口。
type Handler struct {
	// input 传递input相关的异步事件。
	input chan *push.Receipt
	// channel 传递通道相关的异步事件。
	channel chan *push.ChannelReq
	// stop 传递stop相关的异步事件。
	stop chan bool
	// projectID 保存project标识。
	projectID string

	// client 保存客户端。
	client *legacy.Client
	// v1 保存v1。
	v1 *fcmv1.Service
	// outbox 在数据库中保存尚未确认的推送任务。
	outbox *push.DurableOutbox
}

// configType 保存配置Type的数据和运行状态。
type configType struct {
	// Enabled 指示是否启用或满足Enabled。
	Enabled bool `json:"enabled"`
	// DryRun 保存DryRun。
	DryRun bool `json:"dry_run"`
	// Credentials 保存Credentials。
	Credentials json.RawMessage `json:"credentials"`
	// CredentialsFile 保存Credentials文件。
	CredentialsFile string `json:"credentials_file"`
	// TimeToLive 保存TimeToLive。
	TimeToLive int `json:"time_to_live,omitempty"`
	// ApnsBundleID 保存ApnsBundle标识。
	ApnsBundleID string `json:"apns_bundle_id,omitempty"`
	// Android 保存Android。
	Android *common.Config `json:"android,omitempty"`
	// Apns 保存Apns。
	Apns *common.Config `json:"apns,omitempty"`
	// Webpush 保存Webpush。
	Webpush *common.Config `json:"webpush,omitempty"`
}

// Init 初始化 FCM 推送处理器，读取凭据并构造 FCM v1 客户端服务。
func (Handler) Init(jsonconf json.RawMessage) (bool, error) {

	var config configType
	err := json.Unmarshal([]byte(jsonconf), &config)
	if err != nil {
		return false, errors.New("解析配置失败: " + err.Error())
	}

	if !config.Enabled {
		return false, nil
	}

	// 显式配置文件优先，避免示例或历史内联占位值遮蔽真实服务账号文件。
	if config.CredentialsFile != "" {
		config.Credentials, err = os.ReadFile(config.CredentialsFile)
		if err != nil {
			return false, err
		}
	}

	if config.Credentials == nil {
		return false, errors.New("缺少 FCM credentials 认证凭据")
	}

	ctx := context.Background()
	credentials, err := google.CredentialsFromJSON(ctx, config.Credentials, "https://www.googleapis.com/auth/firebase.messaging")
	if err != nil {
		return false, err
	}
	if credentials.ProjectID == "" {
		return false, errors.New("缺少 Firebase Project ID")
	}

	app, err := fbase.NewApp(ctx, &fbase.Config{}, option.WithCredentials(credentials))
	if err != nil {
		return false, err
	}

	handler.client, err = app.Messaging(ctx)
	if err != nil {
		return false, err
	}

	handler.v1, err = fcmv1.NewService(ctx, option.WithCredentials(credentials), option.WithScopes(fcmv1.FirebaseMessagingScope))
	if err != nil {
		return false, err
	}

	handler.input = make(chan *push.Receipt, bufferSize)
	handler.channel = make(chan *push.ChannelReq, bufferSize)
	handler.stop = make(chan bool, 1)
	handler.projectID = credentials.ProjectID
	handler.outbox = push.NewDurableOutbox("fcm", func(receipt *push.Receipt) error {
		return sendFcmV1(receipt, &config)
	})
	handler.outbox.Start()

	// 启动 Worker 协程循环处理推送与订阅请求
	go func() {
		for {
			select {
			case rcpt := <-handler.input:
				// 持久化失败时由该通道执行一次尽力投递，避免立即丢失通知。
				go func() {
					if err := sendFcmV1(rcpt, &config); err != nil {
						logs.Warn.Println("fcm 降级投递失败:", err)
					}
				}()
			case sub := <-handler.channel:
				go processSubscription(sub)
			case <-handler.stop:
				return
			}
		}
	}()

	return true, nil
}

// sendFcmV1 构建 FCM v1 消息报文并分发给目标设备。
func sendFcmV1(rcpt *push.Receipt, config *configType) error {
	messages, uids, prepareErr := PrepareV1NotificationsWithError(rcpt, config)
	if prepareErr != nil {
		return fmt.Errorf("fcm prepare notifications: %w", prepareErr)
	}
	for i := range messages {
		req := &fcmv1.SendMessageRequest{
			Message:      messages[i],
			ValidateOnly: config.DryRun,
		}
		_, err := handler.v1.Projects.Messages.Send("projects/"+handler.projectID, req).Do()
		if err != nil {
			gerr, decodingErrs := common.DecodeGoogleApiError(err)
			for _, err := range decodingErrs {
				logs.Info.Println("fcm googleapi.Error 解码警告:", err)
			}
			switch strings.ToUpper(gerr.FcmErrCode) {
			case "":
				if gerr.HttpCode == 429 || gerr.HttpCode >= 500 {
					return fmt.Errorf("fcm temporary http error %d: %w", gerr.HttpCode, err)
				}
				return push.Permanent(fmt.Errorf("fcm rejected request %d: %w", gerr.HttpCode, err))
			case common.ErrorQuotaExceeded, common.ErrorUnavailable, common.ErrorInternal, common.ErrorUnspecified:
				// 临时故障，停止该批次发送
				logs.Warn.Println("fcm 临时发信故障:", gerr.FcmErrCode, gerr.ErrMessage)
				return fmt.Errorf("fcm temporary error %s: %w", gerr.FcmErrCode, err)
			case common.ErrorSenderIDMismatch, common.ErrorInvalidArgument, common.ErrorThirdPartyAuth:
				// 配置错误，停止发送
				logs.Warn.Println("fcm 配置无效:", gerr.FcmErrCode, gerr.ErrMessage)
				return push.Permanent(fmt.Errorf("fcm configuration error %s: %w", gerr.FcmErrCode, err))
			case common.ErrorUnregistered:
				// 设备 Token 已失效，从数据库清理该 Token 并继续发送
				logs.Warn.Println("fcm Token 已失效/取消注册:", gerr.FcmErrCode, gerr.ErrMessage)
				if err := store.Devices.Delete(uids[i], messages[i].Token); err != nil {
					logs.Warn.Println("fcm 清理无效 Token 失败:", err)
				}
			default:
				// 未知错误
				logs.Warn.Println("fcm 未知错误:", gerr.FcmErrCode, gerr.ErrMessage)
				return fmt.Errorf("fcm unknown error %s: %w", gerr.FcmErrCode, err)
			}
		}
	}
	return nil
}

// processSubscription 处理设备针对 FCM Topic (Channel) 的订阅与取消订阅。
func processSubscription(req *push.ChannelReq) {
	var channel string
	var devices []string
	var device string
	var channels []string

	if req.Channel != "" && req.DeviceID != "" {
		// 新设备登录时只同步当前 Token，避免为用户的其它设备重复请求 FCM。
		devices = []string{req.DeviceID}
		channel = req.Channel
	} else if req.Channel != "" {
		devices = DevicesForUser(req.Uid)
		channel = req.Channel
	} else if req.DeviceID != "" {
		channels = ChannelsForUser(req.Uid)
		device = req.DeviceID
	}

	if (len(devices) == 0 && device == "") || (len(channels) == 0 && channel == "") {
		return
	}

	if len(devices) > subBatchSize {
		devices = devices[0:subBatchSize]
		logs.Warn.Println("fcm: 用户", req.Uid.UserId(), "拥有的设备数超过单批次上限", subBatchSize)
	}

	var err error
	var resp *legacy.TopicManagementResponse
	if channel != "" && len(devices) > 0 {
		if req.Unsub {
			resp, err = handler.client.UnsubscribeFromTopic(context.Background(), devices, channel)
		} else {
			resp, err = handler.client.SubscribeToTopic(context.Background(), devices, channel)
		}
		if err != nil {
			logs.Warn.Println("fcm: 订阅或取消订阅失败", req.Unsub, err)
		} else {
			handleSubErrors(resp, req.Uid, devices)
		}
		return
	}

	if device != "" && len(channels) > 0 {
		devices := []string{device}
		for _, channel := range channels {
			if req.Unsub {
				resp, err = handler.client.UnsubscribeFromTopic(context.Background(), devices, channel)
			} else {
				resp, err = handler.client.SubscribeToTopic(context.Background(), devices, channel)
			}
			if err != nil {
				logs.Warn.Println("fcm: 订阅或取消订阅失败", req.Unsub, err)
				break
			}
			handleSubErrors(resp, req.Uid, devices)
		}
		return
	}

	logs.Err.Println("fcm: 用户", req.Uid.UserId(), "无效的频道/设备订阅组合",
		len(devices), len(channels))
}

// handleSubErrors 检查并记录 FCM 订阅过程中的部分失败错误
func handleSubErrors(response *legacy.TopicManagementResponse, uid types.Uid, devices []string) {
	if response.FailureCount <= 0 {
		return
	}

	for _, errinfo := range response.Errors {
		logs.Warn.Println("fcm 订阅/取消订阅错误", errinfo.Reason, uid, devices[errinfo.Index])
	}
}

// IsReady 检查推送处理器是否已初始化就绪。
func (Handler) IsReady() bool {
	return handler.input != nil
}

// Push 返回用于接收推送回执写入的 Channel。
func (Handler) Push() chan<- *push.Receipt {
	return handler.input
}

// Enqueue 先持久化推送任务，再由 FCM Worker 投递。
func (Handler) Enqueue(receipt *push.Receipt) error {
	if handler.outbox == nil {
		return errors.New("fcm outbox is unavailable")
	}
	return handler.outbox.Enqueue(receipt)
}

// Channel 返回用于设备 FCM Topic 频道订阅管理的 Channel。
func (Handler) Channel() chan<- *push.ChannelReq {
	return handler.channel
}

// Stop 停止 FCM 推送服务。
func (Handler) Stop() {
	handler.stop <- true
	if handler.outbox != nil {
		handler.outbox.Stop()
	}
}

// init 注册当前包提供的实现并初始化包级状态。
func init() {
	push.Register("fcm", &handler)
}
