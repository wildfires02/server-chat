package server

import (
	"flag"

	"chat/internal/configutil"
	"chat/server/logs"
)

// serverOptions 保存命令行对配置文件和监听参数的覆盖值。
type serverOptions struct {
	logFlags         string
	configFile       string
	staticPath       string
	listenOn         string
	apiPath          string
	listenGRPC       string
	clusterSelf      string
	expvarPath       string
	serverStatusPath string
	pprofFile        string
	pprofURL         string
	tlsEnabled       bool
	validateConfig   bool
}

func parseServerOptions() serverOptions {
	var options serverOptions
	flag.StringVar(&options.logFlags, "log_flags", "stdFlags",
		"逗号分隔的日志标志列表（如 https://golang.org/pkg/log/#pkg-constants 定义，不带 L 前缀）")
	flag.StringVar(&options.configFile, "config", "configs/im.yaml", "YAML 配置文件路径")
	flag.StringVar(&options.staticPath, "static_data", defaultStaticPath, "待服务静态文件目录的文件路径")
	flag.StringVar(&options.listenOn, "listen", "", "覆盖 HTTP(S) 客户端监听地址和端口")
	flag.StringVar(&options.apiPath, "api_path", "", "覆盖 API 服务的基础 URL 路径")
	flag.StringVar(&options.listenGRPC, "grpc_listen", "", "覆盖 gRPC 客户端监听地址和端口")
	flag.BoolVar(&options.tlsEnabled, "tls_enabled", false, "覆盖配置中 TLS 启用设置")
	flag.StringVar(&options.clusterSelf, "cluster_self", "", "覆盖当前集群节点名称")
	flag.StringVar(&options.expvarPath, "expvar", "", "覆盖暴露运行时统计的 URL 路径。使用 '-' 禁用")
	flag.StringVar(&options.serverStatusPath, "server_status", "",
		"覆盖显示服务器内部状态的 URL 路径。使用 '-' 禁用")
	flag.StringVar(&options.pprofFile, "pprof", "", "保存性能分析信息的文件名。未设置则禁用")
	flag.StringVar(&options.pprofURL, "pprof_url", "", "仅用于调试！暴露性能分析信息的 URL 路径。未设置则禁用")
	flag.BoolVar(&options.validateConfig, "validate_config", false, "仅校验配置和部署模式，不启动服务")
	flag.Parse()
	return options
}

func loadServerConfig(curwd string, options serverOptions) configType {
	configFile := toAbsolutePath(curwd, options.configFile)
	logs.Info.Printf("Using config from '%s'", configFile)

	var config configType
	if err := configutil.DecodeFile(configFile, &config); err != nil {
		logs.Err.Fatal(err)
	}
	if options.listenOn != "" {
		config.Listen = options.listenOn
	}
	if err := validateDeploymentConfig(&config, options.clusterSelf, options.pprofURL); err != nil {
		logs.Err.Fatal("部署配置校验失败: ", err)
	}
	logs.Info.Printf("Deployment environment '%s', mode '%s'",
		config.Runtime.Environment, config.Runtime.DeploymentMode)
	return config
}
