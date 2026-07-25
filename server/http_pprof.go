// 调试工具。在响应 HTTP 请求时转储指定的性能分析数据。
// 		http(s)://<host-name>/<configured-path>/<profile-name>
// 可能的性能分析名称列表参见 godoc：https://golang.org/pkg/runtime/pprof/#Profile

package main

import (
	"fmt"
	"net/http"
	"path"
	"runtime/pprof"
	"strings"

	"chat/server/logs"
)

var pprofHttpRoot string

// 在指定的 URL 路径暴露调试性能分析。
func servePprof(mux *http.ServeMux, serveAt string) {
	if serveAt == "" || serveAt == "-" {
		return
	}

	pprofHttpRoot = path.Clean("/"+serveAt) + "/"
	mux.HandleFunc(pprofHttpRoot, profileHandler)

	logs.Info.Printf("pprof: profiling info exposed at '%s'", pprofHttpRoot)
}

func profileHandler(wrt http.ResponseWriter, req *http.Request) {
	wrt.Header().Set("X-Content-Type-Options", "nosniff")
	wrt.Header().Set("Content-Type", "text/plain; charset=utf-8")

	profileName := strings.TrimPrefix(req.URL.Path, pprofHttpRoot)

	profile := pprof.Lookup(profileName)
	if profile == nil {
		servePprofError(wrt, http.StatusNotFound, "Unknown profile '"+profileName+"'")
		return
	}

	// 输出请求的性能分析数据。
	profile.WriteTo(wrt, 2)
}

func servePprofError(wrt http.ResponseWriter, status int, txt string) {
	wrt.Header().Set("Content-Type", "text/plain; charset=utf-8")
	wrt.Header().Set("X-Go-Pprof", "1")
	wrt.Header().Del("Content-Disposition")
	wrt.WriteHeader(status)
	fmt.Fprintln(wrt, txt)
}
