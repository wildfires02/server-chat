// Package main 实现 IM 命令行客户端。
package main

import (
	"context"
	"crypto/tls"
	"flag"
	"fmt"
	"os"
	"strings"

	"chat/api/pbx"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	// AppName 指定App名称。
	AppName = "cli"
	// AppVersion 指定App版本。
	AppVersion = "3.1.0"
)

// main 解析启动参数、初始化依赖并运行当前服务或命令。
func main() {
	host := flag.String("host", "localhost:16060", "Address of IM gRPC server")
	webHost := flag.String("web-host", "localhost:6060", "Address of IM web server (for file uploads)")
	useSSL := flag.Bool("ssl", false, "Connect to server over secure TLS connection")
	sslHost := flag.String("ssl-host", "", "SSL host name to use instead of default")
	loginBasic := flag.String("login-basic", "", "Login using basic auth username:password")
	loginToken := flag.String("login-token", "", "Login using token auth")
	loginCookie := flag.Bool("login-cookie", false, "Read token from cookie file for auth")
	noLogin := flag.Bool("no-login", false, "Do not login even if credentials or cookie file present")
	noCookie := flag.Bool("no-cookie", false, "Do not save login cookie after authentication")
	cookieFile := flag.String("cookie-file", DefaultCookieFile, "Path to cookie file")
	apiKey := flag.String("api-key", "AQEAAAABAAD_rAp4DJh05a1HAwFT3A6K", "API key for file uploads")
	verbose := flag.Bool("verbose", false, "Log full JSON representation of all messages")
	showVersion := flag.Bool("version", false, "Print version")

	flag.Parse()

	if *showVersion {
		fmt.Printf("%s v%s (Go implementation)\n", AppName, AppVersion)
		os.Exit(0)
	}

	if *verbose {
		fmt.Printf("Web host: %s\n", *webHost)
	}

	//确定stdout是否是交互式终端
	fi, _ := os.Stdin.Stat()
	isInteractive := (fi.Mode() & os.ModeCharDevice) != 0

	var initialLoginMsg *pbx.ClientMsg

	if !*noLogin {
		if *loginToken != "" {
			fmt.Printf("Logging in with token: %s\n", *loginToken)
			initialLoginMsg = &pbx.ClientMsg{
				Message: &pbx.ClientMsg_Login{
					Login: &pbx.ClientLogin{
						Id:     "1",
						Scheme: "token",
						Secret: []byte(*loginToken),
					},
				},
			}
		} else if *loginBasic != "" {
			parts := strings.SplitN(*loginBasic, ":", 2)
			user := parts[0]
			pass := ""
			if len(parts) > 1 {
				pass = parts[1]
			}
			fmt.Printf("Logging in with basic auth: %s\n", user)
			initialLoginMsg = &pbx.ClientMsg{
				Message: &pbx.ClientMsg_Login{
					Login: &pbx.ClientLogin{
						Id:     "1",
						Scheme: "basic",
						Secret: fmt.Appendf(nil, "%s:%s", user, pass),
					},
				},
			}
		} else if *loginCookie || isInteractive {
			cookie, err := ReadCookie(*cookieFile)
			if err == nil && cookie.Token != "" {
				fmt.Printf("Logging in with cookie file (%s)\n", *cookieFile)
				initialLoginMsg = &pbx.ClientMsg{
					Message: &pbx.ClientMsg_Login{
						Login: &pbx.ClientLogin{
							Id:     "1",
							Scheme: "token",
							Secret: []byte(cookie.Token),
						},
					},
				}
			}
		}
	}

	//准备gRPC连接拨号选项
	var dialOpts []grpc.DialOption
	if *useSSL {
		tlsConfig := &tls.Config{
			ServerName: *sslHost,
		}
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(credentials.NewTLS(tlsConfig)))
	} else {
		dialOpts = append(dialOpts, grpc.WithTransportCredentials(insecure.NewCredentials()))
	}

	fmt.Printf("Connecting to IM gRPC server at %s...\n", *host)
	conn, err := grpc.Dial(*host, dialOpts...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to connect to %s: %v\n", *host, err)
		os.Exit(1)
	}
	defer conn.Close()

	ctx := context.Background()
	nodeClient := pbx.NewNodeClient(conn)

	stream, err := nodeClient.MessageLoop(ctx)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to open gRPC MessageLoop: %v\n", err)
		os.Exit(1)
	}

	saveCookie := !*noCookie
	client := NewClient(conn, stream, *verbose, isInteractive, saveCookie, *cookieFile, *apiKey)
	if err := client.Run(ctx, initialLoginMsg); err != nil {
		fmt.Fprintf(os.Stderr, "Client error: %v\n", err)
		os.Exit(1)
	}
}
