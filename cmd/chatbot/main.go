// Package main 实现聊天机器人命令入口。
package main

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"math/rand"
	"net"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"

	"chat/api/pbx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	// appName 指定app名称。
	appName = "Tino-chatbot-go"
	// appVersion 指定app版本。
	appVersion = "1.0.0"
)

// Bot 保存Bot的数据和运行状态。
type Bot struct {
	// Embedded 嵌入公共状态或行为，供当前结构直接复用。
	pbx.UnimplementedPluginServer

	// host 保存host。
	host string
	// listen 保存listen。
	listen string
	// ssl 保存ssl。
	ssl bool
	// sslHost 保存sslHost。
	sslHost string
	// quotesFile 保存quotes文件。
	quotesFile string
	// cookieFile 保存cookie文件。
	cookieFile string
	// loginBasic 保存登录Basic。
	loginBasic string
	// loginToken 保存登录令牌。
	loginToken string

	// quotes 保存quotes列表。
	quotes []string
	// quotesMu 保存quotesMu。
	quotesMu sync.RWMutex

	// botUID 保存bot用户标识。
	botUID string
	// botUIDMu 保存bot用户标识Mu。
	botUIDMu sync.RWMutex

	// stream 保存数据流。
	stream pbx.Node_MessageLoopClient
	// sendMu 保存sendMu。
	sendMu sync.Mutex

	// futures 按键索引futures。
	futures map[string]futureBundle
	// futuresMu 保存futuresMu。
	futuresMu sync.RWMutex

	// subs 按键索引subs。
	subs map[string]bool
	// subsMu 保存subsMu。
	subsMu sync.RWMutex

	// nextTID 保存nextTID。
	nextTID int64
	// tidMu 保存tidMu。
	tidMu sync.Mutex
}

// futureBundle 保存futureBundle的数据和运行状态。
type futureBundle struct {
	// onSuccess 完成onSuccess所需的内部处理。
	onSuccess func(params map[string][]byte)
	// onError 完成on错误所需的内部处理。
	onError func(code int32, text string)
}

// getNextTID 查询并返回NextTID。
func (b *Bot) getNextTID() string {
	b.tidMu.Lock()
	defer b.tidMu.Unlock()
	b.nextTID++
	return fmt.Sprintf("%d", b.nextTID)
}

// addFuture 向当前集合添加Future。
func (b *Bot) addFuture(tid string, onSuccess func(map[string][]byte), onError func(int32, string)) {
	b.futuresMu.Lock()
	b.futures[tid] = futureBundle{onSuccess: onSuccess, onError: onError}
	b.futuresMu.Unlock()
}

// execFuture 完成execFuture所需的内部处理。
func (b *Bot) execFuture(tid string, code int32, text string, params map[string][]byte) {
	b.futuresMu.Lock()
	bundle, ok := b.futures[tid]
	if ok {
		delete(b.futures, tid)
	}
	b.futuresMu.Unlock()

	if ok {
		if code >= 200 && code < 400 {
			if bundle.onSuccess != nil {
				bundle.onSuccess(params)
			}
		} else {
			log.Printf("Server response error (%s): %d %s", tid, code, text)
			if bundle.onError != nil {
				bundle.onError(code, text)
			}
		}
	}
}

// loadQuotes 查询并返回Quotes。
func (b *Bot) loadQuotes(filePath string) error {
	f, err := os.Open(filePath)
	if err != nil {
		return err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	var list []string
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line != "" {
			list = append(list, line)
		}
	}
	if err := scanner.Err(); err != nil {
		return err
	}

	b.quotesMu.Lock()
	b.quotes = list
	b.quotesMu.Unlock()
	return nil
}

// getRandomQuote 查询并返回RandomQuote。
func (b *Bot) getRandomQuote() string {
	b.quotesMu.RLock()
	defer b.quotesMu.RUnlock()
	if len(b.quotes) == 0 {
		return "Hello from IM Go Bot!"
	}
	return b.quotes[rand.Intn(len(b.quotes))]
}

// sendMsg 处理Msg消息或事件。
func (b *Bot) sendMsg(msg *pbx.ClientMsg) error {
	b.sendMu.Lock()
	defer b.sendMu.Unlock()
	if b.stream == nil {
		return fmt.Errorf("stream is nil")
	}
	return b.stream.Send(msg)
}

// Plugin Server Interface Implementation
func (b *Bot) Account(ctx context.Context, req *pbx.AccountEvent) (*pbx.Unused, error) {
	action := "unknown"
	switch req.Action {
	case pbx.Crud_CREATE:
		action = "created"
	case pbx.Crud_UPDATE:
		action = "updated"
	case pbx.Crud_DELETE:
		action = "deleted"
	}
	log.Printf("Account event [%s]: user_id=%s", action, req.UserId)
	return &pbx.Unused{}, nil
}

// startPluginServer 启动并运行Plugin服务端处理流程。
func (b *Bot) startPluginServer() error {
	lis, err := net.Listen("tcp", b.listen)
	if err != nil {
		return err
	}
	srv := grpc.NewServer()
	pbx.RegisterPluginServer(srv, b)

	log.Printf("Plugin gRPC server running at %s", b.listen)
	go func() {
		if err := srv.Serve(lis); err != nil {
			log.Printf("Plugin server error: %v", err)
		}
	}()
	return nil
}

// hello 完成hello所需的内部处理。
func (b *Bot) hello() {
	tid := b.getNextTID()
	b.addFuture(tid, func(params map[string][]byte) {
		if params != nil {
			log.Printf("Server version info: build=%s ver=%s", string(params["build"]), string(params["ver"]))
		}
	}, nil)

	_ = b.sendMsg(&pbx.ClientMsg{
		Message: &pbx.ClientMsg_Hi{
			Hi: &pbx.ClientHi{
				Id:        tid,
				UserAgent: fmt.Sprintf("%s/%s (Go)", appName, appVersion),
				Ver:       "0.22",
				Lang:      "EN",
			},
		},
	})
}

// login 完成登录所需的内部处理。
func (b *Bot) login(scheme string, secret []byte) {
	tid := b.getNextTID()
	b.addFuture(tid, func(params map[string][]byte) {
		b.onLogin(params)
	}, func(code int32, text string) {
		if code != 409 {
			log.Fatalf("Login failed: %d %s", code, text)
		}
	})

	_ = b.sendMsg(&pbx.ClientMsg{
		Message: &pbx.ClientMsg_Login{
			Login: &pbx.ClientLogin{
				Id:     tid,
				Scheme: scheme,
				Secret: secret,
			},
		},
	})
}

// onLogin 完成on登录所需的内部处理。
func (b *Bot) onLogin(params map[string][]byte) {
	if params != nil {
		if userBytes, ok := params["user"]; ok {
			uid := strings.Trim(string(userBytes), "\"")
			b.botUIDMu.Lock()
			b.botUID = uid
			b.botUIDMu.Unlock()
			log.Printf("Logged in successfully. Bot User ID: %s", uid)
		}
	}
	b.subscribe("me")
	b.saveAuthCookie(params)
}

// subscribe 完成subscribe所需的内部处理。
func (b *Bot) subscribe(topic string) {
	tid := b.getNextTID()
	b.addFuture(tid, func(params map[string][]byte) {
		b.subsMu.Lock()
		b.subs[topic] = true
		b.subsMu.Unlock()
		log.Printf("Subscribed to topic: %s", topic)
	}, nil)

	_ = b.sendMsg(&pbx.ClientMsg{
		Message: &pbx.ClientMsg_Sub{
			Sub: &pbx.ClientSub{
				Id:    tid,
				Topic: topic,
			},
		},
	})
}

// leave 完成leave所需的内部处理。
func (b *Bot) leave(topic string) {
	tid := b.getNextTID()
	b.addFuture(tid, func(params map[string][]byte) {
		b.subsMu.Lock()
		delete(b.subs, topic)
		b.subsMu.Unlock()
		log.Printf("Left topic: %s", topic)
	}, nil)

	_ = b.sendMsg(&pbx.ClientMsg{
		Message: &pbx.ClientMsg_Leave{
			Leave: &pbx.ClientLeave{
				Id:    tid,
				Topic: topic,
			},
		},
	})
}

// publish 处理publish消息或事件。
func (b *Bot) publish(topic, text string) {
	tid := b.getNextTID()
	contentBytes, _ := json.Marshal(text)
	autoBytes, _ := json.Marshal(true)

	_ = b.sendMsg(&pbx.ClientMsg{
		Message: &pbx.ClientMsg_Pub{
			Pub: &pbx.ClientPub{
				Id:     tid,
				Topic:  topic,
				NoEcho: true,
				Head: map[string][]byte{
					"auto": autoBytes,
				},
				Content: contentBytes,
			},
		},
	})
}

// noteRead 完成事件通知Read所需的内部处理。
func (b *Bot) noteRead(topic string, seqID int32) {
	_ = b.sendMsg(&pbx.ClientMsg{
		Message: &pbx.ClientMsg_Note{
			Note: &pbx.ClientNote{
				Topic: topic,
				What:  pbx.InfoNote_READ,
				SeqId: seqID,
			},
		},
	})
}

// saveAuthCookie 保存认证Cookie。
func (b *Bot) saveAuthCookie(params map[string][]byte) {
	if params == nil || b.cookieFile == "" {
		return
	}
	cookieData := make(map[string]string)
	cookieData["schema"] = "token"
	for k, v := range params {
		if k == "token" {
			cookieData["secret"] = string(v)
		} else {
			cookieData[k] = string(v)
		}
	}
	data, err := json.MarshalIndent(cookieData, "", "  ")
	if err == nil {
		_ = os.WriteFile(b.cookieFile, data, 0600)
	}
}

// readAuthCookie 查询并返回认证Cookie。
func (b *Bot) readAuthCookie() (string, []byte, error) {
	data, err := os.ReadFile(b.cookieFile)
	if err != nil {
		return "", nil, err
	}
	var cookieData map[string]interface{}
	if err := json.Unmarshal(data, &cookieData); err != nil {
		return "", nil, err
	}
	schema, _ := cookieData["schema"].(string)
	secretStr, _ := cookieData["secret"].(string)
	if schema == "" || secretStr == "" {
		return "", nil, fmt.Errorf("invalid cookie format")
	}
	if schema == "token" {
		decoded, err := base64.StdEncoding.DecodeString(secretStr)
		if err == nil {
			return schema, decoded, nil
		}
		return schema, []byte(secretStr), nil
	}
	return schema, []byte(secretStr), nil
}

// handleIncomingMsg 处理IncomingMsg消息或事件。
func (b *Bot) handleIncomingMsg(msg *pbx.ServerMsg) {
	if msg == nil {
		return
	}
	switch m := msg.Message.(type) {
	case *pbx.ServerMsg_Ctrl:
		b.execFuture(m.Ctrl.Id, m.Ctrl.Code, m.Ctrl.Text, m.Ctrl.Params)
	case *pbx.ServerMsg_Data:
		b.handleDataMsg(m.Data)
	case *pbx.ServerMsg_Pres:
		b.handlePresMsg(m.Pres)
	}
}

// handleDataMsg 处理数据Msg消息或事件。
func (b *Bot) handleDataMsg(data *pbx.ServerData) {
	b.botUIDMu.RLock()
	botID := b.botUID
	b.botUIDMu.RUnlock()

	if data.FromUserId != botID {
		b.noteRead(data.Topic, data.SeqId)
		time.Sleep(100 * time.Millisecond)
		replyText := b.getRandomQuote()
		log.Printf("Replying to %s on %s: %s", data.FromUserId, data.Topic, replyText)
		b.publish(data.Topic, replyText)
	}
}

// handlePresMsg 处理PresMsg消息或事件。
func (b *Bot) handlePresMsg(pres *pbx.ServerPres) {
	if pres.Topic == "me" {
		b.subsMu.RLock()
		isSubbed := b.subs[pres.Src]
		b.subsMu.RUnlock()

		if (pres.What == pbx.ServerPres_ON || pres.What == pbx.ServerPres_MSG) && !isSubbed {
			b.subscribe(pres.Src)
		} else if pres.What == pbx.ServerPres_OFF && isSubbed {
			b.leave(pres.Src)
		}
	}
}

// main 解析启动参数、初始化依赖并运行当前服务或命令。
func main() {
	rand.Seed(time.Now().UnixNano())

	host := flag.String("host", "localhost:16060", "Address of IM server gRPC endpoint")
	ssl := flag.Bool("ssl", false, "Use SSL/TLS to connect to the server")
	sslHost := flag.String("ssl-host", "", "SSL host name override")
	listen := flag.String("listen", ":40051", "Address to listen on for incoming Plugin API calls")
	loginBasic := flag.String("login-basic", "", "Login using basic auth username:password")
	loginToken := flag.String("login-token", "", "Login using token authentication")
	loginCookie := flag.String("login-cookie", ".chatbot-cookie", "Cookie file to read/save credentials")
	quotesFile := flag.String("quotes", "quotes.txt", "Path to quotes text file")
	flag.Parse()

	bot := &Bot{
		host:       *host,
		listen:     *listen,
		ssl:        *ssl,
		sslHost:    *sslHost,
		quotesFile: *quotesFile,
		cookieFile: *loginCookie,
		loginBasic: *loginBasic,
		loginToken: *loginToken,
		futures:    make(map[string]futureBundle),
		subs:       make(map[string]bool),
		nextTID:    100,
	}

	if err := bot.loadQuotes(*quotesFile); err != nil {
		log.Printf("Warning: could not load quotes from %s: %v", *quotesFile, err)
	} else {
		log.Printf("Loaded quotes from %s (%d items)", *quotesFile, len(bot.quotes))
	}

	if err := bot.startPluginServer(); err != nil {
		log.Fatalf("Failed to start plugin server: %v", err)
	}

	// Dial gRPC connection
	var opts []grpc.DialOption
	if *ssl {
		tlsConfig := &tls.Config{}
		if *sslHost != "" {
			tlsConfig.ServerName = *sslHost
		}
		opts = append(opts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	} else {
		opts = append(opts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	conn, err := grpc.Dial(*host, opts...)
	if err != nil {
		log.Fatalf("Failed to connect to IM gRPC at %s: %v", *host, err)
	}
	defer conn.Close()

	client := pbx.NewNodeClient(conn)

	// Interrupt handling
	ctx, cancel := context.WithCancel(context.Background())
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigCh
		log.Println("Shutting down chatbot...")
		cancel()
	}()

	for ctx.Err() == nil {
		stream, err := client.MessageLoop(ctx)
		if err != nil {
			log.Printf("Error creating MessageLoop stream: %v, retrying in 3s...", err)
			time.Sleep(3 * time.Second)
			continue
		}

		bot.stream = stream

		// Auth setup
		var scheme string
		var secret []byte
		if *loginToken != "" {
			scheme = "token"
			secret = []byte(*loginToken)
		} else if *loginBasic != "" {
			scheme = "basic"
			secret = []byte(*loginBasic)
		} else {
			s, sec, err := bot.readAuthCookie()
			if err != nil {
				log.Printf("No credentials provided and failed to read cookie: %v", err)
				break
			}
			scheme = s
			secret = sec
		}

		bot.hello()
		bot.login(scheme, secret)

		// Recv loop
		for {
			inMsg, err := stream.Recv()
			if err != nil {
				if err != io.EOF {
					log.Printf("Stream disconnected: %v", err)
				}
				break
			}
			bot.handleIncomingMsg(inMsg)
		}

		time.Sleep(3 * time.Second)
	}
}
