package translate

import (
	"fmt"
	"net/url"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

var (
	settingsIDPattern = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
	languagePattern   = regexp.MustCompile(`^(?:\*|[A-Za-z]{2,8}(?:[-_][A-Za-z0-9]{1,8})*)$`)
)

// 设置是聊天运行时消耗的翻译配置。
// 它只包含秘密引用，从不包含凭据值。
type Settings struct {
	Enabled          bool               `json:"enabled"`
	StaffLanguage    string             `json:"staff_language"`
	CustomerLanguage string             `json:"customer_language"`
	KeepOriginal     bool               `json:"keep_original"`
	FailurePolicy    string             `json:"failure_policy"`
	DefaultTimeoutMS int                `json:"default_timeout_ms"`
	MaxAttempts      int                `json:"max_attempts"`
	Providers        []ProviderSettings `json:"providers"`
	Routes           []RouteSettings    `json:"routes"`
}

//提供商设置是一个后端的可序列化配置。
type ProviderSettings struct {
	ID                    string `json:"id"`
	Type                  string `json:"type"`
	Enabled               bool   `json:"enabled"`
	Priority              int    `json:"priority"`
	Endpoint              string `json:"endpoint,omitempty"`
	Region                string `json:"region,omitempty"`
	CredentialFile        string `json:"credential_file,omitempty"`
	ProjectID             string `json:"project_id,omitempty"`
	TimeoutMS             int    `json:"timeout_ms"`
	MonthlyCharacterLimit int64  `json:"monthly_character_limit,omitempty"`
	FailureThreshold      int    `json:"failure_threshold"`
	OpenSeconds           int    `json:"open_seconds"`
	GlossaryID            string `json:"glossary_id,omitempty"`
}

//RouteSettings为语言定义有序故障转移提供程序。
type RouteSettings struct {
	Source    string   `json:"source"`
	Target    string   `json:"target"`
	Providers []string `json:"providers"`
}

//NormalizeSettings填充安全默认值并规范化提供商元数据。
func NormalizeSettings(settings *Settings) {
	if settings.FailurePolicy == "" {
		settings.FailurePolicy = "hold"
	}
	if settings.DefaultTimeoutMS == 0 {
		settings.DefaultTimeoutMS = 5000
	}
	if settings.MaxAttempts == 0 {
		settings.MaxAttempts = 3
	}
	if settings.Providers == nil {
		settings.Providers = []ProviderSettings{}
	}
	if settings.Routes == nil {
		settings.Routes = []RouteSettings{}
	}
	for index := range settings.Providers {
		provider := &settings.Providers[index]
		provider.ID = strings.TrimSpace(provider.ID)
		provider.Type = strings.ToLower(strings.TrimSpace(provider.Type))
		provider.Endpoint = strings.TrimRight(strings.TrimSpace(provider.Endpoint), "/")
		provider.Region = strings.TrimSpace(provider.Region)
		provider.CredentialFile = strings.TrimSpace(provider.CredentialFile)
		if provider.TimeoutMS == 0 {
			provider.TimeoutMS = settings.DefaultTimeoutMS
		}
		if provider.FailureThreshold == 0 {
			provider.FailureThreshold = 3
		}
		if provider.OpenSeconds == 0 {
			provider.OpenSeconds = 30
		}
	}
}

//ValidateSettings验证完整的聊天运行时配置。
func ValidateSettings(settings Settings) error {
	if settings.FailurePolicy != "hold" && settings.FailurePolicy != "original" ||
		settings.DefaultTimeoutMS < 100 || settings.DefaultTimeoutMS > 30000 ||
		settings.MaxAttempts < 1 || settings.MaxAttempts > 10 ||
		len(settings.Providers) > 20 || len(settings.Routes) > 100 ||
		settings.StaffLanguage != "" && !validSettingsLanguage(settings.StaffLanguage, false) ||
		settings.CustomerLanguage != "" && !validSettingsLanguage(settings.CustomerLanguage, false) {
		return ErrInvalidConfig
	}
	providers := make(map[string]ProviderSettings, len(settings.Providers))
	enabled := 0
	for _, provider := range settings.Providers {
		switch provider.Type {
		case "azure", "google", "aws", "deepl", "libretranslate":
		default:
			return fmt.Errorf("%w: unsupported provider type", ErrInvalidConfig)
		}
		if !settingsIDPattern.MatchString(provider.ID) {
			return fmt.Errorf("%w: invalid provider id", ErrInvalidConfig)
		}
		if _, duplicate := providers[provider.ID]; duplicate {
			return fmt.Errorf("%w: duplicate provider id", ErrInvalidConfig)
		}
		if provider.Priority < 0 || provider.Priority > 10000 ||
			provider.TimeoutMS < 100 || provider.TimeoutMS > 30000 ||
			provider.MonthlyCharacterLimit < 0 ||
			provider.FailureThreshold < 1 || provider.FailureThreshold > 20 ||
			provider.OpenSeconds < 1 || provider.OpenSeconds > 3600 ||
			len(provider.Region) > 64 || len(provider.ProjectID) > 128 ||
			len(provider.GlossaryID) > 256 || len(provider.CredentialFile) > 1024 {
			return ErrInvalidConfig
		}
		if provider.Type != "libretranslate" && !validCredentialFile(provider.CredentialFile) {
			return fmt.Errorf("%w: credential file required", ErrInvalidConfig)
		}
		if provider.CredentialFile != "" && !validCredentialFile(provider.CredentialFile) {
			return fmt.Errorf("%w: invalid credential file", ErrInvalidConfig)
		}
		if provider.Type == "aws" && provider.Region == "" ||
			provider.Type == "libretranslate" && provider.Endpoint == "" ||
			!validSettingsEndpoint(provider.Endpoint) {
			return ErrInvalidConfig
		}
		if provider.Enabled {
			enabled++
		}
		providers[provider.ID] = provider
	}
	for _, route := range settings.Routes {
		if !validSettingsLanguage(route.Source, true) ||
			!validSettingsLanguage(route.Target, true) ||
			len(route.Providers) == 0 || len(route.Providers) > len(settings.Providers) {
			return ErrInvalidConfig
		}
		seen := make(map[string]struct{}, len(route.Providers))
		for _, id := range route.Providers {
			provider, ok := providers[id]
			if !ok || !provider.Enabled {
				return ErrInvalidConfig
			}
			if _, duplicate := seen[id]; duplicate {
				return ErrInvalidConfig
			}
			seen[id] = struct{}{}
		}
	}
	if settings.Enabled && (enabled == 0 || settings.StaffLanguage == "" ||
		settings.CustomerLanguage == "" ||
		settingsLanguageBase(settings.StaffLanguage) == settingsLanguageBase(settings.CustomerLanguage)) {
		return ErrInvalidConfig
	}
	return nil
}

//RouterConfig将序列化的设置转换为运行时值。
func (settings Settings) RouterConfig() Config {
	config := Config{MaxAttempts: settings.MaxAttempts}
	for _, provider := range settings.Providers {
		config.Providers = append(config.Providers, ProviderConfig{
			ID: provider.ID, Type: provider.Type, Enabled: provider.Enabled,
			Priority: provider.Priority, Endpoint: provider.Endpoint, Region: provider.Region,
			CredentialFile: provider.CredentialFile, ProjectID: provider.ProjectID,
			Timeout:               time.Duration(provider.TimeoutMS) * time.Millisecond,
			MonthlyCharacterLimit: provider.MonthlyCharacterLimit,
			FailureThreshold:      provider.FailureThreshold,
			OpenDuration:          time.Duration(provider.OpenSeconds) * time.Second,
			GlossaryID:            provider.GlossaryID,
		})
	}
	for _, route := range settings.Routes {
		config.Routes = append(config.Routes, Route{
			Source: route.Source, Target: route.Target,
			Providers: append([]string(nil), route.Providers...),
		})
	}
	return config
}

func validSettingsLanguage(language string, wildcard bool) bool {
	if language == "" {
		return wildcard
	}
	if language == "*" && !wildcard {
		return false
	}
	return languagePattern.MatchString(language)
}

func validCredentialFile(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path &&
		!strings.ContainsRune(path, '\x00')
}

func validSettingsEndpoint(endpoint string) bool {
	if endpoint == "" {
		return true
	}
	parsed, err := url.Parse(endpoint)
	return err == nil && (parsed.Scheme == "http" || parsed.Scheme == "https") &&
		parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
}

func settingsLanguageBase(language string) string {
	language = strings.ToLower(strings.ReplaceAll(strings.TrimSpace(language), "_", "-"))
	if index := strings.IndexByte(language, '-'); index >= 0 {
		return language[:index]
	}
	return language
}
