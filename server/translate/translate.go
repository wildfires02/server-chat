// Package translate provides pluggable machine-translation clients and
// fail-over routing without coupling the chat server to a single vendor.
package translate

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultTimeout          = 1500 * time.Millisecond
	defaultFailureThreshold = 3
	defaultOpenDuration     = 30 * time.Second
	defaultMaxAttempts      = 3
	maxResponseBytes        = 1 << 20
)

var (
	// ErrNoProvider means that no configured provider can serve the request.
	ErrNoProvider = errors.New("translate: no provider available")
	// ErrInvalidConfig means that a provider or route is malformed.
	ErrInvalidConfig = errors.New("translate: invalid configuration")
	// ErrSecretNotFound means that a credential reference could not be resolved.
	ErrSecretNotFound    = errors.New("translate: credential not found")
	protectedTextPattern = regexp.MustCompile("(?s)```.*?```|`[^`\\n]+`|https?://[^\\s]+|www\\.[^\\s]+|[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\\.[A-Za-z]{2,}|@[A-Za-z0-9_]{1,64}|[¥$€£]?\\d[\\d,.]*%?")
)

// Request is one plain-text translation operation. SourceLanguage may be
// empty or "auto".
type Request struct {
	Text           string
	SourceLanguage string
	TargetLanguage string
}

// Result contains the translated text and the provider which produced it.
type Result struct {
	Text                   string
	DetectedSourceLanguage string
	Provider               string
}

// ProviderConfig describes one configured translation backend. Secrets are
// loaded from CredentialFile and never stored in the control plane.
type ProviderConfig struct {
	ID                    string
	Type                  string
	Enabled               bool
	Priority              int
	Endpoint              string
	Region                string
	CredentialFile        string
	ProjectID             string
	Timeout               time.Duration
	MonthlyCharacterLimit int64
	FailureThreshold      int
	OpenDuration          time.Duration
	GlossaryID            string
}

// Route chooses an ordered provider list for a language pair. Empty or "*"
// matches any language.
type Route struct {
	Source    string
	Target    string
	Providers []string
}

// Config is the immutable router configuration.
type Config struct {
	Providers   []ProviderConfig
	Routes      []Route
	MaxAttempts int
}

// SecretResolver resolves an opaque secret name. Implementations must not
// expose secret values in errors.
type SecretResolver func(context.Context, string) ([]byte, error)

// FileSecretResolver resolves credentials from a read-only file.
func FileSecretResolver(_ context.Context, path string) ([]byte, error) {
	value, err := os.ReadFile(path)
	if err != nil || len(value) == 0 || len(value) > 64<<10 {
		return nil, fmt.Errorf("%w: %s", ErrSecretNotFound, path)
	}
	value = bytes.TrimSpace(value)
	if len(value) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrSecretNotFound, path)
	}
	return value, nil
}

// Provider is implemented by translation backends.
type Provider interface {
	Translate(context.Context, Request) (Result, error)
}

type providerState struct {
	config     ProviderConfig
	client     Provider
	failures   int
	openUntil  time.Time
	usageMonth string
	usageChars int64
}

// Router selects providers, applies quotas and circuit breaking, and falls
// through to the next provider when one is unavailable.
type Router struct {
	mu          sync.Mutex
	providers   map[string]*providerState
	configured  map[string]struct{}
	unavailable map[string]error
	ordered     []string
	routes      []Route
	maxAttempts int
	now         func() time.Time
}

// NewRouter validates config and constructs all provider clients.
func NewRouter(config Config, resolve SecretResolver, httpClient *http.Client) (*Router, error) {
	if resolve == nil {
		resolve = FileSecretResolver
	}
	if httpClient == nil {
		httpClient = http.DefaultClient
	}
	if config.MaxAttempts <= 0 {
		config.MaxAttempts = defaultMaxAttempts
	}
	router := &Router{
		providers:   make(map[string]*providerState),
		configured:  make(map[string]struct{}),
		unavailable: make(map[string]error),
		routes:      append([]Route(nil), config.Routes...),
		maxAttempts: config.MaxAttempts,
		now:         time.Now,
	}
	for _, item := range config.Providers {
		item.ID = strings.TrimSpace(item.ID)
		item.Type = strings.ToLower(strings.TrimSpace(item.Type))
		if item.ID == "" {
			return nil, fmt.Errorf("%w: provider id is required", ErrInvalidConfig)
		}
		if _, exists := router.configured[item.ID]; exists {
			return nil, fmt.Errorf("%w: duplicate provider %q", ErrInvalidConfig, item.ID)
		}
		router.configured[item.ID] = struct{}{}
		if !item.Enabled {
			continue
		}
		if item.Timeout <= 0 {
			item.Timeout = defaultTimeout
		}
		if item.FailureThreshold <= 0 {
			item.FailureThreshold = defaultFailureThreshold
		}
		if item.OpenDuration <= 0 {
			item.OpenDuration = defaultOpenDuration
		}
		client, err := newProvider(item, resolve, httpClient)
		if err != nil {
			router.unavailable[item.ID] = fmt.Errorf("translate: provider %q: %w", item.ID, err)
			continue
		}
		router.providers[item.ID] = &providerState{config: item, client: client}
		router.ordered = append(router.ordered, item.ID)
	}
	sort.SliceStable(router.ordered, func(i, j int) bool {
		left, right := router.providers[router.ordered[i]], router.providers[router.ordered[j]]
		if left.config.Priority == right.config.Priority {
			return left.config.ID < right.config.ID
		}
		return left.config.Priority < right.config.Priority
	})
	for _, route := range router.routes {
		if len(route.Providers) == 0 {
			return nil, fmt.Errorf("%w: route has no providers", ErrInvalidConfig)
		}
		for _, id := range route.Providers {
			if _, ok := router.configured[id]; !ok {
				return nil, fmt.Errorf("%w: route references provider %q", ErrInvalidConfig, id)
			}
		}
	}
	return router, nil
}

// Translate executes a request using the first matching route and automatic
// provider fail-over.
func (router *Router) Translate(ctx context.Context, request Request) (Result, error) {
	if strings.TrimSpace(request.Text) == "" || normalizeLanguage(request.TargetLanguage) == "" {
		return Result{}, fmt.Errorf("%w: text and target language are required", ErrInvalidConfig)
	}
	candidates := router.candidates(request.SourceLanguage, request.TargetLanguage)
	if len(candidates) == 0 {
		return Result{}, ErrNoProvider
	}
	var failures []error
	attempts := 0
	protectedText, placeholders := protectText(request.Text)
	providerRequest := request
	providerRequest.Text = protectedText
	for _, id := range candidates {
		if attempts >= router.maxAttempts {
			break
		}
		if unavailable := router.unavailable[id]; unavailable != nil {
			failures = append(failures, unavailable)
			continue
		}
		state, ok := router.reserve(id, int64(len([]rune(request.Text))))
		if !ok {
			continue
		}
		attempts++
		callCtx, cancel := context.WithTimeout(ctx, state.config.Timeout)
		result, err := state.client.Translate(callCtx, providerRequest)
		cancel()
		if err == nil && strings.TrimSpace(result.Text) == "" {
			err = errors.New("translate: provider returned empty text")
		}
		if err == nil {
			result.Text, err = restoreText(result.Text, placeholders)
		}
		if err == nil {
			result.Provider = id
			router.succeeded(id)
			return result, nil
		}
		router.failed(id)
		failures = append(failures, fmt.Errorf("%s: %w", id, err))
		if ctx.Err() != nil {
			return Result{}, ctx.Err()
		}
	}
	if len(failures) == 0 {
		return Result{}, ErrNoProvider
	}
	return Result{}, errors.Join(failures...)
}

// TranslateWithProvider bypasses route selection. It is intended for the
// management console's provider health check.
func (router *Router) TranslateWithProvider(ctx context.Context, id string, request Request) (Result, error) {
	state, ok := router.providers[id]
	if !ok {
		if err := router.unavailable[id]; err != nil {
			return Result{}, err
		}
		return Result{}, ErrNoProvider
	}
	callCtx, cancel := context.WithTimeout(ctx, state.config.Timeout)
	defer cancel()
	protectedText, placeholders := protectText(request.Text)
	request.Text = protectedText
	result, err := state.client.Translate(callCtx, request)
	if err != nil {
		return Result{}, err
	}
	if strings.TrimSpace(result.Text) == "" {
		return Result{}, errors.New("translate: provider returned empty text")
	}
	result.Text, err = restoreText(result.Text, placeholders)
	if err != nil {
		return Result{}, err
	}
	result.Provider = id
	return result, nil
}

type textPlaceholder struct {
	token string
	value string
}

func protectText(text string) (string, []textPlaceholder) {
	var placeholders []textPlaceholder
	protected := protectedTextPattern.ReplaceAllStringFunc(text, func(value string) string {
		token := fmt.Sprintf("⟦IM%d⟧", len(placeholders))
		placeholders = append(placeholders, textPlaceholder{token: token, value: value})
		return token
	})
	return protected, placeholders
}

func restoreText(text string, placeholders []textPlaceholder) (string, error) {
	for _, placeholder := range placeholders {
		if strings.Count(text, placeholder.token) != 1 {
			return "", errors.New("translate: protected placeholder was changed")
		}
		text = strings.Replace(text, placeholder.token, placeholder.value, 1)
	}
	return text, nil
}

func (router *Router) candidates(source, target string) []string {
	source = normalizeLanguage(source)
	target = normalizeLanguage(target)
	for _, route := range router.routes {
		if languageMatches(route.Source, source) && languageMatches(route.Target, target) {
			return unique(route.Providers)
		}
	}
	return append([]string(nil), router.ordered...)
}

func (router *Router) reserve(id string, characters int64) (*providerState, bool) {
	router.mu.Lock()
	defer router.mu.Unlock()
	state := router.providers[id]
	if state == nil {
		return nil, false
	}
	now := router.now()
	if now.Before(state.openUntil) {
		return nil, false
	}
	month := now.UTC().Format("2006-01")
	if state.usageMonth != month {
		state.usageMonth = month
		state.usageChars = 0
	}
	if state.config.MonthlyCharacterLimit > 0 &&
		state.usageChars+characters > state.config.MonthlyCharacterLimit {
		return nil, false
	}
	state.usageChars += characters
	return state, true
}

func (router *Router) succeeded(id string) {
	router.mu.Lock()
	defer router.mu.Unlock()
	if state := router.providers[id]; state != nil {
		state.failures = 0
		state.openUntil = time.Time{}
	}
}

func (router *Router) failed(id string) {
	router.mu.Lock()
	defer router.mu.Unlock()
	if state := router.providers[id]; state != nil {
		state.failures++
		if state.failures >= state.config.FailureThreshold {
			state.failures = 0
			state.openUntil = router.now().Add(state.config.OpenDuration)
		}
	}
}

func languageMatches(pattern, language string) bool {
	pattern = normalizeLanguage(pattern)
	if pattern == "" || pattern == "*" {
		return true
	}
	if pattern == language {
		return true
	}
	return primaryLanguage(pattern) == primaryLanguage(language)
}

func normalizeLanguage(language string) string {
	return strings.ToLower(strings.ReplaceAll(strings.TrimSpace(language), "_", "-"))
}

func primaryLanguage(language string) string {
	if index := strings.IndexByte(language, '-'); index >= 0 {
		return language[:index]
	}
	return language
}

func unique(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

type credentials struct {
	APIKey          string `json:"api_key"`
	AccessKeyID     string `json:"access_key_id"`
	SecretAccessKey string `json:"secret_access_key"`
	SessionToken    string `json:"session_token"`
}

func resolveCredentials(resolve SecretResolver, ref string) (credentials, error) {
	if strings.TrimSpace(ref) == "" {
		return credentials{}, fmt.Errorf("%w: credential_file is required", ErrInvalidConfig)
	}
	raw, err := resolve(context.Background(), ref)
	if err != nil {
		return credentials{}, err
	}
	var value credentials
	if len(raw) > 0 && raw[0] == '{' {
		if err = json.Unmarshal(raw, &value); err != nil {
			return credentials{}, fmt.Errorf("%w: malformed credential JSON", ErrInvalidConfig)
		}
	} else {
		value.APIKey = string(raw)
	}
	return value, nil
}

func newProvider(config ProviderConfig, resolve SecretResolver, client *http.Client) (Provider, error) {
	var credential credentials
	var err error
	if config.CredentialFile != "" {
		credential, err = resolveCredentials(resolve, config.CredentialFile)
		if err != nil {
			return nil, err
		}
	}
	switch config.Type {
	case "azure":
		if credential.APIKey == "" {
			return nil, fmt.Errorf("%w: api_key is required", ErrInvalidConfig)
		}
		endpoint, err := endpointURL(config.Endpoint, "https://api.cognitive.microsofttranslator.com")
		return &azureProvider{client: client, endpoint: endpoint, key: credential.APIKey, region: config.Region}, err
	case "google":
		if credential.APIKey == "" {
			return nil, fmt.Errorf("%w: api_key is required", ErrInvalidConfig)
		}
		endpoint, err := endpointURL(config.Endpoint, "https://translation.googleapis.com")
		return &googleProvider{
			client: client, endpoint: endpoint, key: credential.APIKey, projectID: config.ProjectID,
		}, err
	case "deepl":
		if credential.APIKey == "" {
			return nil, fmt.Errorf("%w: api_key is required", ErrInvalidConfig)
		}
		endpoint, err := endpointURL(config.Endpoint, "https://api-free.deepl.com")
		return &deepLProvider{
			client: client, endpoint: endpoint, key: credential.APIKey, glossaryID: config.GlossaryID,
		}, err
	case "libretranslate":
		endpoint, err := endpointURL(config.Endpoint, "")
		return &libreProvider{client: client, endpoint: endpoint, key: credential.APIKey}, err
	case "aws":
		if credential.AccessKeyID == "" || credential.SecretAccessKey == "" || config.Region == "" {
			return nil, fmt.Errorf("%w: AWS access keys and region are required", ErrInvalidConfig)
		}
		endpoint, err := endpointURL(config.Endpoint,
			"https://translate."+config.Region+".amazonaws.com")
		return &awsProvider{
			client: client, endpoint: endpoint, region: config.Region,
			accessKeyID: credential.AccessKeyID, secretAccessKey: credential.SecretAccessKey,
			sessionToken: credential.SessionToken,
		}, err
	default:
		return nil, fmt.Errorf("%w: unsupported provider type %q", ErrInvalidConfig, config.Type)
	}
}

func endpointURL(raw, fallback string) (*url.URL, error) {
	if strings.TrimSpace(raw) == "" {
		raw = fallback
	}
	if raw == "" {
		return nil, fmt.Errorf("%w: endpoint is required", ErrInvalidConfig)
	}
	parsed, err := url.Parse(raw)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
		parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("%w: invalid endpoint", ErrInvalidConfig)
	}
	return parsed, nil
}

type httpStatusError struct {
	Status int
	Body   string
}

func (err *httpStatusError) Error() string {
	return "translation provider returned HTTP " + strconv.Itoa(err.Status)
}

func doJSON(ctx context.Context, client *http.Client, method string, endpoint *url.URL,
	headers http.Header, body any, target any) error {
	payload, err := json.Marshal(body)
	if err != nil {
		return err
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/json")
	for name, values := range headers {
		for _, value := range values {
			request.Header.Add(name, value)
		}
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return &httpStatusError{Status: response.StatusCode}
	}
	if err = json.Unmarshal(raw, target); err != nil {
		return fmt.Errorf("translate: invalid provider response: %w", err)
	}
	return nil
}

func withPath(endpoint *url.URL, path string) *url.URL {
	cloned := *endpoint
	cloned.Path = strings.TrimRight(cloned.Path, "/") + path
	return &cloned
}

type azureProvider struct {
	client   *http.Client
	endpoint *url.URL
	key      string
	region   string
}

func (provider *azureProvider) Translate(ctx context.Context, request Request) (Result, error) {
	endpoint := withPath(provider.endpoint, "/translate")
	query := endpoint.Query()
	query.Set("api-version", "3.0")
	query.Set("to", request.TargetLanguage)
	if source := normalizeSource(request.SourceLanguage); source != "" {
		query.Set("from", source)
	}
	endpoint.RawQuery = query.Encode()
	headers := make(http.Header)
	headers.Set("Ocp-Apim-Subscription-Key", provider.key)
	if provider.region != "" {
		headers.Set("Ocp-Apim-Subscription-Region", provider.region)
	}
	var response []struct {
		DetectedLanguage struct {
			Language string `json:"language"`
		} `json:"detectedLanguage"`
		Translations []struct {
			Text string `json:"text"`
		} `json:"translations"`
	}
	err := doJSON(ctx, provider.client, http.MethodPost, endpoint, headers,
		[]map[string]string{{"Text": request.Text}}, &response)
	if err != nil {
		return Result{}, err
	}
	if len(response) == 0 || len(response[0].Translations) == 0 {
		return Result{}, errors.New("translate: empty Azure response")
	}
	return Result{
		Text:                   response[0].Translations[0].Text,
		DetectedSourceLanguage: response[0].DetectedLanguage.Language,
	}, nil
}

type googleProvider struct {
	client    *http.Client
	endpoint  *url.URL
	key       string
	projectID string
}

func (provider *googleProvider) Translate(ctx context.Context, request Request) (Result, error) {
	endpoint := withPath(provider.endpoint, "/language/translate/v2")
	query := endpoint.Query()
	query.Set("key", provider.key)
	endpoint.RawQuery = query.Encode()
	body := map[string]any{"q": request.Text, "target": request.TargetLanguage, "format": "text"}
	if source := normalizeSource(request.SourceLanguage); source != "" {
		body["source"] = source
	}
	var response struct {
		Data struct {
			Translations []struct {
				Text     string `json:"translatedText"`
				Detected string `json:"detectedSourceLanguage"`
			} `json:"translations"`
		} `json:"data"`
	}
	headers := make(http.Header)
	if provider.projectID != "" {
		headers.Set("X-Goog-User-Project", provider.projectID)
	}
	if err := doJSON(ctx, provider.client, http.MethodPost, endpoint, headers, body, &response); err != nil {
		return Result{}, err
	}
	if len(response.Data.Translations) == 0 {
		return Result{}, errors.New("translate: empty Google response")
	}
	translation := response.Data.Translations[0]
	return Result{Text: html.UnescapeString(translation.Text), DetectedSourceLanguage: translation.Detected}, nil
}

type deepLProvider struct {
	client     *http.Client
	endpoint   *url.URL
	key        string
	glossaryID string
}

func (provider *deepLProvider) Translate(ctx context.Context, request Request) (Result, error) {
	endpoint := withPath(provider.endpoint, "/v2/translate")
	headers := make(http.Header)
	headers.Set("Authorization", "DeepL-Auth-Key "+provider.key)
	body := map[string]any{
		"text":        []string{request.Text},
		"target_lang": deepLLanguage(request.TargetLanguage),
	}
	if source := normalizeSource(request.SourceLanguage); source != "" {
		body["source_lang"] = deepLLanguage(source)
	}
	if provider.glossaryID != "" {
		body["glossary_id"] = provider.glossaryID
	}
	var response struct {
		Translations []struct {
			Text     string `json:"text"`
			Detected string `json:"detected_source_language"`
		} `json:"translations"`
	}
	if err := doJSON(ctx, provider.client, http.MethodPost, endpoint, headers, body, &response); err != nil {
		return Result{}, err
	}
	if len(response.Translations) == 0 {
		return Result{}, errors.New("translate: empty DeepL response")
	}
	translation := response.Translations[0]
	return Result{Text: translation.Text, DetectedSourceLanguage: translation.Detected}, nil
}

func deepLLanguage(language string) string {
	return strings.ToUpper(strings.ReplaceAll(language, "_", "-"))
}

type libreProvider struct {
	client   *http.Client
	endpoint *url.URL
	key      string
}

func (provider *libreProvider) Translate(ctx context.Context, request Request) (Result, error) {
	endpoint := withPath(provider.endpoint, "/translate")
	source := normalizeSource(request.SourceLanguage)
	if source == "" {
		source = "auto"
	}
	body := map[string]any{
		"q": request.Text, "source": source, "target": request.TargetLanguage, "format": "text",
	}
	if provider.key != "" {
		body["api_key"] = provider.key
	}
	var response struct {
		Text     string `json:"translatedText"`
		Detected struct {
			Language string `json:"language"`
		} `json:"detectedLanguage"`
	}
	if err := doJSON(ctx, provider.client, http.MethodPost, endpoint, nil, body, &response); err != nil {
		return Result{}, err
	}
	if response.Text == "" {
		return Result{}, errors.New("translate: empty LibreTranslate response")
	}
	return Result{Text: response.Text, DetectedSourceLanguage: response.Detected.Language}, nil
}

type awsProvider struct {
	client          *http.Client
	endpoint        *url.URL
	region          string
	accessKeyID     string
	secretAccessKey string
	sessionToken    string
}

func (provider *awsProvider) Translate(ctx context.Context, request Request) (Result, error) {
	source := normalizeSource(request.SourceLanguage)
	if source == "" {
		source = "auto"
	}
	body := map[string]string{
		"Text": request.Text, "SourceLanguageCode": source,
		"TargetLanguageCode": request.TargetLanguage,
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return Result{}, err
	}
	now := time.Now().UTC()
	headers := make(http.Header)
	headers.Set("Content-Type", "application/x-amz-json-1.1")
	headers.Set("X-Amz-Target", "AWSShineFrontendService_20170701.TranslateText")
	headers.Set("X-Amz-Date", now.Format("20060102T150405Z"))
	headers.Set("Host", provider.endpoint.Host)
	if provider.sessionToken != "" {
		headers.Set("X-Amz-Security-Token", provider.sessionToken)
	}
	headers.Set("Authorization", provider.authorization(http.MethodPost, payload, headers, now))
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost,
		provider.endpoint.String(), bytes.NewReader(payload))
	if err != nil {
		return Result{}, err
	}
	httpRequest.Header = headers
	response, err := provider.client.Do(httpRequest)
	if err != nil {
		return Result{}, err
	}
	defer response.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes))
	if err != nil {
		return Result{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return Result{}, &httpStatusError{Status: response.StatusCode}
	}
	var decoded struct {
		Text     string `json:"TranslatedText"`
		Detected string `json:"SourceLanguageCode"`
	}
	if err = json.Unmarshal(raw, &decoded); err != nil {
		return Result{}, fmt.Errorf("translate: invalid AWS response: %w", err)
	}
	if decoded.Text == "" {
		return Result{}, errors.New("translate: empty AWS response")
	}
	return Result{Text: decoded.Text, DetectedSourceLanguage: decoded.Detected}, nil
}

func (provider *awsProvider) authorization(method string, payload []byte, headers http.Header,
	now time.Time) string {
	date := now.Format("20060102")
	signedHeaders := []string{"content-type", "host", "x-amz-date", "x-amz-target"}
	if provider.sessionToken != "" {
		signedHeaders = append(signedHeaders, "x-amz-security-token")
	}
	sort.Strings(signedHeaders)
	var canonicalHeaders strings.Builder
	for _, name := range signedHeaders {
		canonicalHeaders.WriteString(name)
		canonicalHeaders.WriteByte(':')
		canonicalHeaders.WriteString(strings.TrimSpace(headers.Get(name)))
		canonicalHeaders.WriteByte('\n')
	}
	payloadHash := sha256Hex(payload)
	canonicalRequest := strings.Join([]string{
		method, canonicalPath(provider.endpoint), canonicalQuery(provider.endpoint),
		canonicalHeaders.String(), strings.Join(signedHeaders, ";"), payloadHash,
	}, "\n")
	scope := date + "/" + provider.region + "/translate/aws4_request"
	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256", now.Format("20060102T150405Z"), scope,
		sha256Hex([]byte(canonicalRequest)),
	}, "\n")
	dateKey := hmacSHA256([]byte("AWS4"+provider.secretAccessKey), date)
	regionKey := hmacSHA256(dateKey, provider.region)
	serviceKey := hmacSHA256(regionKey, "translate")
	signingKey := hmacSHA256(serviceKey, "aws4_request")
	signature := hex.EncodeToString(hmacSHA256(signingKey, stringToSign))
	return "AWS4-HMAC-SHA256 Credential=" + provider.accessKeyID + "/" + scope +
		", SignedHeaders=" + strings.Join(signedHeaders, ";") + ", Signature=" + signature
}

func canonicalPath(endpoint *url.URL) string {
	if endpoint.EscapedPath() == "" {
		return "/"
	}
	return endpoint.EscapedPath()
}

func canonicalQuery(endpoint *url.URL) string {
	return endpoint.Query().Encode()
}

func sha256Hex(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func hmacSHA256(key []byte, value string) []byte {
	hash := hmac.New(sha256.New, key)
	_, _ = hash.Write([]byte(value))
	return hash.Sum(nil)
}

func normalizeSource(source string) string {
	source = normalizeLanguage(source)
	if source == "auto" {
		return ""
	}
	return source
}
