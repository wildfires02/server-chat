/******************************************************************************
 *
 *  描述 :
 *
 *  服务端配置解析与初始化主程序。
 *
 *****************************************************************************/

// Package server 实现即时通信服务端的协议、路由和业务逻辑。
package server

import (
	"net/http"
	"os"
	"runtime"

	"chat/server/logs"
)

// Run 解析启动参数、初始化依赖并运行即时通信服务。
func Run() {
	executable, _ := os.Executable()
	options := parseServerOptions()
	logs.Init(os.Stderr, options.logFlags)

	curwd, err := os.Getwd()
	if err != nil {
		logs.Err.Fatal("Couldn't get current working directory: ", err)
	}
	logs.Info.Printf("Server v%s:%s:%s; pid %d; %d process(es)",
		currentVersion,
		executable,
		buildstamp,
		os.Getpid(),
		runtime.GOMAXPROCS(runtime.NumCPU()),
	)

	config := loadServerConfig(curwd, options)
	if options.validateConfig {
		logs.Info.Println("配置校验通过")
		return
	}

	mux := http.NewServeMux()
	initServerStats(mux, config, options.expvarPath)
	servePprof(mux, options.pprofURL)

	workerID := clusterInit(
		config.Cluster,
		&options.clusterSelf,
		config.Runtime.DeploymentMode,
	)

	defer startServerProfiler(curwd, options.pprofFile)()
	defer openServerStore(workerID, config.Store)()
	defer startServerHealth(mux, config.Health)()

	initServerAuthAndTags(&config)
	applyServerRuntimeConfig(config)

	defer startServerMedia(&config)()
	defer startAccountGarbageCollector(config.AccountGC)()
	defer startServerPush(config.Push)()

	if err := initVideoCalls(config.WebRTC); err != nil {
		logs.Err.Fatalf("Failed to init video calls: %v", err)
	}
	defer startCoreRuntime()()

	tlsConfig := startProtocolRuntime(options, config)
	registerServerHTTPRoutes(mux, curwd, options, &config, tlsConfig)

	globals.health.MarkServing()
	if err := listenAndServe(config.Listen, mux, tlsConfig, signalHandler()); err != nil {
		logs.Err.Fatal(err)
	}
}
