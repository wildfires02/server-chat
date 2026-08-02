// Package tnpg 实现 IM Push Gateway（IM 官方网关服务）移动端推送插件。
package tnpg

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"chat/server/logs"
	"chat/server/push"
	"chat/server/push/common"
	"chat/server/push/fcm"
	"chat/server/store"
	"chat/server/store/types"

	fcmv1 "google.golang.org/api/fcm/v1"
)

const (
	// pushPath 指定pushPath。
	pushPath = "pushv1"
	// subsPath 指定subsPath。
	subsPath = "sub"
	// pushBatchSize 指定push批次Size。
	pushBatchSize = 100
	// subBatchSize 指定订阅批次Size。
	subBatchSize = 1000
	// bufferSize 指定缓冲区Size。
	bufferSize = 1024
)

// handler 保存处理器的共享实例或运行状态。
var handler Handler

// maxPooledPostBodyCap 指定maxPooledPostBodyCap。
const maxPooledPostBodyCap = 1 << 16

// postBodyPool 保存postBody池的共享实例或运行状态。
var postBodyPool = sync.Pool{
	New: func() any {
		return new(bytes.Buffer)
	},
}

// gzipWriterPool 保存gzip写入器池的共享实例或运行状态。
var gzipWriterPool = sync.Pool{
	New: func() any {
		return gzip.NewWriter(nil)
	},
}

// Handler TNPG 推送客户端处理器结构体。
type Handler struct {
	// input 传递input相关的异步事件。
	input chan *push.Receipt
	// channel 传递通道相关的异步事件。
	channel chan *push.ChannelReq
	// stop 传递stop相关的异步事件。
	stop chan bool
	// pushUrl 保存pushURL。
	pushUrl string
	// subUrl 保存订阅URL。
	subUrl string
	// outbox 在数据库中保存尚未确认的推送任务。
	outbox *push.DurableOutbox
}

// configType 保存配置Type的数据和运行状态。
type configType struct {
	// Enabled 指示是否启用或满足Enabled。
	Enabled bool `json:"enabled"`
	// ServerAddr 保存服务端Addr。
	ServerAddr string `json:"server_addr"`
	// OrgID 保存Org标识。
	OrgID string `json:"org"`
	// AuthToken 保存认证令牌。
	AuthToken       string `json:"token"`
	DebugPushGWHost string `json:"debug_server"` // 兼容配置别名
}

// subUnsubReq 设备 Token 订阅/取消订阅 FCM Topic 频道的网关请求格式。
type subUnsubReq struct {
	// Channel 保存通道。
	Channel string `json:"channel,omitempty"`
	// Channels 保存Channels列表。
	Channels []string `json:"channels,omitempty"`
	// Device 保存设备。
	Device string `json:"device,omitempty"`
	// Devices 保存Devices列表。
	Devices []string `json:"devices,omitempty"`
	// Unsub 保存Unsub。
	Unsub bool `json:"unsub"`
}

// tnpgResponse 保存tnpg响应的数据和运行状态。
type tnpgResponse struct {
	// 仅推送消息时的单条消息 ID
	MessageID string `json:"msg_id,omitempty"`
	// 服务器返回的 HTTP 状态码
	Code int `json:"code,omitempty"`
	// FCM 错误码
	ErrorCode string `json:"errcode,omitempty"`
	// ExtendedError 保存Extended错误。
	ExtendedError string `json:"exerr,omitempty"`
	// ErrorMessage 保存错误消息。
	ErrorMessage string `json:"errmsg,omitempty"`
	// 仅订阅/取消订阅时的索引位置
	Index int `json:"index,omitempty"`
}

// batchResponse 保存批次响应的数据和运行状态。
type batchResponse struct {
	// 成功发送的消息数量
	SuccessCount int `json:"sent_count"`
	// 失败数量
	FailureCount int `json:"fail_count"`
	// 批次整体失败时的错误码与错误信息
	FatalCode string `json:"errcode,omitempty"`
	// FatalMessage 保存Fatal消息。
	FatalMessage string `json:"errmsg,omitempty"`
	// 单条消息的详细响应列表（顺序与请求数组一致）
	Responses []*tnpgResponse `json:"resp,omitempty"`

	// 本地状态字段
	httpCode int
	// httpStatus 保存HTTP状态。
	httpStatus string
}

// Init 初始化 TNPG 推送处理器，配置服务 URL 并启动后台发送协程。
func (Handler) Init(jsonconf json.RawMessage) (bool, error) {
	var config configType
	if err := json.Unmarshal(jsonconf, &config); err != nil {
		return false, errors.New("解析配置失败: " + err.Error())
	}

	if !config.Enabled {
		return false, nil
	}

	serverAddr := config.ServerAddr
	if serverAddr == "" {
		serverAddr = config.DebugPushGWHost
	}
	serverAddr = strings.TrimSpace(serverAddr)
	if serverAddr == "" {
		return false, errors.New("缺少 Push Gateway 网关地址配置 (server_addr)")
	}

	config.OrgID = strings.TrimSpace(config.OrgID)
	if config.OrgID == "" {
		return false, errors.New("缺少 organization 机构名称配置")
	}

	config.OrgID = strings.ToLower(config.OrgID)

	serverUrl, err := url.Parse(serverAddr)
	if err != nil {
		return false, err
	}
	serverUrl.Path += pushPath + "/" + config.OrgID
	handler.pushUrl = serverUrl.String()
	serverUrl, _ = url.Parse(serverAddr)
	serverUrl.Path += subsPath + "/" + config.OrgID
	handler.subUrl = serverUrl.String()

	handler.input = make(chan *push.Receipt, bufferSize)
	handler.channel = make(chan *push.ChannelReq, bufferSize)
	handler.stop = make(chan bool, 1)
	handler.outbox = push.NewDurableOutbox("tnpg", func(receipt *push.Receipt) error {
		return sendPushes(receipt, &config)
	})
	handler.outbox.Start()

	// 启动 Worker 协程循环处理推送与订阅请求
	go func() {
		for {
			select {
			case rcpt := <-handler.input:
				// 持久化失败时执行一次尽力投递，避免立即丢失通知。
				go func() {
					if err := sendPushes(rcpt, &config); err != nil {
						logs.Warn.Println("tnpg 降级投递失败:", err)
					}
				}()
			case sub := <-handler.channel:
				go processSubscription(sub, &config)
			case <-handler.stop:
				return
			}
		}
	}()

	return true, nil
}

// postMessage 向 TNPG 网关发送经过 gzip 压缩的 POST 请求。
func postMessage(endpoint string, body any, config *configType) (*batchResponse, error) {
	buf := postBodyPool.Get().(*bytes.Buffer)
	defer func() {
		buf.Reset()
		if cap(buf.Bytes()) > maxPooledPostBodyCap {
			return
		}
		postBodyPool.Put(buf)
	}()

	gzw := gzipWriterPool.Get().(*gzip.Writer)
	defer func() {
		gzw.Reset(nil)
		gzipWriterPool.Put(gzw)
	}()
	gzw.Reset(buf)
	err := json.NewEncoder(gzw).Encode(body)
	if closeErr := gzw.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, endpoint, buf)
	if err != nil {
		return nil, err
	}
	req.Header.Add("Authorization", "Bearer "+config.AuthToken)
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	req.Header.Add("Content-Encoding", "gzip")
	req.Header.Add("Accept-Encoding", "gzip")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}

	var batch batchResponse
	var reader io.ReadCloser
	if strings.Contains(resp.Header.Get("Content-Encoding"), "gzip") {
		reader, err = gzip.NewReader(resp.Body)
		if err == nil {
			defer reader.Close()
		}
	} else {
		reader = resp.Body
	}

	if err == nil {
		err = json.NewDecoder(reader).Decode(&batch)
	}
	resp.Body.Close()

	if err != nil {
		logs.Warn.Println("tnpg 解码响应失败:", err)
	}

	batch.httpCode = resp.StatusCode
	batch.httpStatus = resp.Status

	return &batch, nil
}

// sendPushes 批量打包并通过 TNPG 发送推送消息。
func sendPushes(rcpt *push.Receipt, config *configType) error {
	messages, uids, prepareErr := fcm.PrepareV1NotificationsWithError(rcpt, nil)
	if prepareErr != nil {
		return fmt.Errorf("tnpg prepare notifications: %w", prepareErr)
	}

	n := len(messages)
	for i := 0; i < n; i += pushBatchSize {
		upper := min(i+pushBatchSize, n)
		var payloads []any
		for j := i; j < upper; j++ {
			payloads = append(payloads, messages[j])
		}
		resp, err := postMessage(handler.pushUrl, payloads, config)
		if err != nil {
			logs.Warn.Println("tnpg 推送请求失败:", err)
			return fmt.Errorf("tnpg request failed: %w", err)
		}
		if resp.httpCode >= 300 {
			logs.Warn.Println("tnpg 推送请求被拒绝:", resp.httpStatus)
			requestErr := fmt.Errorf("tnpg rejected request: %s", resp.httpStatus)
			if resp.httpCode == http.StatusTooManyRequests || resp.httpCode >= 500 {
				return requestErr
			}
			return push.Permanent(requestErr)
		}
		if resp.FatalCode != "" {
			logs.Err.Println("tnpg 推送发生致命错误:", resp.FatalMessage)
			fatalErr := fmt.Errorf("tnpg fatal error %s: %s", resp.FatalCode, resp.FatalMessage)
			if isTemporaryPushError(resp.FatalCode) {
				return fatalErr
			}
			return push.Permanent(fatalErr)
		}
		// 处理失效 Token 与错误
		if err = handlePushResponse(resp, messages[i:upper], uids[i:upper]); err != nil {
			return err
		}
	}
	return nil
}

// processSubscription 处理网关上的设备频道订阅/取消订阅。
func processSubscription(req *push.ChannelReq, config *configType) {
	su := subUnsubReq{
		Unsub: req.Unsub,
	}

	if req.Channel != "" && req.DeviceID != "" {
		su.Devices = []string{req.DeviceID}
		su.Channel = req.Channel
	} else if req.Channel != "" {
		su.Devices = fcm.DevicesForUser(req.Uid)
		su.Channel = req.Channel
	} else if req.DeviceID != "" {
		su.Channels = fcm.ChannelsForUser(req.Uid)
		su.Device = req.DeviceID
	}

	if (len(su.Devices) == 0 && su.Device == "") || (len(su.Channels) == 0 && su.Channel == "") {
		return
	}

	if len(su.Devices) > subBatchSize {
		su.Devices = su.Devices[0:subBatchSize]
		logs.Warn.Println("tnpg: 用户", req.Uid.UserId(), "拥有的设备数超过上限", subBatchSize)
	}

	resp, err := postMessage(handler.subUrl, &su, config)
	if err != nil {
		logs.Warn.Println("tnpg 频道订阅请求失败:", err)
		return
	}
	if resp.httpCode >= 300 {
		logs.Warn.Println("tnpg 频道订阅被拒绝:", resp.httpStatus)
		return
	}
	if resp.FatalCode != "" {
		logs.Err.Println("tnpg 频道订阅发生错误:", resp.FatalMessage)
		return
	}
	handleSubResponse(resp, req, su.Devices, su.Channels)
}

// handlePushResponse 检查网关返回的响应，清理失效的设备 Token。
func handlePushResponse(batch *batchResponse, messages []*fcmv1.Message, uids []types.Uid) error {
	if batch.FailureCount <= 0 {
		return nil
	}

	for i, resp := range batch.Responses {
		if resp == nil {
			continue
		}
		switch resp.ErrorCode {
		case "": // 无错误
		case common.ErrorQuotaExceeded, common.ErrorUnavailable, common.ErrorInternal, common.ErrorUnspecified:
			logs.Warn.Println("tnpg 临时故障:", resp.ErrorMessage)
			return fmt.Errorf("tnpg temporary error %s: %s", resp.ErrorCode, resp.ErrorMessage)
		case common.ErrorInvalidArgument:
			logs.Warn.Println("tnpg 参数无效:", resp.ExtendedError, resp.ErrorMessage)
			if strings.Contains(resp.ExtendedError, "message.token") && i < len(uids) && i < len(messages) {
				if err := store.Devices.Delete(uids[i], messages[i].Token); err != nil {
					logs.Warn.Println("tnpg 清理无效 Token 失败:", err)
				}
			} else {
				return push.Permanent(fmt.Errorf("tnpg invalid argument: %s", resp.ErrorMessage))
			}
		case common.ErrorSenderIDMismatch, common.ErrorThirdPartyAuth:
			logs.Warn.Println("tnpg 配置错误:", resp.ExtendedError, resp.ErrorMessage)
			return push.Permanent(fmt.Errorf("tnpg configuration error %s: %s",
				resp.ErrorCode, resp.ErrorMessage))
		case common.ErrorUnregistered:
			logs.Info.Println("tnpg Token 已失效:", resp.ErrorMessage, resp.ExtendedError, resp.MessageID)
			if i < len(uids) && i < len(messages) {
				if err := store.Devices.Delete(uids[i], messages[i].Token); err != nil {
					logs.Warn.Println("tnpg 清理无效 Token 失败:", err)
				}
			}
		default:
			logs.Warn.Println("tnpg 未知错误:", resp.ErrorCode, resp.ErrorMessage, resp.ExtendedError, resp.Code)
			unknownErr := fmt.Errorf("tnpg unknown error %s: %s", resp.ErrorCode, resp.ErrorMessage)
			if resp.Code >= 500 || isTemporaryPushError(resp.ErrorCode) {
				return unknownErr
			}
			return push.Permanent(unknownErr)
		}
	}
	return nil
}

func isTemporaryPushError(code string) bool {
	switch strings.ToUpper(strings.TrimSpace(code)) {
	case common.ErrorQuotaExceeded, common.ErrorUnavailable, common.ErrorInternal, common.ErrorUnspecified:
		return true
	default:
		return false
	}
}

// handleSubResponse 检查网关订阅响应中的错误并记录日志。
func handleSubResponse(batch *batchResponse, req *push.ChannelReq, devices, channels []string) {
	if batch.FailureCount <= 0 {
		return
	}

	var src string
	for _, resp := range batch.Responses {
		if len(devices) > 0 {
			src = devices[resp.Index]
		} else {
			src = channels[resp.Index]
		}
		logs.Warn.Println("fcm 订阅/取消订阅错误", resp.ErrorCode, req.Uid, src)
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

// Enqueue 先持久化推送任务，再由网关 Worker 投递。
func (Handler) Enqueue(receipt *push.Receipt) error {
	if handler.outbox == nil {
		return errors.New("tnpg outbox is unavailable")
	}
	return handler.outbox.Enqueue(receipt)
}

// Channel 返回用于发送频道订阅请求的 Channel。
func (Handler) Channel() chan<- *push.ChannelReq {
	return handler.channel
}

// Stop 停止网关推送服务。
func (Handler) Stop() {
	handler.stop <- true
	if handler.outbox != nil {
		handler.outbox.Stop()
	}
}

// init 注册当前包提供的实现并初始化包级状态。
func init() {
	push.Register("tnpg", &handler)
}
