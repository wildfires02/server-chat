package translate

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestFileSecretResolver(t *testing.T) {
	path := filepath.Join(t.TempDir(), "provider.key")
	if err := os.WriteFile(path, []byte("  secret-value\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	value, err := FileSecretResolver(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	if string(value) != "secret-value" {
		t.Fatalf("unexpected credential value %q", value)
	}
}

func TestRouterFallsBackAndOpensCircuit(t *testing.T) {
	var failedCalls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "first.example" {
			failedCalls.Add(1)
			return jsonResponse(http.StatusServiceUnavailable, `{"error":"unavailable"}`), nil
		}
		return jsonResponse(http.StatusOK,
			`{"translatedText":"hello","detectedLanguage":{"language":"zh"}}`), nil
	})}

	resolver := func(_ context.Context, _ string) ([]byte, error) { return []byte("key"), nil }
	router, err := NewRouter(Config{
		MaxAttempts: 2,
		Providers: []ProviderConfig{
			{
				ID: "first", Type: "libretranslate", Enabled: true, Endpoint: "https://first.example",
				Timeout: time.Second, FailureThreshold: 1, OpenDuration: time.Minute,
			},
			{
				ID: "second", Type: "libretranslate", Enabled: true, Endpoint: "https://second.example",
				Timeout: time.Second,
			},
		},
		Routes: []Route{{Source: "zh", Target: "en", Providers: []string{"first", "second"}}},
	}, resolver, client)
	if err != nil {
		t.Fatal(err)
	}
	for range 2 {
		result, err := router.Translate(context.Background(), Request{
			Text: "你好", SourceLanguage: "zh-CN", TargetLanguage: "en-US",
		})
		if err != nil {
			t.Fatal(err)
		}
		if result.Text != "hello" || result.Provider != "second" {
			t.Fatalf("unexpected result: %#v", result)
		}
	}
	if calls := failedCalls.Load(); calls != 1 {
		t.Fatalf("circuit breaker did not skip failed provider: calls=%d", calls)
	}
}

func TestRouterUsesFirstMatchingLanguageRoute(t *testing.T) {
	var firstCalls atomic.Int32
	var secondCalls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "first.example" {
			firstCalls.Add(1)
			return jsonResponse(http.StatusOK, `{"translatedText":"wrong"}`), nil
		}
		secondCalls.Add(1)
		return jsonResponse(http.StatusOK, `{"translatedText":"right"}`), nil
	})}
	resolve := func(_ context.Context, _ string) ([]byte, error) { return []byte("key"), nil }
	router, err := NewRouter(Config{
		Providers: []ProviderConfig{
			{ID: "first", Type: "libretranslate", Enabled: true, Endpoint: "https://first.example"},
			{ID: "second", Type: "libretranslate", Enabled: true, Endpoint: "https://second.example"},
		},
		Routes: []Route{
			{Source: "en", Target: "fr", Providers: []string{"first"}},
			{Source: "*", Target: "zh", Providers: []string{"second"}},
		},
	}, resolve, client)
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.Translate(context.Background(),
		Request{Text: "hello", SourceLanguage: "en-US", TargetLanguage: "zh-CN"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Text != "right" || firstCalls.Load() != 0 || secondCalls.Load() != 1 {
		t.Fatalf("wrong route: result=%#v first=%d second=%d",
			result, firstCalls.Load(), secondCalls.Load())
	}
}

func TestMonthlyQuotaFallsThrough(t *testing.T) {
	var firstCalls atomic.Int32
	client := &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Host == "first.example" {
			firstCalls.Add(1)
			return jsonResponse(http.StatusOK, `{"translatedText":"first"}`), nil
		}
		return jsonResponse(http.StatusOK, `{"translatedText":"second"}`), nil
	})}
	resolve := func(_ context.Context, _ string) ([]byte, error) { return []byte("key"), nil }
	router, err := NewRouter(Config{
		Providers: []ProviderConfig{
			{
				ID: "first", Type: "libretranslate", Enabled: true,
				Endpoint: "https://first.example", MonthlyCharacterLimit: 1,
			},
			{ID: "second", Type: "libretranslate", Enabled: true, Endpoint: "https://second.example"},
		},
	}, resolve, client)
	if err != nil {
		t.Fatal(err)
	}
	result, err := router.Translate(context.Background(),
		Request{Text: "hello", TargetLanguage: "zh"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Provider != "second" || firstCalls.Load() != 0 {
		t.Fatalf("quota was not enforced: %#v, calls=%d", result, firstCalls.Load())
	}
}

func TestProtectedValuesAreRestoredAndValidated(t *testing.T) {
	protected, placeholders := protectText("Pay $1,200 to @alice: https://example.com/a?id=7 and `code()`")
	if protected == "" || len(placeholders) != 4 {
		t.Fatalf("unexpected placeholders: %q %#v", protected, placeholders)
	}
	restored, err := restoreText("Translated "+protected, placeholders)
	if err != nil {
		t.Fatal(err)
	}
	if restored != "Translated Pay $1,200 to @alice: https://example.com/a?id=7 and `code()`" {
		t.Fatalf("unexpected restored text: %q", restored)
	}
	if _, err = restoreText(strings.Replace(protected, "⟦IM0⟧", "changed", 1),
		placeholders); err == nil {
		t.Fatal("changed placeholder must be rejected")
	}
}

func TestSupportedVendorResponses(t *testing.T) {
	tests := []struct {
		name     string
		config   ProviderConfig
		secret   string
		response string
		want     string
		check    func(*testing.T, *http.Request)
	}{
		{
			name: "azure",
			config: ProviderConfig{
				ID: "vendor", Type: "azure", Enabled: true,
				Endpoint: "https://azure.example", CredentialFile: "/run/secrets/translate", Region: "eastus",
			},
			secret:   "azure-key",
			response: `[{"detectedLanguage":{"language":"zh"},"translations":[{"text":"Hello"}]}]`,
			want:     "Hello",
			check: func(t *testing.T, request *http.Request) {
				if request.URL.Path != "/translate" ||
					request.Header.Get("Ocp-Apim-Subscription-Key") != "azure-key" {
					t.Fatalf("unexpected Azure request: %s %#v", request.URL, request.Header)
				}
			},
		},
		{
			name: "google",
			config: ProviderConfig{
				ID: "vendor", Type: "google", Enabled: true,
				Endpoint: "https://google.example", CredentialFile: "/run/secrets/translate",
			},
			secret:   "google-key",
			response: `{"data":{"translations":[{"translatedText":"Hello &amp; bye","detectedSourceLanguage":"zh"}]}}`,
			want:     "Hello & bye",
			check: func(t *testing.T, request *http.Request) {
				if request.URL.Query().Get("key") != "google-key" {
					t.Fatalf("Google API key missing")
				}
			},
		},
		{
			name: "deepl",
			config: ProviderConfig{
				ID: "vendor", Type: "deepl", Enabled: true,
				Endpoint: "https://deepl.example", CredentialFile: "/run/secrets/translate",
			},
			secret:   "deepl-key",
			response: `{"translations":[{"text":"Hello","detected_source_language":"ZH"}]}`,
			want:     "Hello",
			check: func(t *testing.T, request *http.Request) {
				if request.Header.Get("Authorization") != "DeepL-Auth-Key deepl-key" {
					t.Fatalf("DeepL authorization missing")
				}
			},
		},
		{
			name: "aws",
			config: ProviderConfig{
				ID: "vendor", Type: "aws", Enabled: true,
				Endpoint: "https://aws.example", CredentialFile: "/run/secrets/translate", Region: "us-east-1",
			},
			secret:   `{"access_key_id":"access","secret_access_key":"secret","session_token":"token"}`,
			response: `{"TranslatedText":"Hello","SourceLanguageCode":"zh"}`,
			want:     "Hello",
			check: func(t *testing.T, request *http.Request) {
				if !strings.HasPrefix(request.Header.Get("Authorization"), "AWS4-HMAC-SHA256 ") ||
					request.Header.Get("X-Amz-Security-Token") != "token" {
					t.Fatalf("AWS signature headers missing: %#v", request.Header)
				}
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := &http.Client{Transport: roundTripFunc(
				func(request *http.Request) (*http.Response, error) {
					test.check(t, request)
					return jsonResponse(http.StatusOK, test.response), nil
				})}
			resolve := func(_ context.Context, ref string) ([]byte, error) {
				if ref != "/run/secrets/translate" {
					t.Fatalf("unexpected secret reference: %q", ref)
				}
				return []byte(test.secret), nil
			}
			router, err := NewRouter(Config{Providers: []ProviderConfig{test.config}},
				resolve, client)
			if err != nil {
				t.Fatal(err)
			}
			result, err := router.Translate(context.Background(),
				Request{Text: "你好", SourceLanguage: "zh", TargetLanguage: "en"})
			if err != nil {
				t.Fatal(err)
			}
			if result.Text != test.want || result.Provider != "vendor" {
				t.Fatalf("unexpected result: %#v", result)
			}
		})
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
