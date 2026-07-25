// Package stdout 提供简单的标准输出（stdout）调试推送插件实现。
// 启用后，会将接收到的每一条推送通知日志直接打印输出到控制台 stdout。
package stdout

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"chat/server/push"
)

var handler stdoutPush

// 默认输入 Channel 缓冲区容量大小
const defaultBuffer = 32

type stdoutPush struct {
	initialized bool
	input       chan *push.Receipt
	channel     chan *push.ChannelReq
	stop        chan bool
}

type configType struct {
	Enabled bool `json:"enabled"`
	Buffer  int  `json:"buffer"`
}

// Init 初始化 stdout 推送处理器。
func (stdoutPush) Init(jsonconf json.RawMessage) (bool, error) {

	if handler.initialized {
		return false, errors.New("已经初始化过")
	}

	var config configType
	if err := json.Unmarshal([]byte(jsonconf), &config); err != nil {
		return false, errors.New("解析配置失败: " + err.Error())
	}

	handler.initialized = true

	if !config.Enabled {
		return false, nil
	}

	if config.Buffer <= 0 {
		config.Buffer = defaultBuffer
	}

	handler.input = make(chan *push.Receipt, config.Buffer)
	handler.channel = make(chan *push.ChannelReq, config.Buffer)
	handler.stop = make(chan bool, 1)

	// 启动打印协程，监听输入 Channel 并打印到 stdout
	go func() {
		for {
			select {
			case msg := <-handler.input:
				fmt.Fprintln(os.Stdout, msg)
			case msg := <-handler.channel:
				fmt.Fprintln(os.Stdout, msg)
			case <-handler.stop:
				return
			}
		}
	}()

	return true, nil
}

// IsReady 检查推送处理器是否已初始化就绪。
func (stdoutPush) IsReady() bool {
	return handler.input != nil
}

// Push 返回用于接收推送回执消息写入的 Channel。
func (stdoutPush) Push() chan<- *push.Receipt {
	return handler.input
}

// Channel 返回用于接收设备频道订阅变更请求的 Channel。
func (stdoutPush) Channel() chan<- *push.ChannelReq {
	return handler.channel
}

// Stop 停止推送打印 Worker 协程。
func (stdoutPush) Stop() {
	handler.stop <- true
}

func init() {
	push.Register("stdout", &handler)
}
