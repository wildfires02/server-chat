// 本包提供可在单机或多台执行节点运行的即时通信压测能力。
package loadtest

import (
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// 综合场景会在登录后遍历已有订阅并发布消息。
	ScenarioMixed = "mixed"
	// 个人主题场景会创建大量连接并保持在线。
	ScenarioMe = "me"
	// 热点主题场景会让大量连接订阅并发布到同一个主题。
	ScenarioHotTopic = "hot-topic"

	// 默认协议版本与当前服务端测试协议保持一致。
	DefaultProtocolVersion = "0.29"
	// 默认接口密钥来自开发配置，只能用于本地或隔离测试。
	DefaultAPIKey = "AQEAAAABAAD_rAp4DJh05a1HAwFT3A6K"
)

// 压测账号包含用户名、密码及可选的缓存令牌。
type Account struct {
	Username string `json:"username"`
	Password string `json:"password"`
	Token    string `json:"token,omitempty"`
}

// 工作负载配置描述一个执行节点需要运行的完整任务。
type WorkloadConfig struct {
	RunID           string        `json:"run_id"`
	WorkerID        string        `json:"worker_id"`
	WebSocketURL    string        `json:"websocket_url"`
	APIKey          string        `json:"api_key"`
	ProtocolVersion string        `json:"protocol_version"`
	Scenario        string        `json:"scenario"`
	Topic           string        `json:"topic,omitempty"`
	Sessions        int           `json:"sessions"`
	Ramp            time.Duration `json:"ramp"`
	Duration        time.Duration `json:"duration"`
	RequestTimeout  time.Duration `json:"request_timeout"`
	PublishCount    int           `json:"publish_count"`
	PublishInterval time.Duration `json:"publish_interval"`
	MaxTopics       int           `json:"max_topics"`
	StartAt         time.Time     `json:"start_at"`
	Accounts        []Account     `json:"accounts"`
}

// 配置校验会检查影响执行安全和结果有效性的参数。
func (config WorkloadConfig) Validate() error {
	if strings.TrimSpace(config.RunID) == "" {
		return errors.New("运行标识不能为空")
	}
	if strings.TrimSpace(config.WorkerID) == "" {
		return errors.New("执行节点标识不能为空")
	}
	switch config.Scenario {
	case ScenarioMixed, ScenarioMe, ScenarioHotTopic:
	default:
		return fmt.Errorf("不支持的压测场景 %q", config.Scenario)
	}
	if config.WebSocketURL == "" {
		return errors.New("WebSocket 地址不能为空")
	}
	endpoint, err := url.ParseRequestURI(config.WebSocketURL)
	if err != nil {
		return fmt.Errorf("WebSocket 地址无效: %w", err)
	}
	switch endpoint.Scheme {
	case "http", "https", "ws", "wss":
	default:
		return errors.New("WebSocket 地址必须使用 ws、wss、http 或 https 协议")
	}
	if endpoint.Host == "" {
		return errors.New("WebSocket 地址必须包含主机名")
	}
	if config.APIKey == "" {
		return errors.New("接口密钥不能为空")
	}
	if config.ProtocolVersion == "" {
		return errors.New("协议版本不能为空")
	}
	if config.Sessions <= 0 {
		return errors.New("连接数必须大于零")
	}
	if config.Ramp < 0 {
		return errors.New("升压时间不能为负数")
	}
	if config.Duration <= 0 {
		return errors.New("运行时间必须大于零")
	}
	if config.RequestTimeout <= 0 {
		return errors.New("请求超时时间必须大于零")
	}
	if config.PublishCount < 0 {
		return errors.New("发布次数不能为负数")
	}
	if config.PublishInterval < 0 {
		return errors.New("发布间隔不能为负数")
	}
	if config.MaxTopics < 0 {
		return errors.New("最大主题数不能为负数")
	}
	if len(config.Accounts) == 0 {
		return errors.New("至少需要一个压测账号")
	}
	for index, account := range config.Accounts {
		if strings.TrimSpace(account.Username) == "" {
			return fmt.Errorf("第 %d 个压测账号的用户名为空", index+1)
		}
		if account.Password == "" && account.Token == "" {
			return fmt.Errorf("第 %d 个压测账号必须提供密码或令牌", index+1)
		}
	}
	if config.Scenario == ScenarioHotTopic && strings.TrimSpace(config.Topic) == "" {
		return errors.New("热点主题场景必须指定主题")
	}
	return nil
}

// 账号加载会从带表头的逗号分隔值文件读取压测账号。
func LoadAccounts(path string) ([]Account, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	return ReadAccounts(file)
}

// 账号解析会从逗号分隔值数据流读取用户名、密码和可选令牌列。
func ReadAccounts(reader io.Reader) ([]Account, error) {
	csvReader := csv.NewReader(reader)
	csvReader.TrimLeadingSpace = true

	header, err := csvReader.Read()
	if err != nil {
		return nil, fmt.Errorf("读取账号表头失败: %w", err)
	}
	columns := make(map[string]int, len(header))
	for index, name := range header {
		columns[strings.ToLower(strings.TrimSpace(name))] = index
	}
	usernameColumn, hasUsername := columns["username"]
	passwordColumn, hasPassword := columns["password"]
	tokenColumn, hasToken := columns["token"]
	if !hasUsername || !hasPassword {
		return nil, errors.New("账号文件必须包含 username 和 password 列")
	}

	var accounts []Account
	for rowNumber := 2; ; rowNumber++ {
		row, readErr := csvReader.Read()
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("读取账号文件第 %d 行失败: %w", rowNumber, readErr)
		}
		account := Account{
			Username: csvValue(row, usernameColumn),
			Password: csvValue(row, passwordColumn),
		}
		if hasToken {
			account.Token = csvValue(row, tokenColumn)
		}
		if account.Username == "" {
			return nil, fmt.Errorf("账号文件第 %d 行用户名为空", rowNumber)
		}
		if account.Password == "" && account.Token == "" {
			return nil, fmt.Errorf("账号文件第 %d 行必须提供密码或令牌", rowNumber)
		}
		accounts = append(accounts, account)
	}
	if len(accounts) == 0 {
		return nil, errors.New("账号文件不包含数据")
	}
	return accounts, nil
}

func csvValue(row []string, index int) string {
	if index < 0 || index >= len(row) {
		return ""
	}
	return strings.TrimSpace(row[index])
}

// 账号分片会按执行节点序号稳定分配账号。
func PartitionAccounts(accounts []Account, workerIndex, workerCount int) []Account {
	if len(accounts) == 0 || workerCount <= 0 || workerIndex < 0 || workerIndex >= workerCount {
		return nil
	}
	partition := make([]Account, 0, (len(accounts)+workerCount-1)/workerCount)
	for index, account := range accounts {
		if index%workerCount == workerIndex {
			partition = append(partition, account)
		}
	}
	if len(partition) == 0 {
		partition = append(partition, accounts[workerIndex%len(accounts)])
	}
	return partition
}

// 总量分片会把全局数量尽可能均匀地分配给所有执行节点。
func PartitionTotal(total, workerIndex, workerCount int) int {
	if total <= 0 || workerCount <= 0 || workerIndex < 0 || workerIndex >= workerCount {
		return 0
	}
	result := total / workerCount
	if workerIndex < total%workerCount {
		result++
	}
	return result
}
