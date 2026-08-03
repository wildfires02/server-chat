package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"go.yaml.in/yaml/v3"
)

type sourceConfig struct {
	MySQL struct {
		Path     string `yaml:"path"`
		Port     int    `yaml:"port"`
		DBName   string `yaml:"db-name"`
		Username string `yaml:"username"`
		Password string `yaml:"password"`
	} `yaml:"mysql"`
}

type cloudflareConfig struct {
	Bucket    string
	AccessKey string
	SecretKey string
	Endpoint  string
	AccountID string
	APIToken  string
	Enabled   bool
}

const cloudflareAPIBaseURL = "https://api.cloudflare.com/client/v4"

func main() {
	serverConfigPath := flag.String("server-config", "../server/config.yaml", "Groupbuying server config path")
	imConfigPath := flag.String("im-config", "configs/im.yaml", "server-chat config path")
	flag.Parse()

	if err := syncConfig(*serverConfigPath, *imConfigPath); err != nil {
		fmt.Fprintln(os.Stderr, "Unable to synchronize R2 configuration:", err)
		os.Exit(1)
	}
	fmt.Println("Cloudflare R2 configuration synchronized without printing credentials.")
}

func syncConfig(serverConfigPath, imConfigPath string) error {
	// 只读取商城服务的数据库连接信息，不读取其运行时缓存或内部接口。
	source, err := readSourceConfig(serverConfigPath)
	if err != nil {
		return err
	}
	config, err := readDefaultCloudflareConfig(source)
	if err != nil {
		return err
	}
	if !config.Enabled {
		return errors.New("the default Cloudflare configuration is disabled")
	}
	if strings.TrimSpace(config.Bucket) == "" || strings.TrimSpace(config.AccessKey) == "" || strings.TrimSpace(config.SecretKey) == "" {
		return errors.New("the default Cloudflare configuration is incomplete")
	}

	endpoint, cdnBaseURL, err := resolveR2Endpoints(context.Background(), &http.Client{Timeout: 8 * time.Second}, cloudflareAPIBaseURL, config)
	if err != nil {
		return err
	}
	updates := map[string]string{
		"access_key_id":     strings.TrimSpace(config.AccessKey),
		"secret_access_key": strings.TrimSpace(config.SecretKey),
		"bucket":            strings.TrimSpace(config.Bucket),
		"endpoint":          endpoint,
		"cdn_base_url":      cdnBaseURL,
	}
	return updateIMConfig(imConfigPath, updates)
}

func readSourceConfig(path string) (sourceConfig, error) {
	var config sourceConfig
	raw, err := os.ReadFile(path)
	if err != nil {
		return config, err
	}
	if err := yaml.Unmarshal(raw, &config); err != nil {
		return config, err
	}
	if config.MySQL.Path == "" || config.MySQL.Port <= 0 || config.MySQL.DBName == "" || config.MySQL.Username == "" {
		return config, errors.New("the Groupbuying MySQL configuration is incomplete")
	}
	return config, nil
}

func readDefaultCloudflareConfig(config sourceConfig) (cloudflareConfig, error) {
	driverConfig := mysqldriver.NewConfig()
	driverConfig.User = config.MySQL.Username
	driverConfig.Passwd = config.MySQL.Password
	driverConfig.Net = "tcp"
	driverConfig.Addr = net.JoinHostPort(config.MySQL.Path, strconv.Itoa(config.MySQL.Port))
	driverConfig.DBName = config.MySQL.DBName
	driverConfig.Timeout = 5 * time.Second
	driverConfig.ReadTimeout = 5 * time.Second
	driverConfig.WriteTimeout = 5 * time.Second

	database, err := sql.Open("mysql", driverConfig.FormatDSN())
	if err != nil {
		return cloudflareConfig{}, err
	}
	defer database.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	row := database.QueryRowContext(ctx, `
		SELECT
			COALESCE(r2_bucket, ''),
			COALESCE(r2_access_key, ''),
			COALESCE(r2_secret_key, ''),
			COALESCE(r2_endpoint, ''),
			COALESCE(account_id, ''),
			COALESCE(api_token, ''),
			COALESCE(status, 0)
		FROM sys_cloudflare_configs
		WHERE is_default = 1
		ORDER BY id DESC
		LIMIT 1`)
	var result cloudflareConfig
	if err := row.Scan(&result.Bucket, &result.AccessKey, &result.SecretKey, &result.Endpoint, &result.AccountID, &result.APIToken, &result.Enabled); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return result, errors.New("no default Cloudflare configuration was found")
		}
		return result, err
	}
	return result, nil
}

// resolveR2Endpoints 优先查询桶当前绑定的自定义域名，数据库中的公开地址仅作为回退。
func resolveR2Endpoints(ctx context.Context, client *http.Client, apiBaseURL string, config cloudflareConfig) (string, string, error) {
	s3Endpoint, fallbackCDN, err := mapR2Endpoints(config)
	if err != nil {
		return "", "", err
	}
	if strings.TrimSpace(config.APIToken) == "" {
		return s3Endpoint, fallbackCDN, nil
	}
	customDomain, err := fetchCloudflareCustomDomain(ctx, client, apiBaseURL, config)
	if err != nil {
		if fallbackCDN != "" {
			return s3Endpoint, fallbackCDN, nil
		}
		return "", "", err
	}
	if customDomain == "" {
		return s3Endpoint, fallbackCDN, nil
	}
	return s3Endpoint, normalizePublicURL(customDomain), nil
}

// fetchCloudflareCustomDomain 返回第一个已启用且所有权验证完成的 R2 自定义域名。
func fetchCloudflareCustomDomain(ctx context.Context, client *http.Client, apiBaseURL string, config cloudflareConfig) (string, error) {
	accountID := strings.TrimSpace(config.AccountID)
	bucket := strings.TrimSpace(config.Bucket)
	if accountID == "" || bucket == "" {
		return "", errors.New("Cloudflare account_id and bucket are required to query the custom domain")
	}
	endpoint := strings.TrimRight(apiBaseURL, "/") + "/accounts/" + url.PathEscape(accountID) + "/r2/buckets/" + url.PathEscape(bucket) + "/domains/custom"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Authorization", "Bearer "+strings.TrimSpace(config.APIToken))
	request.Header.Set("Accept", "application/json")

	response, err := client.Do(request)
	if err != nil {
		return "", fmt.Errorf("unable to query the Cloudflare R2 custom domain: %w", err)
	}
	defer response.Body.Close()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return "", fmt.Errorf("Cloudflare R2 custom domain query returned HTTP %d", response.StatusCode)
	}
	var envelope struct {
		Success bool `json:"success"`
		Result  struct {
			Domains []struct {
				Domain  string `json:"domain"`
				Enabled bool   `json:"enabled"`
				Status  struct {
					Ownership string `json:"ownership"`
				} `json:"status"`
			} `json:"domains"`
		} `json:"result"`
	}
	if err := json.NewDecoder(response.Body).Decode(&envelope); err != nil {
		return "", fmt.Errorf("unable to decode the Cloudflare R2 custom domain response: %w", err)
	}
	if !envelope.Success {
		return "", errors.New("Cloudflare rejected the R2 custom domain query")
	}
	for _, domain := range envelope.Result.Domains {
		ownership := strings.ToLower(strings.TrimSpace(domain.Status.Ownership))
		if domain.Enabled && (ownership == "active" || ownership == "verified") && strings.TrimSpace(domain.Domain) != "" {
			return domain.Domain, nil
		}
	}
	return "", nil
}

func mapR2Endpoints(config cloudflareConfig) (string, string, error) {
	rawEndpoint := strings.TrimRight(strings.TrimSpace(config.Endpoint), "/")
	accountID := strings.TrimSpace(config.AccountID)
	if strings.Contains(strings.ToLower(rawEndpoint), ".r2.cloudflarestorage.com") {
		return rawEndpoint, "", nil
	}
	if accountID == "" {
		return "", "", errors.New("Cloudflare account_id is required to build the R2 S3 endpoint")
	}
	s3Endpoint := "https://" + accountID + ".r2.cloudflarestorage.com"
	if rawEndpoint == "" {
		return s3Endpoint, "", nil
	}
	return s3Endpoint, normalizePublicURL(rawEndpoint), nil
}

func normalizePublicURL(value string) string {
	value = strings.TrimRight(strings.TrimSpace(value), "/")
	if value != "" && !strings.HasPrefix(value, "https://") && !strings.HasPrefix(value, "http://") {
		value = "https://" + value
	}
	return value
}

func updateIMConfig(path string, updates map[string]string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	text := string(raw)
	sectionStart := strings.Index(text, "    s3:\n")
	if sectionStart < 0 {
		return errors.New("media.handlers.s3 was not found in the server-chat configuration")
	}
	// 主配置会省略未启用模块，因此不能依赖某个固定的下一节名称。
	sectionEnd := strings.Index(text[sectionStart:], "\n# ")
	if sectionEnd < 0 {
		sectionEnd = len(text)
	} else {
		sectionEnd += sectionStart
	}
	section := text[sectionStart:sectionEnd]
	for key, value := range updates {
		pattern := regexp.MustCompile(`(?m)^      ` + regexp.QuoteMeta(key) + `:[^\r\n]*$`)
		if !pattern.MatchString(section) {
			return fmt.Errorf("media.handlers.s3.%s was not found", key)
		}
		section = pattern.ReplaceAllString(section, "      "+key+": "+strconv.Quote(value))
	}
	updated := text[:sectionStart] + section + text[sectionEnd:]
	if updated == text {
		return nil
	}
	return writeAtomically(path, []byte(updated))
}

func writeAtomically(path string, content []byte) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".im-config-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(info.Mode().Perm()); err != nil {
		temporary.Close()
		return err
	}
	if _, err := temporary.Write(content); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}
