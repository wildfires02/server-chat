//go:build rethinkdb
// +build rethinkdb

// Package rethinkdb is a 数据库 adapter for RethinkDB.
package rethinkdb

import (
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"chat/server/store"

	rdb "gopkg.in/rethinkdb/rethinkdb-go.v6"
)

// adapter 保存 RethinkDB 连接数据。
type adapter struct {
	// conn 保存连接。
	conn *rdb.Session
	// dbName 保存数据库名称。
	dbName string
	// 最大返回记录数
	maxResults int
	// 最大返回消息记录数
	maxMessageResults int
	// version 保存版本。
	version int
}

const (
	// adpVersion 指定adp版本。
	adpVersion = 120
	// adapterName 指定adapter名称。
	adapterName = "rethinkdb"

	// defaultHost 指定默认Host。
	defaultHost = "localhost:28015"
	// defaultDatabase 指定默认Database。
	defaultDatabase = "im"

	// defaultMaxResults 指定默认MaxResults。
	defaultMaxResults = 1024
	// 此值受 Session 发送队列上限 (128) 限制。
	defaultMaxMessageResults = 100
)

// 配置字段说明参见 https://godoc.org/github.com/rethinkdb/rethinkdb-go#ConnectOpts
type configType struct {
	// Database 保存Database。
	Database string `json:"database,omitempty"`
	// Addresses 保存Addresses。
	Addresses any `json:"addresses,omitempty"`
	// Username 指示是否启用或满足Username。
	Username string `json:"username,omitempty"`
	// Password 保存密码。
	Password string `json:"password,omitempty"`
	// AuthKey 保存认证键。
	AuthKey string `json:"authkey,omitempty"`
	// Timeout 保存超时时间。
	Timeout int `json:"timeout,omitempty"`
	// WriteTimeout 保存Write超时时间。
	WriteTimeout int `json:"write_timeout,omitempty"`
	// ReadTimeout 保存Read超时时间。
	ReadTimeout int `json:"read_timeout,omitempty"`
	// KeepAlivePeriod 保存KeepAlivePeriod。
	KeepAlivePeriod int `json:"keep_alive_timeout,omitempty"`
	// UseJSONNumber 指示是否启用或满足UseJSONNumber。
	UseJSONNumber bool `json:"use_json_number,omitempty"`
	// NumRetries 保存NumRetries。
	NumRetries int `json:"num_retries,omitempty"`
	// InitialCap 保存InitialCap。
	InitialCap int `json:"initial_cap,omitempty"`
	// MaxOpen 保存MaxOpen。
	MaxOpen int `json:"max_open,omitempty"`
	// DiscoverHosts 保存DiscoverHosts。
	DiscoverHosts bool `json:"discover_hosts,omitempty"`
	// HostDecayDuration 保存HostDecayDuration。
	HostDecayDuration int `json:"host_decay_duration,omitempty"`
}

// Open 初始化 RethinkDB Session
func (a *adapter) Open(jsonconfig json.RawMessage) error {
	if a.conn != nil {
		return errors.New("adapter rethinkdb is already connected")
	}

	if len(jsonconfig) < 2 {
		return errors.New("adapter rethinkdb missing config")
	}

	var err error
	var config configType
	if err = json.Unmarshal(jsonconfig, &config); err != nil {
		return errors.New("adapter rethinkdb failed to parse config: " + err.Error())
	}

	var opts rdb.ConnectOpts

	if config.Addresses == nil {
		opts.Address = defaultHost
	} else if host, ok := config.Addresses.(string); ok {
		opts.Address = host
	} else if ihosts, ok := config.Addresses.([]any); ok && len(ihosts) > 0 {
		hosts := make([]string, len(ihosts))
		for i, ih := range ihosts {
			h, ok := ih.(string)
			if !ok || h == "" {
				return errors.New("adapter rethinkdb invalid config.Addresses value")
			}
			hosts[i] = h
		}
		opts.Addresses = hosts
	} else {
		return errors.New("adapter rethinkdb failed to parse config.Addresses")
	}

	if config.Database == "" {
		a.dbName = defaultDatabase
	} else {
		a.dbName = config.Database
	}

	if a.maxResults <= 0 {
		a.maxResults = defaultMaxResults
	}

	if a.maxMessageResults <= 0 {
		a.maxMessageResults = defaultMaxMessageResults
	}

	opts.Database = a.dbName
	opts.Username = config.Username
	opts.Password = config.Password
	opts.AuthKey = config.AuthKey
	opts.Timeout = time.Duration(config.Timeout) * time.Second
	opts.WriteTimeout = time.Duration(config.WriteTimeout) * time.Second
	opts.ReadTimeout = time.Duration(config.ReadTimeout) * time.Second
	opts.KeepAlivePeriod = time.Duration(config.KeepAlivePeriod) * time.Second
	opts.UseJSONNumber = config.UseJSONNumber
	opts.NumRetries = config.NumRetries
	opts.InitialCap = config.InitialCap
	opts.MaxOpen = config.MaxOpen
	opts.DiscoverHosts = config.DiscoverHosts
	opts.HostDecayDuration = time.Duration(config.HostDecayDuration) * time.Second

	a.conn, err = rdb.Connect(opts)
	if err != nil {
		return err
	}

	rdb.SetTags("json")
	a.version = -1

	return nil
}

// Close 关闭底层数据库连接
func (a *adapter) Close() error {
	var err error
	if a.conn != nil {
		// Close 会等待所有未完成的请求完成
		err = a.conn.Close()
		a.conn = nil
		a.version = -1
	}
	return err
}

// IsOpen 如果与数据库的连接已建立则返回 true。
// 不检查连接是否实际存活。
func (a *adapter) IsOpen() bool {
	return a.conn != nil
}

// GetDbVersion 返回当前数据库版本。
func (a *adapter) GetDbVersion() (int, error) {
	if a.version > 0 {
		return a.version, nil
	}

	cursor, err := rdb.DB(a.dbName).Table("kvmeta").Get("version").Field("value").Run(a.conn)
	if err != nil {
		if isMissingDb(err) {
			err = errors.New("Database not initialized")
		}
		return -1, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return -1, errors.New("Database not initialized")
	}

	var vers int
	if err = cursor.One(&vers); err != nil {
		return -1, err
	}

	a.version = vers

	return vers, nil
}

// updateDbVersion 更新数据库版本。
func (a *adapter) updateDbVersion(v int) error {
	a.version = -1
	if _, err := rdb.DB(a.dbName).Table("kvmeta").Get("version").
		Update(map[string]any{"value": v}).RunWrite(a.conn); err != nil {
		return err
	}
	return nil
}

// CheckDbVersion 检查实际数据库版本是否与此适配器的预期版本匹配。
func (a *adapter) CheckDbVersion() error {
	// 绕过 GetDbVersion 的启动期缓存，使用真实查询检测连接是否仍然可用。
	cursor, err := rdb.DB(a.dbName).Table("kvmeta").Get("version").Field("value").Run(a.conn)
	if err != nil {
		return err
	}
	defer cursor.Close()
	if cursor.IsNil() {
		return errors.New("Database not initialized")
	}
	var version int
	if err = cursor.One(&version); err != nil {
		return err
	}

	if version != adpVersion {
		return errors.New("Invalid database version " + strconv.Itoa(version) +
			". Expected " + strconv.Itoa(adpVersion))
	}

	return nil
}

// Version 返回适配器版本。
func (adapter) Version() int {
	return adpVersion
}

// Stats 返回数据库连接统计对象。
func (a *adapter) Stats() any {
	if a.conn == nil {
		return nil
	}

	cursor, err := rdb.DB("rethinkdb").Table("stats").Get([]string{"cluster"}).Field("query_engine").Run(a.conn)
	if err != nil {
		return nil
	}
	defer cursor.Close()

	var stats []any
	if err = cursor.All(&stats); err != nil || len(stats) < 1 {
		return nil
	}

	return stats[0]
}

// GetName 返回适配器向存储注册时使用的名称。
func (a *adapter) GetName() string {
	return adapterName
}

// SetMaxResults 配置单次数据库调用可返回的最大结果数。
func (a *adapter) SetMaxResults(val int) error {
	if val <= 0 {
		a.maxResults = defaultMaxResults
	} else {
		a.maxResults = val
	}

	return nil
}

// GetTestDB 返回当前打开的数据库连接。
func (a *adapter) GetTestDB() any {
	return a.conn
}

// 检查错误是否由于无结果。
// 涉及的情况是在非对象值上调用 Field('name')。
func isNoResults(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "perform get_field on a non-object non-sequence")
}

// 检查给定错误是否为 '数据库未找到'。
func isMissingDb(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	// "数据库 `db_name` 不存在"
	return strings.Contains(msg, "Database `") && strings.Contains(msg, "` does not exist")
}

// GetTestAdapter 返回适配器对象。对运行测试有用。
func GetTestAdapter() *adapter {
	return &adapter{}
}

// init 注册当前包提供的实现并初始化包级状态。
func init() {
	store.RegisterAdapter(&adapter{})
}
