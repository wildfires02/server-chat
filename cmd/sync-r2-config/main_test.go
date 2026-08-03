package main

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestMapR2Endpoints(t *testing.T) {
	tests := []struct {
		name         string
		config       cloudflareConfig
		wantEndpoint string
		wantCDN      string
	}{
		{
			name: "S3 API endpoint",
			config: cloudflareConfig{
				Endpoint: "https://account.r2.cloudflarestorage.com/",
			},
			wantEndpoint: "https://account.r2.cloudflarestorage.com",
		},
		{
			name: "public custom domain",
			config: cloudflareConfig{
				Endpoint:  "media.example.com/",
				AccountID: "account",
			},
			wantEndpoint: "https://account.r2.cloudflarestorage.com",
			wantCDN:      "https://media.example.com",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			endpoint, cdn, err := mapR2Endpoints(test.config)
			if err != nil {
				t.Fatal(err)
			}
			if endpoint != test.wantEndpoint || cdn != test.wantCDN {
				t.Fatalf("got endpoint=%q cdn=%q", endpoint, cdn)
			}
		})
	}
}

func TestResolveR2EndpointsQueriesCustomDomain(t *testing.T) {
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != "/client/v4/accounts/account/r2/buckets/assets/domains/custom" {
			t.Fatalf("unexpected Cloudflare path: %s", request.URL.Path)
		}
		if request.Header.Get("Authorization") != "Bearer token" {
			t.Fatal("Cloudflare API token was not sent")
		}
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     http.Header{"Content-Type": []string{"application/json"}},
			Body: io.NopCloser(strings.NewReader(`{
  "success": true,
  "result": {
    "domains": [
      {"domain":"pending.example.com","enabled":true,"status":{"ownership":"pending"}},
      {"domain":"media.example.com","enabled":true,"status":{"ownership":"active"}}
    ]
  }
}`)),
		}, nil
	})}

	endpoint, cdn, err := resolveR2Endpoints(context.Background(), client, "https://api.cloudflare.test/client/v4", cloudflareConfig{
		Bucket:    "assets",
		Endpoint:  "https://account.r2.cloudflarestorage.com",
		AccountID: "account",
		APIToken:  "token",
	})
	if err != nil {
		t.Fatal(err)
	}
	if endpoint != "https://account.r2.cloudflarestorage.com" || cdn != "https://media.example.com" {
		t.Fatalf("got endpoint=%q cdn=%q", endpoint, cdn)
	}
}

func TestUpdateIMConfigPreservesComments(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "im.yaml")
	original := `# 配置说明
media:
  handlers:
    s3:
      # 密钥说明
      access_key_id: ""
      secret_access_key: ""
      bucket: ""
      endpoint: ""
      cdn_base_url: ""

# HTTP 与 gRPC 共用的 TLS 配置。
tls:
  enabled: false
`
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := updateIMConfig(path, map[string]string{
		"access_key_id": "access",
		"bucket":        "bucket",
	}); err != nil {
		t.Fatal(err)
	}
	updated, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(updated)
	for _, expected := range []string{"# 配置说明", "# 密钥说明", `access_key_id: "access"`, `bucket: "bucket"`} {
		if !strings.Contains(text, expected) {
			t.Fatalf("updated config is missing %q", expected)
		}
	}
}
