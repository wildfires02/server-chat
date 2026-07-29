// Package server 实现服务端 HTTP 路径和对外地址的启动配置。
package server

import "strings"

// normalizeHTTPPath 把配置路径归一化为以斜杠开头和结尾的 URL 路径。
func normalizeHTTPPath(path, fallback string) string {
	if path == "" {
		path = fallback
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	if !strings.HasSuffix(path, "/") {
		path += "/"
	}
	return path
}

// resolveServingEndpoint 计算下发给客户端的公开服务端点。
//
// externalURL 非空时具有最高优先级；Unix Domain Socket 无法形成公网
// URL，因此回退到 localhost，并由调用方记录部署提示。
func resolveServingEndpoint(
	externalURL string,
	listenAddress string,
	apiPath string,
	tlsEnabled bool,
) (endpoint string, usedUnixFallback bool) {
	scheme := "http://"
	if tlsEnabled {
		scheme = "https://"
	}

	if externalURL != "" {
		endpoint = externalURL
		if !strings.HasPrefix(endpoint, "http://") &&
			!strings.HasPrefix(endpoint, "https://") {
			endpoint = scheme + endpoint
		}
		if !strings.HasSuffix(endpoint, "/") {
			endpoint += "/"
		}
		return endpoint, false
	}

	if strings.HasPrefix(listenAddress, "unix:") ||
		strings.HasPrefix(listenAddress, "unix://") {
		return scheme + "localhost" + apiPath, true
	}
	if strings.HasPrefix(listenAddress, ":") {
		listenAddress = "localhost" + listenAddress
	}
	return scheme + listenAddress + apiPath, false
}
