package server

import (
	"crypto/tls"
	"net/http"
	"os"
	"strings"

	"chat/server/logs"

	"github.com/gin-gonic/gin"
)

func startProtocolRuntime(config configType) *tls.Config {
	tlsConfig, err := parseTLSConfig(false, config.TLS)
	if err != nil {
		logs.Err.Fatalln(err)
	}

	pluginsInit(config.Plugin)
	usersInit()

	if globals.grpcServer, err = serveGrpc(config.GrpcListen, config.GrpcKeepalive, tlsConfig); err != nil {
		logs.Err.Fatal(err)
	}
	return tlsConfig
}

func registerServerHTTPRoutes(
	mux *http.ServeMux,
	curwd string,
	config *configType,
	tlsConfig *tls.Config,
) {
	// Gin 仅负责 HTTP 路由与中间件；已有协议处理函数继续使用标准库
	// http.Handler 签名，避免 WebSocket、Long Poll 和文件传输逻辑产生行为变化。
	router := gin.New()
	router.RedirectTrailingSlash = false
	router.RedirectFixedPath = false

	staticMountPoint := ""
	if config.StaticData != "" && config.StaticData != "-" {
		staticPath := toAbsolutePath(curwd, config.StaticData)
		if _, err := os.Stat(staticPath); os.IsNotExist(err) {
			logs.Err.Fatal("Static content directory is not found", staticPath)
		}

		staticMountPoint = normalizeHTTPPath(config.StaticMount, defaultStaticMount)
		staticHandler := cacheControlHandler(config.CacheControl,
			optionalHttpHeaders(
				httpErrorHandler(
					http.StripPrefix(staticMountPoint,
						http.FileServer(http.Dir(staticPath))))))
		if staticMountPoint == "/" {
			router.NoRoute(ginCompression(), gin.WrapH(staticHandler))
		} else {
			staticRoute := strings.TrimSuffix(staticMountPoint, "/") + "/*filepath"
			router.Any(staticRoute, ginCompression(), gin.WrapH(staticHandler))
		}
		logs.Info.Printf("Serving static content from '%s' at '%s'", staticPath, staticMountPoint)
	} else {
		logs.Info.Println("Static content is disabled")
	}

	config.ApiPath = normalizeHTTPPath(config.ApiPath, defaultApiPath)
	logs.Info.Printf("API served from root URL path '%s'", config.ApiPath)

	var usedUnixFallback bool
	globals.servingAt, usedUnixFallback = resolveServingEndpoint(
		config.ExtUrl,
		config.Listen,
		config.ApiPath,
		tlsConfig != nil,
	)
	if usedUnixFallback {
		logs.Warn.Printf(
			"Server is listening on Unix Socket '%s'. Using 'localhost' for servingAt. "+
				"Consider setting 'ext_url' in config for public endpoint.",
			config.Listen,
		)
	}
	logs.Info.Printf("Serving endpoint URL set to '%s'", globals.servingAt)

	statusPath := config.ServerStatusPath
	if statusPath != "" && statusPath != "-" {
		logs.Info.Printf("Server status is available at '%s'", statusPath)
		mux.HandleFunc(statusPath, serveStatus)
	}

	router.Any(config.ApiPath+"v0/channels", gin.WrapF(serveWebSocket))
	router.Any(config.ApiPath+"v0/channels/lp", ginCompression(), gin.WrapF(serveLongPoll))
	if config.Media != nil {
		registerGinSubtree(router, config.ApiPath+"v0/file/u/", largeFileReceiveHTTP)
		registerGinSubtree(router, config.ApiPath+"v0/file/s/", largeFileServeHTTP)
		registerGinSubtree(router, config.ApiPath+"v0/file/meta/", largeFileMetaHTTP)
		registerGinSubtree(router, config.ApiPath+"v0/file/resumable/", resumableFileHTTP)
		registerGinSubtree(router, config.ApiPath+"v0/file/direct/", directFileHTTP)
		logs.Info.Println("Large media handling enabled", config.Media.UseHandler)
	}
	if staticMountPoint != "/" {
		router.NoRoute(gin.WrapF(serve404))
	}
	mux.Handle("/", router)
}

// registerGinSubtree 保留 net/http ServeMux 以斜线结尾时匹配整个子树的行为。
func registerGinSubtree(router *gin.Engine, path string, handler http.HandlerFunc) {
	router.Any(strings.TrimSuffix(path, "/")+"/*filepath",
		ginCompression(), gin.WrapF(handler))
}
