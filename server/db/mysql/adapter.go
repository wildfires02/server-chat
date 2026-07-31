//go:build mysql || (!postgres && !mongodb && !rethinkdb)
// +build mysql !postgres,!mongodb,!rethinkdb

// Package mysql 是 MySQL 的数据库适配器。
package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"chat/server/store"

	ms "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

// adapter 保存 MySQL 连接数据。
type adapter struct {
	// db 保存数据库。
	db *sqlx.DB
	// dsn 保存dsn。
	dsn string
	// dbName 保存数据库名称。
	dbName string
	// 最大返回记录数
	maxResults int
	// Maximum number of 消息 records to return
	maxMessageResults int
	// version 保存版本。
	version int

	// 单次查询超时。
	sqlTimeout time.Duration
	// 数据库事务超时。
	txTimeout time.Duration
}

const (
	// adpVersion 指定adp版本。
	adpVersion = 122
	// adapterName 指定adapter名称。
	adapterName = "mysql"

	// defaultDSN 指定默认DSN。
	defaultDSN = "root:@tcp(localhost:3306)/im?parseTime=true"
	// defaultDatabase 指定默认Database。
	defaultDatabase = "im"

	// defaultMaxResults 指定默认MaxResults。
	defaultMaxResults = 1024
	// 此值受 Session 发送队列上限 (128) 限制。
	defaultMaxMessageResults = 100

	// 如果指定了数据库请求超时，
	// 事务将分配 txTimeoutMultiplier 倍的时间。
	txTimeoutMultiplier = 1.5
)

// configType 保存配置Type的数据和运行状态。
type configType struct {
	// 数据库连接设置。
	// 参见 https://pkg.go.dev/github.com/go-sql-driver/mysql#Config
	// 了解完整字段列表。
	ms.Config
	// 已弃用。
	DSN string `json:"dsn,omitempty"`

	// 连接池设置。
	//
	// 最大打开连接数。
	MaxOpenConns int `json:"max_open_conns,omitempty"`
	// 空闲连接池中最大连接数。
	MaxIdleConns int `json:"max_idle_conns,omitempty"`
	// 连接可复用的最长时间（秒）。
	ConnMaxLifetime int `json:"conn_max_lifetime,omitempty"`

	// 数据库请求超时（秒）。
	// 若为 0（或负数），则不设置超时。
	SqlTimeout int `json:"sql_timeout,omitempty"`
}

// getContext 查询并返回上下文。
func (a *adapter) getContext() (context.Context, context.CancelFunc) {
	if a.sqlTimeout > 0 {
		return context.WithTimeout(context.Background(), a.sqlTimeout)
	}
	return context.Background(), nil
}

// getContextForTx 查询并返回上下文ForTx。
func (a *adapter) getContextForTx() (context.Context, context.CancelFunc) {
	if a.txTimeout > 0 {
		return context.WithTimeout(context.Background(), a.txTimeout)
	}
	return context.Background(), nil
}

// Open initializes 数据库 Session
func (a *adapter) Open(jsonconfig json.RawMessage) error {
	if a.db != nil {
		return errors.New("mysql adapter is already connected")
	}

	if len(jsonconfig) < 2 {
		return errors.New("adapter mysql missing config")
	}

	var err error
	defaultCfg := ms.NewConfig()
	config := configType{Config: *defaultCfg}
	if err = json.Unmarshal(jsonconfig, &config); err != nil {
		return errors.New("mysql adapter failed to parse config: " + err.Error())
	}

	if dsn := config.FormatDSN(); dsn != defaultCfg.FormatDSN() {
		// 已指定 MySQL 配置，使用它。
		a.dbName = config.DBName
		a.dsn = dsn
		if config.DSN != "" {
			return errors.New("mysql config: conflicting config and DSN are provided")
		}
	} else {
		// 否则使用 DSN 配置数据库连接。
		// 注意：此方法已弃用。
		if config.DSN != "" {
			// 移除可选的 schema 前缀。
			a.dsn = strings.TrimPrefix(config.DSN, "mysql://")
		} else {
			a.dsn = defaultDSN
		}

		// 从 DSN 中解析数据库名称。
		if cfg, err := ms.ParseDSN(a.dsn); err == nil {
			a.dbName = cfg.DBName
		} else {
			return err
		}
	}

	if a.dbName == "" {
		a.dbName = defaultDatabase
	}

	if a.maxResults <= 0 {
		a.maxResults = defaultMaxResults
	}

	if a.maxMessageResults <= 0 {
		a.maxMessageResults = defaultMaxMessageResults
	}

	// 这仅初始化驱动但不打开网络连接。
	a.db, err = sqlx.Open("mysql", a.dsn)
	if err != nil {
		return err
	}

	// 实际打开网络连接。
	err = a.db.Ping()
	if isMissingDb(err) {
		// 此处忽略缺失数据库的错误。如果正在初始化数据库，
		// 缺少数据库是可以的。
		err = nil
	}
	if err == nil {
		if config.MaxOpenConns > 0 {
			a.db.SetMaxOpenConns(config.MaxOpenConns)
		}
		if config.MaxIdleConns > 0 {
			a.db.SetMaxIdleConns(config.MaxIdleConns)
		}
		if config.ConnMaxLifetime > 0 {
			a.db.SetConnMaxLifetime(time.Duration(config.ConnMaxLifetime) * time.Second)
		}
		if config.SqlTimeout > 0 {
			a.sqlTimeout = time.Duration(config.SqlTimeout) * time.Second
			// 我们为事务分配 txTimeoutMultiplier 倍的 sqlTimeout。
			a.txTimeout = time.Duration(float64(config.SqlTimeout)*txTimeoutMultiplier) * time.Second
		}
	}
	return err
}

// Close 关闭底层数据库连接
func (a *adapter) Close() error {
	var err error
	if a.db != nil {
		err = a.db.Close()
		a.db = nil
		a.version = -1
	}
	return err
}

// IsOpen 如果与数据库的连接已建立则返回 true。它不检查
// 连接是否实际存活。
func (a *adapter) IsOpen() bool {
	return a.db != nil
}

// GetDbVersion 返回当前数据库版本。
func (a *adapter) GetDbVersion() (int, error) {
	if a.version > 0 {
		return a.version, nil
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var vers int
	err := a.db.GetContext(ctx, &vers, "SELECT `value` FROM kvmeta WHERE `key`='version'")
	if err != nil {
		if isMissingDb(err) || isMissingTable(err) || err == sql.ErrNoRows {
			err = errors.New("Database not initialized")
		}
		return -1, err
	}

	a.version = vers

	return vers, nil
}

// updateDbVersion 更新数据库版本。
func (a *adapter) updateDbVersion(v int) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	a.version = -1
	if _, err := a.db.ExecContext(ctx, "UPDATE kvmeta SET `value`=? WHERE `key`='version'", v); err != nil {
		return err
	}
	return nil
}

// CheckDbVersion 检查实际数据库版本是否与适配器预期版本匹配。
func (a *adapter) CheckDbVersion() error {
	// GetDbVersion 会缓存 schema 版本，不能用于运行期健康检查；
	// 这里必须执行真实查询，才能发现连接中断和数据库失联。
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var version int
	if err := a.db.GetContext(
		ctx,
		&version,
		"SELECT `value` FROM kvmeta WHERE `key`='version'",
	); err != nil {
		// 初始化器依赖这个稳定错误识别全新数据库。Readiness 仍会执行
		// 上面的真实查询，但缺库、缺表和缺版本行必须保留启动期语义。
		if isMissingDb(err) || isMissingTable(err) || err == sql.ErrNoRows {
			return errors.New("Database not initialized")
		}
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

// 数据库连接统计对象。
func (a *adapter) Stats() any {
	if a.db == nil {
		return nil
	}
	return a.db.Stats()
}

// GetName 返回适配器用于向存储注册自身的名称字符串。
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

// GetTestDB returns a currently open 数据库 connection.
func (a *adapter) GetTestDB() any {
	return a.db
}

// 辅助函数

// 检查是否 MySQL 错误 is a 错误 Code: 1062. Duplicate entry ... for key ...
func isDupe(err error) bool {
	if err == nil {
		return false
	}

	myerr, ok := err.(*ms.MySQLError)
	return ok && myerr.Number == 1062
}

// isMissingTable 判断是否满足MissingTable条件。
func isMissingTable(err error) bool {
	if err == nil {
		return false
	}

	myerr, ok := err.(*ms.MySQLError)
	return ok && myerr.Number == 1146
}

// isMissingDb 判断是否满足Missing数据库条件。
func isMissingDb(err error) bool {
	if err == nil {
		return false
	}

	myerr, ok := err.(*ms.MySQLError)
	return ok && myerr.Number == 1049
}

// GetTestAdapter 返回适配器对象。用于运行测试。
func GetTestAdapter() *adapter {
	return &adapter{}
}

// init 注册当前包提供的实现并初始化包级状态。
func init() {
	store.RegisterAdapter(&adapter{})
}
