package server

import (
	"compress/gzip"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestGinCompressionGzip(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/payload", ginCompression(), func(context *gin.Context) {
		context.String(http.StatusOK, "compressed response")
	})

	request := httptest.NewRequest(http.MethodGet, "/payload", nil)
	request.Header.Set(acceptEncodingHeader, "br, gzip")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Header().Get("Content-Encoding") != "gzip" {
		t.Fatalf("Content-Encoding=%q，期望 gzip", response.Header().Get("Content-Encoding"))
	}
	reader, err := gzip.NewReader(response.Body)
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	body, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "compressed response" {
		t.Fatalf("正文=%q", body)
	}
}

func TestGinCompressionCanBeDisabled(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.GET("/payload", ginCompression(), func(context *gin.Context) {
		context.String(http.StatusOK, "plain response")
	})

	request := httptest.NewRequest(http.MethodGet, "/payload", nil)
	request.Header.Set(acceptEncodingHeader, "gzip;q=0")
	response := httptest.NewRecorder()
	router.ServeHTTP(response, request)

	if response.Header().Get("Content-Encoding") != "" {
		t.Fatalf("不应压缩，Content-Encoding=%q", response.Header().Get("Content-Encoding"))
	}
	if strings.TrimSpace(response.Body.String()) != "plain response" {
		t.Fatalf("正文=%q", response.Body.String())
	}
}
