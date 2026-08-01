package server

import (
	"context"
	"crypto/tls"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"syscall"
	"time"

	"chat/internal/configutil"
	"chat/server/logs"
	"chat/server/store"
)

//RunAdmin启动管理控制平面，作为一个独立于
//服务器。
func RunAdmin() {
	if err := rejectServiceArguments(os.Args); err != nil {
		logs.Err.Fatal(err)
	}
	currentDirectory, err := os.Getwd()
	if err != nil {
		logs.Err.Fatal("Couldn't get current working directory: ", err)
	}
	configFile, err := findServiceConfig(currentDirectory, "admin.yaml")
	if err != nil {
		logs.Err.Fatal(err)
	}
	var config configType
	if err = configutil.DecodeFileConfigOnly(configFile, &config); err != nil {
		logs.Err.Fatal(err)
	}
	if config.LogFlags == "" {
		config.LogFlags = "stdFlags"
	}
	logs.Init(os.Stderr, config.LogFlags)
	if err = validateAdminServiceConfig(&config); err != nil {
		logs.Err.Fatal("im-admin 配置校验失败: ", err)
	}
	executable, _ := os.Executable()
	logs.Info.Printf("Admin server v%s:%s:%s; pid %d; %d process(es)",
		currentVersion, executable, buildstamp, os.Getpid(),
		runtime.GOMAXPROCS(runtime.NumCPU()))
	logs.Info.Printf("Using Viper config from '%s'", configFile)

	if err = store.Store.Open(config.Admin.WorkerID, config.Store); err != nil {
		logs.Err.Fatal("Failed to connect to DB: ", err)
	}
	defer func() {
		_ = store.Store.Close()
		logs.Info.Println("im-admin database connection closed")
	}()
	globals.apiKeySalt = config.APIKeySalt

	apiPath := normalizeHTTPPath(config.ApiPath, defaultApiPath)
	mux := http.NewServeMux()
	registerAdminHTTPRoutes(mux, apiPath, config)
	mux.HandleFunc("/", func(writer http.ResponseWriter, request *http.Request) {
		http.NotFound(writer, request)
	})

	tlsConfig, err := parseTLSConfig(false, config.TLS)
	if err != nil {
		logs.Err.Fatal("Failed to initialize im-admin TLS: ", err)
	}
	if err = serveAdminHTTP(config.Listen, mux, tlsConfig); err != nil {
		logs.Err.Fatal(err)
	}
}

func validateAdminServiceConfig(config *configType) error {
	if config == nil || config.Admin == nil || !config.Admin.Enabled {
		return errors.New("admin.enabled 必须为 true")
	}
	environment := strings.ToLower(strings.TrimSpace(config.Runtime.Environment))
	switch environment {
	case environmentDevelopment, environmentTest, environmentStaging, environmentProduction:
	default:
		return errors.New("runtime.environment 必须是 development、test、staging 或 production")
	}
	config.Runtime.Environment = environment
	if config.Listen == "" {
		config.Listen = ":6061"
	}
	if config.Admin.WorkerID < 0 || config.Admin.WorkerID > 1023 {
		return errors.New("admin.worker_id 必须在 0..1023 之间")
	}
	token := strings.TrimSpace(config.Admin.BootstrapToken)
	if token == "" {
		return errors.New("admin.bootstrap_token 不能为空")
	}
	if (environment == environmentStaging || environment == environmentProduction) && len(token) < 32 {
		return errors.New("预发布或生产环境的 admin.bootstrap_token 至少需要 32 个字符")
	}
	for _, origin := range config.Admin.AllowedOrigins {
		origin = strings.TrimSpace(origin)
		if origin == "*" {
			return errors.New("admin.allowed_origins 禁止使用通配符")
		}
		if !strings.HasPrefix(origin, "https://") &&
			!(environment == environmentDevelopment && strings.HasPrefix(origin, "http://")) {
			return errors.New("admin.allowed_origins 包含不安全来源")
		}
	}
	if len(config.Store) == 0 || string(config.Store) == "null" {
		return errors.New("store_config 不能为空")
	}
	return nil
}

func serveAdminHTTP(address string, handler http.Handler, tlsConfig *tls.Config) error {
	server := &http.Server{
		Addr: address, Handler: handler, TLSConfig: tlsConfig,
		ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second,
		WriteTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 14,
	}
	listener, err := netListener(address)
	if err != nil {
		return err
	}
	serverError := make(chan error, 1)
	go func() {
		if tlsConfig != nil {
			serverError <- server.ServeTLS(listener, "", "")
		} else {
			serverError <- server.Serve(listener)
		}
	}()
	logs.Info.Printf("im-admin listening on [%s]", address)

	signals := make(chan os.Signal, 1)
	signal.Notify(signals, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(signals)
	select {
	case <-signals:
		shutdownContext, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err = server.Shutdown(shutdownContext); err != nil {
			return err
		}
		err = <-serverError
	case err = <-serverError:
	}
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
