//go:build postgres
// +build postgres

// Package postgres 是 PostgreSQL 的数据库适配器。
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	pgx "github.com/jackc/pgx/v5"
	"net/url"
	"reflect"
	"strconv"
	"strings"
	"time"

	"chat/server/store"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jmoiron/sqlx"
)

// adapter 保存 PostgreSQL 连接数据。
type adapter struct {
	// db 保存数据库。
	db *pgxpool.Pool
	// poolConfig 保存池配置。
	poolConfig *pgxpool.Config
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
	adpVersion = 123
	// adapterName 指定adapter名称。
	adapterName = "postgres"

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
	// 数据库连接设置：
	// 使用字段
	User string `json:"user,omitempty"`
	// Passwd 保存Passwd。
	Passwd string `json:"passwd,omitempty"`
	// Host 保存Host。
	Host string `json:"host,omitempty"`
	// Port 保存Port。
	Port string `json:"port,omitempty"`
	// DBName 保存数据库名称。
	DBName string `json:"dbname,omitempty"`
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

	// SSL 模式决定如何处理 SSL 连接。
	// 支持的值：
	//   - "disable"：无 SSL 连接（默认）
	//   - "require"：要求 SSL 连接但不验证服务器证书
	//   - "verify-ca"：要求 SSL 并验证服务器证书由受信任的 CA 签发
	//   - "verify-full"：要求 SSL 并验证服务器证书与服务器主机名匹配
	//   - "prefer"：优先尝试 SSL，失败则回退到非 SSL
	//   - "allow"：优先尝试非 SSL，失败则回退到 SSL
	SSLMode string `json:"ssl_mode,omitempty"`

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

// Open 初始化数据库 Session
func (a *adapter) Open(jsonconfig json.RawMessage) error {
	if a.db != nil {
		return errors.New("postgres adapter is already connected")
	}

	if len(jsonconfig) < 2 {
		return errors.New("postgres adapter missing config")
	}

	var err error
	var config configType
	ctx := context.Background()
	if err = json.Unmarshal(jsonconfig, &config); err != nil {
		return errors.New("postgres adapter failed to parse config: " + err.Error())
	}

	if config.DSN != "" {
		a.dsn = config.DSN
		if uri, err := url.Parse(a.dsn); err == nil {
			a.dbName = strings.TrimPrefix(uri.Path, "/")
		} else {
			return err
		}
	} else {
		if a.dsn, err = setConnStr(config); err != nil {
			return err
		}
		a.dbName = config.DBName
	}

	if a.maxResults <= 0 {
		a.maxResults = defaultMaxResults
	}

	if a.maxMessageResults <= 0 {
		a.maxMessageResults = defaultMaxMessageResults
	}

	if a.poolConfig, err = pgxpool.ParseConfig(a.dsn); err != nil {
		return errors.New("postgres adapter failed to parse DSN: " + err.Error())
	}

	// NewWithConfig 仅创建连接池对象。通过 Ping 验证连接可用性。
	a.db, err = pgxpool.NewWithConfig(ctx, a.poolConfig)
	if err != nil {
		return err
	}

	err = a.db.Ping(ctx)
	if isMissingDb(err) {
		// Missing DB is OK if we are initializing the 数据库.
		// 由于 im 数据库不存在，连接时不指定数据库名。
		a.db.Close()
		a.poolConfig.ConnConfig.Database = ""
		a.db, err = pgxpool.NewWithConfig(ctx, a.poolConfig)
		if err != nil {
			return err
		}
		err = a.db.Ping(ctx)
	}
	if err != nil {
		return err
	}

	if config.MaxOpenConns > 0 {
		a.poolConfig.MaxConns = int32(config.MaxOpenConns)
	}
	if config.MaxIdleConns > 0 {
		a.poolConfig.MinConns = int32(config.MaxIdleConns)
	}
	if config.ConnMaxLifetime > 0 {
		a.poolConfig.MaxConnLifetime = time.Duration(config.ConnMaxLifetime) * time.Second
	}
	if config.SqlTimeout > 0 {
		a.sqlTimeout = time.Duration(config.SqlTimeout) * time.Second
		// 我们为事务分配 txTimeoutMultiplier 倍的 sqlTimeout。
		a.txTimeout = time.Duration(float64(config.SqlTimeout)*txTimeoutMultiplier) * time.Second
	}

	return nil
}

// Close closes the underlying 数据库 connection
func (a *adapter) Close() error {
	if a.db != nil {
		a.db.Close()
		a.db = nil
		a.version = -1
	}
	return nil
}

// IsOpen returns true if connection to 数据库 has been established. It does not 检查是否
// 连接是否实际存活。
func (a *adapter) IsOpen() bool {
	return a.db != nil
}

// GetDbVersion returns current 数据库 version.
func (a *adapter) GetDbVersion() (int, error) {
	if a.version > 0 {
		return a.version, nil
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var vers string
	err := a.db.QueryRow(ctx, "SELECT value FROM kvmeta WHERE key='version'").Scan(&vers)
	if err != nil {
		if isMissingDb(err) || isMissingTable(err) || err == pgx.ErrNoRows {
			err = errors.New("Database not initialized")
		}
		return -1, err
	}

	a.version, _ = strconv.Atoi(vers)

	return a.version, nil
}

// updateDbVersion 更新数据库版本。
func (a *adapter) updateDbVersion(v int) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	a.version = -1
	if _, err := a.db.Exec(ctx, `UPDATE kvmeta SET "value"=$1 WHERE "key"='version'`, strconv.Itoa(v)); err != nil {
		return err
	}
	return nil
}

// CheckDbVersion 检查实际数据库版本是否与适配器预期版本匹配。
func (a *adapter) CheckDbVersion() error {
	// 绕过 GetDbVersion 的启动期缓存，确保 Readiness 每次都真实访问数据库。
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var rawVersion string
	if err := a.db.QueryRow(
		ctx,
		"SELECT value FROM kvmeta WHERE key='version'",
	).Scan(&rawVersion); err != nil {
		return err
	}
	version, err := strconv.Atoi(rawVersion)
	if err != nil {
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
	return a.db.Stat()
}

// GetName returns string that adapter uses to register itself with 存储.
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

	msg := err.Error()
	return strings.Contains(msg, "SQLSTATE 23505")
}

// isMissingTable 判断是否满足MissingTable条件。
func isMissingTable(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	return strings.Contains(msg, "SQLSTATE 42P01")
}

// isMissingDb 判断是否满足Missing数据库条件。
func isMissingDb(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	return strings.Contains(msg, "SQLSTATE 3D000")
}

// setConnStr 将配置结构转换为 DSN 连接字符串。
func setConnStr(c configType) (string, error) {
	// 默认禁用 SSL 模式。
	sslMode := "disable"
	if c.SSLMode != "" {
		sslMode = c.SSLMode
	}

	if c.User == "" || c.Passwd == "" || c.Host == "" || c.Port == "" || c.DBName == "" {
		return "", errors.New("adapter postgres invalid config value")
	}
	connStr := fmt.Sprintf("postgres://%s:%s@%s:%s/%s?sslmode=%s&connect_timeout=%d",
		c.User,
		c.Passwd,
		c.Host,
		c.Port,
		c.DBName,
		sslMode,
		c.SqlTimeout)

	return connStr, nil
}

// expandQuery 将查询中的占位符替换为实际值并返回
// 展开后的查询和要在查询中使用的参数。
func expandQuery(query string, args ...any) (string, []any) {
	var expandedArgs []any
	var expandedQuery string

	if len(args) != strings.Count(query, "?") {
		args = flattenSlice(args)
	}
	expandedQuery, expandedArgs, _ = sqlx.In(query, args...)
	return sqlx.Rebind(sqlx.DOLLAR, expandedQuery), expandedArgs
}

// flatMap 将混合值/切片的切片转换为扁平切片。
func flattenSlice(slice []any) []any {
	var result []any
	for _, v := range slice {
		switch reflect.TypeOf(v).Kind() {
		case reflect.Slice:
			s := reflect.ValueOf(v)
			for i := 0; i < s.Len(); i++ {
				result = append(result, s.Index(i).Interface())
			}
		default:
			result = append(result, v)
		}
	}
	return result
}

// 将查询从 ? 重新绑定到目标 $，使用自定义初始值。
func rebindWithStart(query string, startAt int) string {
	// 在需要分配之前为 10 个参数预留足够空间
	rqb := make([]byte, 0, len(query)+10)

	var i, j = 0, startAt

	for i = strings.Index(query, "?"); i != -1; i = strings.Index(query, "?") {
		rqb = append(rqb, query[:i]...)
		rqb = append(rqb, '$')

		rqb = strconv.AppendInt(rqb, int64(j), 10)
		j++

		query = query[i+1:]
	}

	return string(append(rqb, query...))
}

// GetTestAdapter 返回适配器对象。用于运行测试。
func GetTestAdapter() *adapter {
	return &adapter{}
}

// init 注册当前包提供的实现并初始化包级状态。
func init() {
	store.RegisterAdapter(&adapter{})
}
