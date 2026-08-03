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

// Run 从 Viper YAML 配置初始化依赖并运行即时通信服务。
func Run() {
	executable, _ := os.Executable()
	if err := rejectServiceArguments(os.Args); err != nil {
		logs.Err.Fatal(err)
	}
	curwd, err := os.Getwd()
	if err != nil {
		logs.Err.Fatal("Couldn't get current working directory: ", err)
	}
	config, configFile, err := loadServerConfig(curwd)
	if err != nil {
		logs.Err.Fatal("部署配置校验失败: ", err)
	}
	logs.Init(os.Stderr, config.LogFlags)
	logs.Info.Printf("Server v%s:%s:%s; pid %d; %d process(es)",
		currentVersion,
		executable,
		buildstamp,
		os.Getpid(),
		runtime.GOMAXPROCS(runtime.NumCPU()),
	)
	logs.Info.Printf("Using Viper config from '%s'", configFile)
	logs.Info.Printf("Deployment environment '%s', mode '%s'",
		config.Runtime.Environment, config.Runtime.DeploymentMode)

	mux := http.NewServeMux()
	initServerStats(mux, config)
	servePprof(mux, config.PprofURL)

	clusterSelf := ""
	workerID := clusterInit(config.Cluster, &clusterSelf, config.Runtime.DeploymentMode)

	defer startServerProfiler(curwd, config.PprofFile)()
	defer openServerStore(workerID, config.Store)()
	defer startServerHealth(mux, config.Health)()

	initServerAuthAndTags(&config)
	applyServerRuntimeConfig(config)

	defer startServerMedia(&config)()
	defer startAccountGarbageCollector(config.AccountGC)()
	defer startServerPush(config.Push, config.PushAlerts)()

	if err := initVideoCalls(config.WebRTC); err != nil {
		logs.Err.Fatalf("Failed to init video calls: %v", err)
	}
	defer startCoreRuntime()()

	tlsConfig := startProtocolRuntime(config)
	registerServerHTTPRoutes(mux, curwd, &config, tlsConfig)

	globals.health.MarkServing()
	if err := listenAndServe(config.Listen, mux, tlsConfig, signalHandler()); err != nil {
		logs.Err.Fatal(err)
	}
}
