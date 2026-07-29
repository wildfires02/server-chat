// Package main 实现 IM 命令行客户端。
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"chat/api/pbx"

	"google.golang.org/grpc"
)

// Client 保存客户端的数据和运行状态。
type Client struct {
	// Conn 保存连接。
	Conn *grpc.ClientConn
	// Stream 保存数据流。
	Stream pbx.Node_MessageLoopClient
	// MsgID 保存Msg标识。
	MsgID int64
	// DefaultUser 保存默认用户。
	DefaultUser string
	// DefaultTopic 保存默认Topic。
	DefaultTopic string
	// Variables 按键索引Variables。
	Variables map[string]interface{}
	// WaitingFor 保存WaitingFor。
	WaitingFor *ParsedCmd
	// WaitingChan 传递WaitingChan相关的异步事件。
	WaitingChan chan *pbx.ServerCtrl
	// Verbose 保存Verbose。
	Verbose bool
	// Interactive 保存Interactive。
	Interactive bool
	// SaveCookie 保存SaveCookie。
	SaveCookie bool
	// CookieFile 保存Cookie文件。
	CookieFile string
	// ApiKey 保存API键。
	ApiKey string
	// AuthToken 保存认证令牌。
	AuthToken string
	// mu 保护客户端的并发读写。
	mu sync.Mutex
}

// NewClient 创建并初始化 Client。
func NewClient(conn *grpc.ClientConn, stream pbx.Node_MessageLoopClient, verbose, interactive, saveCookie bool, cookieFile, apiKey string) *Client {
	return &Client{
		Conn:         conn,
		Stream:       stream,
		Variables:    make(map[string]interface{}),
		WaitingChan:  make(chan *pbx.ServerCtrl, 1),
		Verbose:      verbose,
		Interactive:  interactive,
		SaveCookie:   saveCookie,
		CookieFile:   cookieFile,
		ApiKey:       apiKey,
		DefaultUser:  "me",
		DefaultTopic: "me",
	}
}

// NextID 完成Next标识所需的内部处理。
func (c *Client) NextID() int64 {
	return atomic.AddInt64(&c.MsgID, 1)
}

// Run 启动并运行Run处理流程。
func (c *Client) Run(ctx context.Context, initialLoginMsg *pbx.ClientMsg) error {
	// Send Hi message
	hiMsg := &pbx.ClientMsg{
		Message: &pbx.ClientMsg_Hi{
			Hi: &pbx.ClientHi{
				Id:         fmt.Sprintf("%d", c.NextID()),
				UserAgent:  "cli-go/3.1.0",
				Ver:        "0.29",
				Lang:       "EN",
				Background: !c.Interactive,
			},
		},
	}

	if c.Verbose {
		fmt.Printf("=> Hi: %s\n", PrettyJSON(hiMsg))
	}
	if err := c.Stream.Send(hiMsg); err != nil {
		return fmt.Errorf("failed to send Hi: %w", err)
	}

	// Send initial login message if provided
	if initialLoginMsg != nil {
		if c.Verbose {
			fmt.Printf("=> Login: %s\n", PrettyJSON(initialLoginMsg))
		}
		if err := c.Stream.Send(initialLoginMsg); err != nil {
			return fmt.Errorf("failed to send initial Login: %w", err)
		}
	}

	// Start receive loop
	errChan := make(chan error, 1)
	go func() {
		errChan <- c.receiveLoop()
	}()

	// Read input commands from stdin
	scanner := bufio.NewScanner(os.Stdin)

	if c.Interactive {
		fmt.Print("tn> ")
	}

	for scanner.Scan() {
		line := scanner.Text()
		err := c.processLine(line)
		if err != nil {
			fmt.Printf("Error: %v\n", err)
		}

		if c.Interactive {
			fmt.Print("tn> ")
		}
	}

	if err := scanner.Err(); err != nil {
		fmt.Printf("Error reading input: %v\n", err)
	}

	return <-errChan
}

// processLine 处理Line消息或事件。
func (c *Client) processLine(line string) error {
	cmd, err := ParseCommandLine(line, c.NextID(), c.DefaultUser, c.DefaultTopic, c.ApiKey)
	if err != nil {
		return err
	}
	if cmd == nil {
		return nil
	}

	if cmd.IsExit {
		fmt.Println("Exiting CLI...")
		os.Exit(0)
	}

	if len(cmd.MacroCmds) > 0 {
		for _, macroCmd := range cmd.MacroCmds {
			if err := c.processLine(macroCmd); err != nil {
				return err
			}
		}
		return nil
	}

	if cmd.IsLocal {
		c.executeLocalCommand(cmd)
	}

	if cmd.Msg != nil {
		return c.sendServerMessage(cmd)
	}

	return nil
}

// executeLocalCommand 完成executeLocal命令所需的内部处理。
func (c *Client) executeLocalCommand(cmd *ParsedCmd) {
	if cmd.SleepMs > 0 {
		time.Sleep(time.Duration(cmd.SleepMs) * time.Millisecond)
	}
	if cmd.LogValue != "" {
		fmt.Println(cmd.LogValue)
	}
	if cmd.UseUser != "" {
		c.DefaultUser = cmd.UseUser
		fmt.Printf("Default user set to %s\n", c.DefaultUser)
	}
	if cmd.UseTopic != "" {
		c.DefaultTopic = cmd.UseTopic
		fmt.Printf("Default topic set to %s\n", c.DefaultTopic)
	}
}

// sendServerMessage 处理服务端消息消息或事件。
func (c *Client) sendServerMessage(cmd *ParsedCmd) error {
	if c.Verbose {
		fmt.Printf("=> %s\n", PrettyJSON(cmd.Msg))
	}

	c.mu.Lock()
	if cmd.IsSynchronous {
		c.WaitingFor = cmd
	}
	c.mu.Unlock()

	if err := c.Stream.Send(cmd.Msg); err != nil {
		return fmt.Errorf("send error: %w", err)
	}

	if cmd.IsSynchronous {
		select {
		case ctrl := <-c.WaitingChan:
			c.mu.Lock()
			c.WaitingFor = nil
			c.mu.Unlock()

			if cmd.VarName != "" {
				c.Variables[cmd.VarName] = ctrl
			}
			if cmd.FailOnError && ctrl.Code >= 400 {
				return fmt.Errorf("command failed (%d): %s", ctrl.Code, ctrl.Text)
			}
		case <-time.After(5 * time.Second):
			c.mu.Lock()
			c.WaitingFor = nil
			c.mu.Unlock()
			return fmt.Errorf("timeout waiting for command response")
		}
	}

	return nil
}

// receiveLoop 持续运行receiveLoop，直到输入通道关闭或收到停止信号。
func (c *Client) receiveLoop() error {
	for {
		msg, err := c.Stream.Recv()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}

		if c.Verbose {
			fmt.Printf("\r<= %s\n", PrettyJSON(msg))
		}

		switch m := msg.Message.(type) {
		case *pbx.ServerMsg_Ctrl:
			c.handleCtrl(m.Ctrl)
		case *pbx.ServerMsg_Meta:
			c.handleMeta(m.Meta)
		case *pbx.ServerMsg_Data:
			c.handleData(m.Data)
		case *pbx.ServerMsg_Pres:
			c.handlePres(m.Pres)
		case *pbx.ServerMsg_Info:
			c.handleInfo(m.Info)
		}
	}
}

// handleCtrl 处理Ctrl消息或事件。
func (c *Client) handleCtrl(ctrl *pbx.ServerCtrl) {
	topicStr := ""
	if ctrl.Topic != "" {
		topicStr = fmt.Sprintf(" (%s)", ctrl.Topic)
	}

	if ctrl.Code >= 200 && ctrl.Code < 300 {
		if tokenBytes, ok := ctrl.Params["token"]; ok {
			var token string
			_ = json.Unmarshal(tokenBytes, &token)
			c.AuthToken = token
			if userBytes, ok := ctrl.Params["user"]; ok {
				var user string
				_ = json.Unmarshal(userBytes, &user)
				c.DefaultUser = user
				if c.SaveCookie {
					_ = SaveCookie(c.CookieFile, user, token)
				}
				fmt.Printf("\r<= %d %s (Authenticated as %s)\n", ctrl.Code, ctrl.Text, user)
			} else {
				fmt.Printf("\r<= %d %s%s\n", ctrl.Code, ctrl.Text, topicStr)
			}
		} else {
			fmt.Printf("\r<= %d %s%s\n", ctrl.Code, ctrl.Text, topicStr)
		}
	} else {
		fmt.Printf("\r<= %d %s%s\n", ctrl.Code, ctrl.Text, topicStr)
	}

	c.mu.Lock()
	if c.WaitingFor != nil && c.WaitingFor.ID == ctrl.Id {
		select {
		case c.WaitingChan <- ctrl:
		default:
		}
	}
	c.mu.Unlock()
}

// handleMeta 处理元数据消息或事件。
func (c *Client) handleMeta(meta *pbx.ServerMeta) {
	var what []string
	if len(meta.Sub) > 0 {
		what = append(what, "sub")
	}
	if meta.Desc != nil {
		what = append(what, "desc")
	}
	if meta.Del != nil {
		what = append(what, "del")
	}
	if len(meta.Tags) > 0 {
		what = append(what, "tags")
	}

	fmt.Printf("\r<= meta %s %s\n", strings.Join(what, ","), meta.Topic)

	if meta.Desc != nil && meta.Desc.Public != nil {
		fmt.Printf("   Public: %s\n", string(meta.Desc.Public))
	}
}

// handleData 处理数据消息或事件。
func (c *Client) handleData(data *pbx.ServerData) {
	fmt.Printf("\n\rFrom: %s\n", data.FromUserId)
	fmt.Printf("Topic: %s\n", data.Topic)
	fmt.Printf("Seq: %d\n", data.SeqId)
	if len(data.Head) > 0 {
		fmt.Println("Headers:")
		for k, v := range data.Head {
			fmt.Printf("\t%s: %s\n", k, string(v))
		}
	}
	fmt.Printf("Content: %s\n", string(data.Content))
}

// handlePres 处理Pres消息或事件。
func (c *Client) handlePres(pres *pbx.ServerPres) {
	fmt.Printf("\r<= pres %s %s\n", pres.What.String(), pres.Topic)
}

// handleInfo 处理通知消息或事件。
func (c *Client) handleInfo(info *pbx.ServerInfo) {
	fmt.Printf("\r<= info %s by %s in topic %s\n", info.What.String(), info.FromUserId, info.Topic)
}
