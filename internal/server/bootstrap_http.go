package server

import (
	"crypto/tls"
	"net/http"
	"os"

	"chat/server/logs"

	gh "github.com/gorilla/handlers"
)

func startProtocolRuntime(options serverOptions, config configType) *tls.Config {
	tlsConfig, err := parseTLSConfig(options.tlsEnabled, config.TLS)
	if err != nil {
		logs.Err.Fatalln(err)
	}

	pluginsInit(config.Plugin)
	usersInit()

	listenGRPC := options.listenGRPC
	if listenGRPC == "" {
		listenGRPC = config.GrpcListen
	}
	if globals.grpcServer, err = serveGrpc(listenGRPC, config.GrpcKeepalive, tlsConfig); err != nil {
		logs.Err.Fatal(err)
	}
	return tlsConfig
}

func registerServerHTTPRoutes(
	mux *http.ServeMux,
	curwd string,
	options serverOptions,
	config *configType,
	tlsConfig *tls.Config,
) {
	staticMountPoint := ""
	if options.staticPath != "" && options.staticPath != "-" {
		staticPath := toAbsolutePath(curwd, options.staticPath)
		if _, err := os.Stat(staticPath); os.IsNotExist(err) {
			logs.Err.Fatal("Static content directory is not found", staticPath)
		}

		staticMountPoint = normalizeHTTPPath(config.StaticMount, defaultStaticMount)
		mux.Handle(staticMountPoint,
			cacheControlHandler(config.CacheControl,
				optionalHttpHeaders(
					gh.CompressHandler(
						httpErrorHandler(
							http.StripPrefix(staticMountPoint,
								http.FileServer(http.Dir(staticPath))))))))
		logs.Info.Printf("Serving static content from '%s' at '%s'", staticPath, staticMountPoint)
	} else {
		logs.Info.Println("Static content is disabled")
	}

	if options.apiPath != "" {
		config.ApiPath = options.apiPath
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

	statusPath := options.serverStatusPath
	if statusPath == "" {
		statusPath = config.ServerStatusPath
	}
	if statusPath != "" && statusPath != "-" {
		logs.Info.Printf("Server status is available at '%s'", statusPath)
		mux.HandleFunc(statusPath, serveStatus)
	}

	mux.HandleFunc(config.ApiPath+"v0/channels", serveWebSocket)
	mux.Handle(config.ApiPath+"v0/channels/lp", gh.CompressHandler(http.HandlerFunc(serveLongPoll)))
	if config.Media != nil {
		mux.Handle(config.ApiPath+"v0/file/u/", gh.CompressHandler(http.HandlerFunc(largeFileReceiveHTTP)))
		mux.Handle(config.ApiPath+"v0/file/s/", gh.CompressHandler(http.HandlerFunc(largeFileServeHTTP)))
		logs.Info.Println("Large media handling enabled", config.Media.UseHandler)
	}

	if staticMountPoint != "/" {
		mux.HandleFunc("/", serve404)
	}
}
