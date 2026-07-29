// Package main 提供即时通信服务端的可执行入口。
package main

import "chat/internal/server"

// main 把进程控制权交给内部服务端应用。
func main() {
	server.Run()
}
