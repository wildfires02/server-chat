//go:build postgres
// +build postgres

// Package postgres 是 PostgreSQL 的数据库适配器。
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"log"
	"net/url"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"

	"chat/server/auth"
	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
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
	adpVersion = 119
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
	version, err := a.GetDbVersion()
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

// CreateDb 初始化存储。
func (a *adapter) CreateDb(reset bool) error {
	var err error
	var tx pgx.Tx

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	// Can't use an existing connection because it's configured with a 数据库 name which may not exist.
	// 不干净关闭也没关系。
	if a.db != nil {
		a.db.Close()
	}

	// Create default 数据库 name
	a.poolConfig.ConnConfig.Database = "postgres"

	a.db, err = pgxpool.NewWithConfig(ctx, a.poolConfig)
	if err != nil {
		return err
	}
	if err = a.db.Ping(ctx); err != nil {
		return err
	}

	if reset {
		if _, err = a.db.Exec(ctx, fmt.Sprintf("DROP DATABASE IF EXISTS %s;", a.dbName)); err != nil {
			return err
		}
	}

	if _, err = a.db.Exec(ctx, fmt.Sprintf("CREATE DATABASE %s WITH ENCODING utf8;", a.dbName)); err != nil {
		return err
	}

	a.poolConfig.ConnConfig.Database = a.dbName
	a.db, err = pgxpool.NewWithConfig(ctx, a.poolConfig)
	if err != nil {
		return err
	}
	if err = a.db.Ping(ctx); err != nil {
		return err
	}

	if tx, err = a.db.Begin(ctx); err != nil {
		return err
	}

	defer func() {
		if err != nil {
			// PostgreSQL 原生支持事务型 DDL，建表过程若报错，Rollback 会彻底回滚已创建的表和索引
			tx.Rollback(ctx)
		}
	}()

	// Indexed 用户.
	if _, err := tx.Exec(ctx,
		`CREATE TABLE users(
			id        BIGINT NOT NULL,
			createdat TIMESTAMP(3) NOT NULL,
			updatedat TIMESTAMP(3) NOT NULL,
			state     SMALLINT NOT NULL DEFAULT 0,
			stateat   TIMESTAMP(3),
			access    JSON,
			lastseen  TIMESTAMP,
			useragent VARCHAR(255) DEFAULT '',
			public    JSON,
			trusted   JSON,
			tags      JSON,
			PRIMARY KEY(id)
		);
		CREATE INDEX users_state_stateat ON users(state, stateat);
		CREATE INDEX users_lastseen_updatedat ON users(lastseen, updatedat);
		COMMENT ON TABLE users IS '用户账号及公开资料';
		COMMENT ON COLUMN users.id IS '用户唯一ID';
		COMMENT ON COLUMN users.createdat IS '用户创建时间';
		COMMENT ON COLUMN users.updatedat IS '用户最近更新时间';
		COMMENT ON COLUMN users.state IS '用户生命周期状态';
		COMMENT ON COLUMN users.stateat IS '状态最近变更时间';
		COMMENT ON COLUMN users.access IS '用户默认访问权限';
		COMMENT ON COLUMN users.lastseen IS '用户最近在线时间';
		COMMENT ON COLUMN users.useragent IS '最近在线客户端User-Agent';
		COMMENT ON COLUMN users.public IS '对其他用户可见的公开资料';
		COMMENT ON COLUMN users.trusted IS '仅可信客户端可见的资料';
		COMMENT ON COLUMN users.tags IS '用于发现和搜索的反规范化标签';`); err != nil {
		return err
	}

	// Indexed 用户 tags.
	if _, err = tx.Exec(ctx,
		`CREATE TABLE usertags(
			id     SERIAL NOT NULL,
			userid BIGINT NOT NULL,
			tag    VARCHAR(96) NOT NULL,
			PRIMARY KEY(id),
			FOREIGN KEY(userid) REFERENCES users(id)
		);
		CREATE INDEX usertags_tag ON usertags(tag);
		CREATE UNIQUE INDEX usertags_userid_tag ON usertags(userid, tag);
		COMMENT ON TABLE usertags IS '用户与可搜索标签的索引关联';
		COMMENT ON COLUMN usertags.id IS '标签关联记录ID';
		COMMENT ON COLUMN usertags.userid IS '关联用户ID';
		COMMENT ON COLUMN usertags.tag IS '标准化用户搜索标签';`); err != nil {
		return err
	}

	// 已索引的设备。归一化到单独的表中。
	if _, err = tx.Exec(ctx,
		`CREATE TABLE devices(
			id       SERIAL NOT NULL,
			userid   BIGINT NOT NULL,
			hash     CHAR(16) NOT NULL,
			deviceid TEXT NOT NULL,
			platform VARCHAR(32),
			lastseen TIMESTAMP NOT NULL,
			lang     VARCHAR(8),
			PRIMARY KEY(id),
			FOREIGN KEY(userid) REFERENCES users(id)
		);
		CREATE UNIQUE INDEX devices_hash ON devices(hash);
		COMMENT ON TABLE devices IS '用户登录设备与推送目标';
		COMMENT ON COLUMN devices.id IS '设备记录ID';
		COMMENT ON COLUMN devices.userid IS '设备所属用户ID';
		COMMENT ON COLUMN devices.hash IS '设备标识的短哈希索引';
		COMMENT ON COLUMN devices.deviceid IS '推送服务使用的完整设备标识';
		COMMENT ON COLUMN devices.platform IS '客户端平台名称';
		COMMENT ON COLUMN devices.lastseen IS '设备最近活跃时间';
		COMMENT ON COLUMN devices.lang IS '设备首选语言';`); err != nil {
		return err
	}

	// 基础认证方案的认证记录。
	if _, err = tx.Exec(ctx,
		`CREATE TABLE auth(
			id      SERIAL NOT NULL,
			uname   VARCHAR(32) NOT NULL,
			userid  BIGINT NOT NULL,
			scheme  VARCHAR(16) NOT NULL,
			authlvl INT NOT NULL,
			secret  VARCHAR(255) NOT NULL,
			expires TIMESTAMP,
			PRIMARY KEY(id),
			FOREIGN KEY(userid) REFERENCES users(id)
		);
		CREATE UNIQUE INDEX auth_userid_scheme ON auth(userid, scheme);
		CREATE UNIQUE INDEX auth_uname ON auth(uname);
		COMMENT ON TABLE auth IS '用户认证方案与登录凭据';
		COMMENT ON COLUMN auth.id IS '认证记录ID';
		COMMENT ON COLUMN auth.uname IS '认证方案内唯一登录名';
		COMMENT ON COLUMN auth.userid IS '认证记录所属用户ID';
		COMMENT ON COLUMN auth.scheme IS '认证方案名称';
		COMMENT ON COLUMN auth.authlvl IS '认证等级';
		COMMENT ON COLUMN auth.secret IS '认证方案保存的凭据摘要或密文';
		COMMENT ON COLUMN auth.expires IS '认证记录过期时间';`); err != nil {
		return err
	}

	// Topic 管理
	if _, err = tx.Exec(ctx,
		`CREATE TABLE topics(
			id        SERIAL NOT NULL,
			createdat TIMESTAMP(3) NOT NULL,
			updatedat TIMESTAMP(3) NOT NULL,
			state     SMALLINT NOT NULL DEFAULT 0,
			stateat   TIMESTAMP(3),
			touchedat TIMESTAMP(3),
			name      VARCHAR(25) NOT NULL,
			usebt     BOOLEAN DEFAULT FALSE,
			owner     BIGINT NOT NULL DEFAULT 0,
			access    JSON,
			seqid     INT NOT NULL DEFAULT 0,
			delid     INT DEFAULT 0,
			subcnt    INT DEFAULT 0,
			public    JSON,
			trusted   JSON,
			tags      JSON,
			aux				JSON,
			PRIMARY KEY(id)
		);
		CREATE UNIQUE INDEX topics_name ON topics(name);
		CREATE INDEX topics_owner ON topics(owner);
		CREATE INDEX topics_state_stateat ON topics(state, stateat);
		CREATE INDEX topics_name_state_seqid ON topics(name, state, seqid);
		COMMENT ON TABLE topics IS '会话、群组和频道的核心状态';
		COMMENT ON COLUMN topics.id IS 'Topic内部记录ID';
		COMMENT ON COLUMN topics.createdat IS 'Topic创建时间';
		COMMENT ON COLUMN topics.updatedat IS 'Topic元数据最近更新时间';
		COMMENT ON COLUMN topics.state IS 'Topic生命周期状态';
		COMMENT ON COLUMN topics.stateat IS '状态最近变更时间';
		COMMENT ON COLUMN topics.touchedat IS '最近一条消息通过Topic的时间';
		COMMENT ON COLUMN topics.name IS 'Topic全局唯一名称';
		COMMENT ON COLUMN topics.usebt IS '是否使用广播频道语义';
		COMMENT ON COLUMN topics.owner IS 'Topic所有者用户ID';
		COMMENT ON COLUMN topics.access IS '匿名和认证用户的默认访问权限';
		COMMENT ON COLUMN topics.seqid IS '最新服务端消息序列号';
		COMMENT ON COLUMN topics.delid IS '最新删除操作序列号';
		COMMENT ON COLUMN topics.subcnt IS '当前有效订阅者数量';
		COMMENT ON COLUMN topics.public IS '订阅者可见的公共资料';
		COMMENT ON COLUMN topics.trusted IS '可信客户端可见的资料';
		COMMENT ON COLUMN topics.tags IS '用于发现和搜索的反规范化标签';
		COMMENT ON COLUMN topics.aux IS '服务端扩展元数据';`); err != nil {
		return err
	}

	// 创建系统 Topic 'sys'。
	if err = createSystemTopic(tx); err != nil {
		return err
	}

	// 已索引的 Topic 标签。
	if _, err = tx.Exec(ctx,
		`CREATE TABLE topictags(
			id    SERIAL NOT NULL,
			topic VARCHAR(25) NOT NULL,
			tag   VARCHAR(96) NOT NULL,
			PRIMARY KEY(id),
			FOREIGN KEY(topic) REFERENCES topics(name)
		);
		CREATE INDEX topictags_tag ON topictags(tag);
		CREATE UNIQUE INDEX topictags_topic_tag ON topictags(topic, tag);
		COMMENT ON TABLE topictags IS 'Topic与可搜索标签的索引关联';
		COMMENT ON COLUMN topictags.id IS '标签关联记录ID';
		COMMENT ON COLUMN topictags.topic IS '关联Topic名称';
		COMMENT ON COLUMN topictags.tag IS '标准化Topic搜索标签';`); err != nil {
		return err
	}

	// 订阅
	if _, err = tx.Exec(ctx,
		`CREATE TABLE subscriptions(
			id        SERIAL NOT NULL,
			createdat TIMESTAMP(3) NOT NULL,
			updatedat TIMESTAMP(3) NOT NULL,
			deletedat TIMESTAMP(3),
			userid    BIGINT NOT NULL,
			topic     VARCHAR(25) NOT NULL,
			delid     INT DEFAULT 0,
			recvseqid INT DEFAULT 0,
			readseqid INT DEFAULT 0,
			modewant  VARCHAR(8),
			modegiven VARCHAR(8),
			private   JSON,
			PRIMARY KEY(id),
			FOREIGN KEY(userid) REFERENCES users(id)
		);
		CREATE UNIQUE INDEX subscriptions_topic_userid ON subscriptions(topic, userid);
		CREATE INDEX subscriptions_topic ON subscriptions(topic);
		CREATE INDEX subscriptions_deletedat ON subscriptions(deletedat);
		CREATE INDEX subscriptions_userid_topic_deletedat ON subscriptions(userid, topic, deletedat);
		COMMENT ON TABLE subscriptions IS '用户与Topic之间的订阅、权限及同步游标';
		COMMENT ON COLUMN subscriptions.id IS '订阅记录ID';
		COMMENT ON COLUMN subscriptions.createdat IS '订阅创建时间';
		COMMENT ON COLUMN subscriptions.updatedat IS '订阅最近更新时间';
		COMMENT ON COLUMN subscriptions.deletedat IS '订阅软删除时间';
		COMMENT ON COLUMN subscriptions.userid IS '订阅用户ID';
		COMMENT ON COLUMN subscriptions.topic IS '订阅Topic名称';
		COMMENT ON COLUMN subscriptions.delid IS '用户已同步的最新删除操作序列号';
		COMMENT ON COLUMN subscriptions.recvseqid IS '用户已送达的最新消息序列号';
		COMMENT ON COLUMN subscriptions.readseqid IS '用户已读的最新消息序列号';
		COMMENT ON COLUMN subscriptions.modewant IS '用户请求的访问模式';
		COMMENT ON COLUMN subscriptions.modegiven IS 'Topic授予的访问模式';
		COMMENT ON COLUMN subscriptions.private IS '仅该订阅用户可见的私有资料';`); err != nil {
		return err
	}

	// 消息
	if _, err = tx.Exec(ctx,
		`CREATE TABLE messages(
			id        SERIAL NOT NULL,
			createdat TIMESTAMP(3) NOT NULL,
			updatedat TIMESTAMP(3) NOT NULL,
			deletedat TIMESTAMP(3),
			delid     INT DEFAULT 0,
			seqid     INT NOT NULL,
			topic     VARCHAR(25) NOT NULL,
			"from"    BIGINT NOT NULL,
			clientid  VARCHAR(64),
			clientkey VARCHAR(43),
			head      JSON,
			content   JSON,
			searchtext TEXT,
			PRIMARY KEY(id),
			FOREIGN KEY(topic) REFERENCES topics(name)
		);
		CREATE UNIQUE INDEX messages_topic_seqid ON messages(topic, seqid);
		CREATE UNIQUE INDEX messages_topic_clientkey ON messages(topic, clientkey);
		CREATE INDEX messages_topic_updatedat_seqid ON messages(topic,updatedat,seqid);
		COMMENT ON TABLE messages IS '已进入Topic序列的持久化消息';
		COMMENT ON COLUMN messages.id IS '消息内部记录ID';
		COMMENT ON COLUMN messages.createdat IS '消息创建时间';
		COMMENT ON COLUMN messages.updatedat IS '消息最近编辑时间';
		COMMENT ON COLUMN messages.deletedat IS '消息删除时间';
		COMMENT ON COLUMN messages.delid IS '硬删除操作序列号；0表示未硬删除';
		COMMENT ON COLUMN messages.seqid IS '消息在Topic内的服务端序列号';
		COMMENT ON COLUMN messages.topic IS '消息所属Topic名称';
		COMMENT ON COLUMN messages."from" IS '发送者用户ID';
		COMMENT ON COLUMN messages.clientid IS '客户端生成的发布幂等键';
		COMMENT ON COLUMN messages.clientkey IS '发送者与clientid生成的唯一索引键';
		COMMENT ON COLUMN messages.head IS '客户端扩展头和服务端消息元数据';
		COMMENT ON COLUMN messages.content IS '纯文本或Drafty消息正文';
		COMMENT ON COLUMN messages.searchtext IS '从消息正文提取并经NFKC归一化的全文搜索文本';`); err != nil {
		return err
	}

	if _, err = tx.Exec(ctx,
		`CREATE TABLE scheduledmessages(
			id        BIGINT NOT NULL,
			createdat TIMESTAMP(3) NOT NULL,
			updatedat TIMESTAMP(3) NOT NULL,
			publishat TIMESTAMP(3) NOT NULL,
			topic     VARCHAR(25) NOT NULL,
			"from"    BIGINT NOT NULL,
			clientid  VARCHAR(64) NOT NULL,
			noecho    BOOLEAN NOT NULL DEFAULT FALSE,
			head      JSON,
			content   JSON,
			attachmenturls JSON,
			attachments JSON,
			PRIMARY KEY(id),
			FOREIGN KEY(topic) REFERENCES topics(name) ON DELETE CASCADE,
			FOREIGN KEY("from") REFERENCES users(id) ON DELETE CASCADE
		);
		CREATE UNIQUE INDEX scheduled_topic_from_clientid ON scheduledmessages(topic,"from",clientid);
		CREATE INDEX scheduled_publishat ON scheduledmessages(publishat);
		COMMENT ON TABLE scheduledmessages IS '尚未进入Topic序列的持久化定时消息队列';
		COMMENT ON COLUMN scheduledmessages.id IS '服务端生成的定时消息唯一ID';
		COMMENT ON COLUMN scheduledmessages.createdat IS '定时消息创建时间';
		COMMENT ON COLUMN scheduledmessages.updatedat IS '定时消息最近更新时间';
		COMMENT ON COLUMN scheduledmessages.publishat IS '计划投递时间';
		COMMENT ON COLUMN scheduledmessages.topic IS '目标Topic名称';
		COMMENT ON COLUMN scheduledmessages."from" IS '发送者用户ID';
		COMMENT ON COLUMN scheduledmessages.clientid IS '发送者范围内的客户端幂等键';
		COMMENT ON COLUMN scheduledmessages.noecho IS '投递时是否跳过发起会话';
		COMMENT ON COLUMN scheduledmessages.head IS '已校验的消息头快照';
		COMMENT ON COLUMN scheduledmessages.content IS '已校验的消息正文快照';
		COMMENT ON COLUMN scheduledmessages.attachmenturls IS '投递普通消息时使用的附件URL列表';
		COMMENT ON COLUMN scheduledmessages.attachments IS '垃圾回收保护使用的文件ID列表';`); err != nil {
		return err
	}

	// 删除日志
	if _, err = tx.Exec(ctx,
		`CREATE TABLE dellog(
			id         SERIAL NOT NULL,
			topic      VARCHAR(25) NOT NULL,
			deletedfor BIGINT NOT NULL DEFAULT 0,
			delid      INT NOT NULL,
			low        INT NOT NULL,
			hi         INT NOT NULL,
			PRIMARY KEY(id),
			FOREIGN KEY(topic) REFERENCES topics(name)
		);
		CREATE INDEX dellog_topic_delid_deletedfor ON dellog(topic,delid,deletedfor);
		CREATE INDEX dellog_topic_deletedfor_low_hi ON dellog(topic,deletedfor,low,hi);
		CREATE INDEX dellog_deletedfor ON dellog(deletedfor);
		COMMENT ON TABLE dellog IS '消息软删除与硬删除的同步日志';
		COMMENT ON COLUMN dellog.id IS '删除日志记录ID';
		COMMENT ON COLUMN dellog.topic IS '删除操作所属Topic';
		COMMENT ON COLUMN dellog.deletedfor IS '软删除目标用户ID；0表示所有用户';
		COMMENT ON COLUMN dellog.delid IS '删除操作序列号';
		COMMENT ON COLUMN dellog.low IS '被删除消息范围下界（包含）';
		COMMENT ON COLUMN dellog.hi IS '被删除消息范围上界（不包含）';`); err != nil {
		return err
	}

	// 用户 credentials
	if _, err = tx.Exec(ctx,
		`CREATE TABLE credentials(
			id        SERIAL NOT NULL,
			createdat TIMESTAMP(3) NOT NULL,
			updatedat TIMESTAMP(3) NOT NULL,
			deletedat TIMESTAMP(3),
			method    VARCHAR(16) NOT NULL,
			value     VARCHAR(128) NOT NULL,
			synthetic VARCHAR(192) NOT NULL,
			userid    BIGINT NOT NULL,
			resp      VARCHAR(255),
			done      BOOLEAN NOT NULL DEFAULT FALSE,
			retries   INT NOT NULL DEFAULT 0,
			PRIMARY KEY(id),
			FOREIGN KEY(userid) REFERENCES users(id)
		);
		CREATE UNIQUE INDEX credentials_uniqueness ON credentials(synthetic);
		COMMENT ON TABLE credentials IS '用户邮箱、手机号等可验证身份凭据';
		COMMENT ON COLUMN credentials.id IS '凭据记录ID';
		COMMENT ON COLUMN credentials.createdat IS '凭据创建时间';
		COMMENT ON COLUMN credentials.updatedat IS '凭据最近更新时间';
		COMMENT ON COLUMN credentials.deletedat IS '凭据软删除时间';
		COMMENT ON COLUMN credentials.method IS '凭据类型，如email或tel';
		COMMENT ON COLUMN credentials.value IS '规范化后的凭据值';
		COMMENT ON COLUMN credentials.synthetic IS '用于全局唯一约束的合成键';
		COMMENT ON COLUMN credentials.userid IS '凭据所属用户ID';
		COMMENT ON COLUMN credentials.resp IS '验证挑战的期望响应摘要';
		COMMENT ON COLUMN credentials.done IS '凭据是否已完成验证';
		COMMENT ON COLUMN credentials.retries IS '验证失败重试次数';`); err != nil {
		return err
	}

	// 上传文件的记录。
	// Don't add FOREIGN KEY on userid. It's not needed and it will break 用户 deletion.
	if _, err = tx.Exec(ctx,
		`CREATE TABLE fileuploads(
			id        BIGINT NOT NULL,
			createdat TIMESTAMP(3) NOT NULL,
			updatedat TIMESTAMP(3) NOT NULL,
			userid    BIGINT,
			status    INT NOT NULL,
			mimetype  VARCHAR(255) NOT NULL,
			size      BIGINT NOT NULL,
			etag      VARCHAR(128),
			location  VARCHAR(2048) NOT NULL,
			PRIMARY KEY(id)
		);
		CREATE INDEX fileuploads_status ON fileuploads(status);
		COMMENT ON TABLE fileuploads IS '媒体后端中的上传文件元数据';
		COMMENT ON COLUMN fileuploads.id IS '上传文件唯一ID';
		COMMENT ON COLUMN fileuploads.createdat IS '上传记录创建时间';
		COMMENT ON COLUMN fileuploads.updatedat IS '上传状态最近更新时间';
		COMMENT ON COLUMN fileuploads.userid IS '上传文件的用户ID';
		COMMENT ON COLUMN fileuploads.status IS '上传和垃圾回收状态';
		COMMENT ON COLUMN fileuploads.mimetype IS '文件MIME类型';
		COMMENT ON COLUMN fileuploads.size IS '文件字节大小';
		COMMENT ON COLUMN fileuploads.etag IS '对象存储内容校验标签';
		COMMENT ON COLUMN fileuploads.location IS '媒体后端中的文件位置';`); err != nil {
		return err
	}

	// Links between uploaded files and the Topic, 用户 or 消息 they are attached to.
	if _, err = tx.Exec(ctx,
		`CREATE TABLE filemsglinks(
			id        SERIAL NOT NULL,
			createdat TIMESTAMP(3) NOT NULL,
			fileid    BIGINT NOT NULL,
			msgid     INT,
			topic     VARCHAR(25),
			userid    BIGINT,
			PRIMARY KEY(id),
			FOREIGN KEY(fileid) REFERENCES fileuploads(id) ON DELETE CASCADE,
			FOREIGN KEY(msgid) REFERENCES messages(id) ON DELETE CASCADE,
			FOREIGN KEY(topic) REFERENCES topics(name) ON DELETE CASCADE,
			FOREIGN KEY(userid) REFERENCES users(id) ON DELETE CASCADE
		);
		COMMENT ON TABLE filemsglinks IS '文件与消息、Topic或用户资料的引用关联';
		COMMENT ON COLUMN filemsglinks.id IS '文件关联记录ID';
		COMMENT ON COLUMN filemsglinks.createdat IS '关联创建时间';
		COMMENT ON COLUMN filemsglinks.fileid IS '关联文件ID';
		COMMENT ON COLUMN filemsglinks.msgid IS '关联消息内部记录ID';
		COMMENT ON COLUMN filemsglinks.topic IS '关联Topic名称';
		COMMENT ON COLUMN filemsglinks.userid IS '关联用户ID';`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx,
		`CREATE TABLE scheduledfilelinks(
			id BIGSERIAL NOT NULL,
			scheduledid BIGINT NOT NULL,
			fileid BIGINT NOT NULL,
			PRIMARY KEY(id),
			FOREIGN KEY(scheduledid) REFERENCES scheduledmessages(id) ON DELETE CASCADE,
			FOREIGN KEY(fileid) REFERENCES fileuploads(id) ON DELETE CASCADE
		);
		CREATE UNIQUE INDEX scheduledfilelinks_pair ON scheduledfilelinks(scheduledid,fileid);
		COMMENT ON TABLE scheduledfilelinks IS '定时消息与待投递文件的垃圾回收保护关联';
		COMMENT ON COLUMN scheduledfilelinks.id IS '关联记录自增ID';
		COMMENT ON COLUMN scheduledfilelinks.scheduledid IS '定时消息ID';
		COMMENT ON COLUMN scheduledfilelinks.fileid IS '待投递文件ID';`); err != nil {
		return err
	}

	if _, err = tx.Exec(ctx,
		`CREATE TABLE kvmeta(
			"key"     VARCHAR(64) NOT NULL,
			createdat TIMESTAMP(3),
			"value"   TEXT,
			PRIMARY KEY("key")
		);
		CREATE INDEX kvmeta_createdat_key ON kvmeta(createdat, "key");
		COMMENT ON TABLE kvmeta IS '数据库版本及全局键值元数据';
		COMMENT ON COLUMN kvmeta."key" IS '元数据键';
		COMMENT ON COLUMN kvmeta.createdat IS '元数据创建时间';
		COMMENT ON COLUMN kvmeta."value" IS '元数据字符串值';`); err != nil {
		return err
	}
	if _, err = tx.Exec(ctx, `INSERT INTO kvmeta("key", "value") VALUES($1, $2)`, "version", strconv.Itoa(adpVersion)); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// UpgradeDb upgrades the 数据库, if necessary.
func (a *adapter) UpgradeDb() error {
	bumpVersion := func(a *adapter, x int) error {
		if err := a.updateDbVersion(x); err != nil {
			return err
		}
		_, err := a.GetDbVersion()
		return err
	}

	if _, err := a.GetDbVersion(); err != nil {
		return err
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	if a.version == 112 {
		// Perform 数据库 upgrade from version 112 to version 113.

		// 删除未验证账户的索引。
		if _, err := a.db.Exec(ctx, "CREATE INDEX users_lastseen_updatedat ON users(lastseen,updatedat)"); err != nil {
			return err
		}

		// 允许更长的 kvmeta 键。
		if _, err := a.db.Exec(ctx, `ALTER TABLE kvmeta ALTER COLUMN "key" TYPE VARCHAR(64)`); err != nil {
			return err
		}

		if _, err := a.db.Exec(ctx, `ALTER TABLE kvmeta ALTER COLUMN "key" SET NOT NULL`); err != nil {
			return err
		}

		// 为 kvmeta 添加时间戳。
		if _, err := a.db.Exec(ctx, `ALTER TABLE kvmeta ADD COLUMN createdat TIMESTAMP(3)`); err != nil {
			return err
		}

		// 在新字段和键上添加复合索引（可按键前缀搜索）。
		if _, err := a.db.Exec(ctx, `CREATE INDEX kvmeta_createdat_key ON kvmeta(createdat, "key")`); err != nil {
			return err
		}

		if err := bumpVersion(a, 113); err != nil {
			return err
		}
	}

	if a.version == 113 {
		// Perform 数据库 upgrade from version 113 to version 114.

		if _, err := a.db.Exec(ctx, "ALTER TABLE topics ADD COLUMN aux JSON"); err != nil {
			return err
		}

		if _, err := a.db.Exec(ctx, "ALTER TABLE fileuploads ADD COLUMN etag VARCHAR(128)"); err != nil {
			return err
		}

		if err := bumpVersion(a, 114); err != nil {
			return err
		}
	}

	if a.version == 114 {
		// Perform 数据库 upgrade from version 114 to version 115.

		// Find relevant 订阅 for given 用户 efficiently, and use the join key too.
		if _, err := a.db.Exec(ctx, "CREATE INDEX idx_subs_user_topic_del ON subscriptions(userid, topic, deletedat)"); err != nil {
			return err
		}

		// 优化连接；状态过滤；seqid 支持 SUM 操作。
		if _, err := a.db.Exec(ctx, "CREATE INDEX idx_topics_name_state_seqid ON topics(name, state, seqid)"); err != nil {
			return err
		}

		if err := bumpVersion(a, 115); err != nil {
			return err
		}
	}

	if a.version == 115 {
		// Perform 数据库 upgrade from version 115 to version 116.

		// 为 Topic 表添加订阅者计数字段。
		if _, err := a.db.Exec(ctx, "ALTER TABLE topics ADD subcnt INT DEFAULT 0"); err != nil {
			return err
		}

		if err := bumpVersion(a, 116); err != nil {
			return err
		}
	}

	if a.version == 116 {
		// 为客户端发布重试增加跨重启幂等约束。
		if _, err := a.db.Exec(ctx, "ALTER TABLE messages ADD COLUMN clientid VARCHAR(64); COMMENT ON COLUMN messages.clientid IS '客户端生成的发布幂等键'"); err != nil {
			return err
		}
		if _, err := a.db.Exec(ctx, "ALTER TABLE messages ADD COLUMN clientkey VARCHAR(43); COMMENT ON COLUMN messages.clientkey IS '发送者与clientid生成的唯一索引键'"); err != nil {
			return err
		}
		if _, err := a.db.Exec(ctx, "CREATE UNIQUE INDEX messages_topic_clientkey ON messages(topic, clientkey)"); err != nil {
			return err
		}
		if err := bumpVersion(a, 117); err != nil {
			return err
		}
	}

	if a.version == 117 {
		// 数据库 117→118：增加消息修改游标索引、定时队列及附件保护关联表。
		if _, err := a.db.Exec(ctx, "CREATE INDEX messages_topic_updatedat_seqid ON messages(topic,updatedat,seqid)"); err != nil {
			return err
		}
		if _, err := a.db.Exec(ctx, `CREATE TABLE scheduledmessages(
			id BIGINT NOT NULL,
			createdat TIMESTAMP(3) NOT NULL,
			updatedat TIMESTAMP(3) NOT NULL,
			publishat TIMESTAMP(3) NOT NULL,
			topic VARCHAR(25) NOT NULL,
			"from" BIGINT NOT NULL,
			clientid VARCHAR(64) NOT NULL,
			noecho BOOLEAN NOT NULL DEFAULT FALSE,
			head JSON,
			content JSON,
			attachmenturls JSON,
			attachments JSON,
			PRIMARY KEY(id),
			FOREIGN KEY(topic) REFERENCES topics(name) ON DELETE CASCADE,
			FOREIGN KEY("from") REFERENCES users(id) ON DELETE CASCADE
		);
		CREATE UNIQUE INDEX scheduled_topic_from_clientid ON scheduledmessages(topic,"from",clientid);
		CREATE INDEX scheduled_publishat ON scheduledmessages(publishat);
		COMMENT ON TABLE scheduledmessages IS '尚未进入Topic序列的持久化定时消息队列';
		COMMENT ON COLUMN scheduledmessages.id IS '服务端生成的定时消息唯一ID';
		COMMENT ON COLUMN scheduledmessages.createdat IS '定时消息创建时间';
		COMMENT ON COLUMN scheduledmessages.updatedat IS '定时消息最近更新时间';
		COMMENT ON COLUMN scheduledmessages.publishat IS '计划投递时间';
		COMMENT ON COLUMN scheduledmessages.topic IS '目标Topic名称';
		COMMENT ON COLUMN scheduledmessages."from" IS '发送者用户ID';
		COMMENT ON COLUMN scheduledmessages.clientid IS '发送者范围内的客户端幂等键';
		COMMENT ON COLUMN scheduledmessages.noecho IS '投递时是否跳过发起会话';
		COMMENT ON COLUMN scheduledmessages.head IS '已校验的消息头快照';
		COMMENT ON COLUMN scheduledmessages.content IS '已校验的消息正文快照';
		COMMENT ON COLUMN scheduledmessages.attachmenturls IS '投递普通消息时使用的附件URL列表';
		COMMENT ON COLUMN scheduledmessages.attachments IS '垃圾回收保护使用的文件ID列表';`); err != nil {
			return err
		}
		if _, err := a.db.Exec(ctx, `CREATE TABLE scheduledfilelinks(
			id BIGSERIAL NOT NULL,
			scheduledid BIGINT NOT NULL,
			fileid BIGINT NOT NULL,
			PRIMARY KEY(id),
			FOREIGN KEY(scheduledid) REFERENCES scheduledmessages(id) ON DELETE CASCADE,
			FOREIGN KEY(fileid) REFERENCES fileuploads(id) ON DELETE CASCADE
		);
		CREATE UNIQUE INDEX scheduledfilelinks_pair ON scheduledfilelinks(scheduledid,fileid);
		COMMENT ON TABLE scheduledfilelinks IS '定时消息与待投递文件的垃圾回收保护关联';
		COMMENT ON COLUMN scheduledfilelinks.id IS '关联记录自增ID';
		COMMENT ON COLUMN scheduledfilelinks.scheduledid IS '定时消息ID';
		COMMENT ON COLUMN scheduledfilelinks.fileid IS '待投递文件ID';`); err != nil {
			return err
		}
		if err := bumpVersion(a, 118); err != nil {
			return err
		}
	}

	if a.version == 118 {
		// 数据库 118→119：增加跨数据库一致的消息搜索文本。
		if _, err := a.db.Exec(ctx, `ALTER TABLE messages ADD COLUMN searchtext TEXT;
			COMMENT ON COLUMN messages.searchtext IS '从消息正文提取并经NFKC归一化的全文搜索文本';
			UPDATE messages SET searchtext=CASE
				WHEN json_typeof(content)='string' THEN content#>>'{}'
				WHEN json_typeof(content)='object' THEN COALESCE(content->>'txt','')
				ELSE '' END`); err != nil {
			return err
		}
		if err := bumpVersion(a, 119); err != nil {
			return err
		}
	}

	if a.version != adpVersion {
		return errors.New("Failed to perform database upgrade to version " + strconv.Itoa(adpVersion) +
			". DB is still at " + strconv.Itoa(a.version))
	}
	return nil
}

// createSystemTopic 创建并初始化SystemTopic。
func createSystemTopic(tx pgx.Tx) error {
	now := t.TimeNow()
	query := `INSERT INTO topics(createdat,updatedat,state,touchedat,name,access,public)
				VALUES($1,$2,$3,$4,'sys','{"Auth": "N","Anon": "N"}','{"fn": "System"}')`
	_, err := tx.Exec(context.Background(), query, now, now, t.StateOK, now)
	return err
}

// addTags 向当前集合添加Tags。
func addTags(ctx context.Context, tx pgx.Tx, table, keyName string, keyVal any, tags []string, ignoreDups bool) error {
	if len(tags) == 0 {
		return nil
	}

	//addTags(ctx, tx, "usertags", "userid", decoded_uid, add, reset == nil)
	sql := "INSERT INTO " + table + " (" + keyName + ",tag) VALUES($1,$2)"
	if ignoreDups {
		sql += " ON CONFLICT DO NOTHING"
	}
	for _, tag := range tags {
		if _, err := tx.Exec(ctx, sql, keyVal, tag); err != nil {
			if isDupe(err) {
				return t.ErrDuplicate
			}
			return err
		}
	}

	return nil
}

// removeTags 删除或清理Tags。
func removeTags(ctx context.Context, tx pgx.Tx, table, keyName string, keyVal any, tags []string) error {
	if len(tags) == 0 {
		return nil
	}

	sql, args := expandQuery("DELETE FROM "+table+" WHERE "+keyName+"=? AND tag IN (?)", keyVal, tags)
	_, err := tx.Exec(ctx, sql, args...)

	return err
}

// UserCreate creates a new 用户. Returns 错误 and true if 错误 is due to duplicate 用户 name,
// false for any other 错误
func (a *adapter) UserCreate(user *t.User) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	decoded_uid := store.DecodeUid(user.Uid())
	if _, err = tx.Exec(ctx,
		"INSERT INTO users(id,createdat,updatedat,state,access,public,trusted,tags) VALUES($1,$2,$3,$4,$5,$6,$7,$8);",
		decoded_uid,
		user.CreatedAt,
		user.UpdatedAt,
		user.State,
		user.Access,
		common.ToJSON(user.Public),
		common.ToJSON(user.Trusted),
		user.Tags); err != nil {
		return err
	}

	// Save 用户's tags to a separate table to make 用户 findable.
	if err = addTags(ctx, tx, "usertags", "userid", decoded_uid, user.Tags, false); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// Add 用户's authentication record
func (a *adapter) AuthAddRecord(uid t.Uid, scheme, unique string, authLvl auth.Level,
	secret []byte, expires time.Time) error {

	var exp *time.Time
	if !expires.IsZero() {
		exp = &expires
	}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	if _, err := a.db.Exec(ctx, "INSERT INTO auth(uname,userid,scheme,authLvl,secret,expires) VALUES($1,$2,$3,$4,$5,$6)",
		unique, store.DecodeUid(uid), scheme, authLvl, secret, exp); err != nil {
		if isDupe(err) {
			return t.ErrDuplicate
		}
		return err
	}
	return nil
}

// AuthDelScheme deletes an existing authentication scheme for the 用户.
func (a *adapter) AuthDelScheme(user t.Uid, scheme string) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.Exec(ctx, "DELETE FROM auth WHERE userid=$1 AND scheme=$2", store.DecodeUid(user), scheme)
	return err
}

// AuthDelAllRecords deletes all authentication records for the 用户.
func (a *adapter) AuthDelAllRecords(user t.Uid) (int, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	res, err := a.db.Exec(ctx, "DELETE FROM auth WHERE userid=$1", store.DecodeUid(user))
	if err != nil {
		return 0, err
	}
	count := res.RowsAffected()

	return int(count), nil
}

// Update 用户's authentication unique, secret, auth level.
func (a *adapter) AuthUpdRecord(uid t.Uid, scheme, unique string, authLvl auth.Level,
	secret []byte, expires time.Time) error {

	parapg := []string{"authLvl=?"}
	args := []any{authLvl}
	if unique != "" {
		parapg = append(parapg, "uname=?")
		args = append(args, unique)
	}
	if len(secret) > 0 {
		parapg = append(parapg, "secret=?")
		args = append(args, secret)
	}
	if !expires.IsZero() {
		parapg = append(parapg, "expires=?")
		args = append(args, expires)
	}
	args = append(args, store.DecodeUid(uid), scheme)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	sql, args := expandQuery("UPDATE auth SET "+strings.Join(parapg, ",")+" WHERE userid=? AND scheme=?", args...)
	resp, err := a.db.Exec(ctx, sql, args...)
	if isDupe(err) {
		return t.ErrDuplicate
	}

	if count := resp.RowsAffected(); count <= 0 {
		return t.ErrNotFound
	}

	return err
}

// Retrieve 用户's authentication record
func (a *adapter) AuthGetRecord(uid t.Uid, scheme string) (string, auth.Level, []byte, time.Time, error) {
	var expires time.Time

	var record struct {
		Uname   string
		Authlvl auth.Level
		Secret  []byte
		Expires *time.Time
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	if err := a.db.QueryRow(ctx, "SELECT uname,secret,expires,authlvl FROM auth WHERE userid=$1 AND scheme=$2",
		store.DecodeUid(uid), scheme).Scan(
		&record.Uname, &record.Secret, &record.Expires, &record.Authlvl); err != nil {
		if err == pgx.ErrNoRows {
			// Nothing found - use standard 错误.
			err = t.ErrNotFound
		}
		return "", 0, nil, expires, err
	}

	if record.Expires != nil {
		expires = *record.Expires
	}

	return record.Uname, record.Authlvl, record.Secret, expires, nil
}

// Retrieve 用户's authentication record
func (a *adapter) AuthGetUniqueRecord(unique string) (t.Uid, auth.Level, []byte, time.Time, error) {
	var expires time.Time

	var record struct {
		Userid  int64
		Authlvl auth.Level
		Secret  []byte
		Expires *time.Time
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	if err := a.db.QueryRow(ctx, "SELECT userid,secret,expires,authlvl FROM auth WHERE uname=$1", unique).Scan(
		&record.Userid, &record.Secret, &record.Expires, &record.Authlvl); err != nil {
		if err == pgx.ErrNoRows {
			// Nothing found - clear the 错误
			err = nil
		}
		return t.ZeroUid, 0, nil, expires, err
	}

	if record.Expires != nil {
		expires = *record.Expires
	}

	return store.EncodeUid(record.Userid), record.Authlvl, record.Secret, expires, nil
}

// UserGet fetches a single 用户 by 用户 id. If 用户 is not found it returns (nil, nil)
func (a *adapter) UserGet(uid t.Uid) (*t.User, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	var user t.User
	var id int64
	row, err := a.db.Query(ctx, "SELECT * FROM users WHERE id=$1 AND state!=$2", store.DecodeUid(uid), t.StateDeleted)
	if err != nil {
		return nil, err
	}
	defer row.Close()

	if !row.Next() {
		// Nothing found: 用户 does not exist or marked as soft-deleted
		return nil, nil
	}

	err = row.Scan(&id, &user.CreatedAt, &user.UpdatedAt, &user.State, &user.StateAt, &user.Access, &user.LastSeen, &user.UserAgent, &user.Public, &user.Trusted, &user.Tags)
	if err == nil {
		user.SetUid(uid)
		return &user, nil
	}

	return nil, err
}

// UserGetAll 完成用户GetAll所需的内部处理。
func (a *adapter) UserGetAll(ids ...t.Uid) ([]t.User, error) {
	uids := make([]any, len(ids))
	for i, id := range ids {
		uids[i] = store.DecodeUid(id)
	}

	users := []t.User{}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	rows, err := a.db.Query(ctx, "SELECT * FROM users WHERE id = ANY ($1) AND state!=$2", uids, t.StateDeleted)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var user t.User
		var id int64
		if err = rows.Scan(&id, &user.CreatedAt, &user.UpdatedAt, &user.State, &user.StateAt, &user.Access, &user.LastSeen, &user.UserAgent, &user.Public, &user.Trusted, &user.Tags); err != nil {
			users = nil
			break
		}
		user.SetUid(store.EncodeUid(id))

		users = append(users, user)
	}
	if err == nil {
		err = rows.Err()
	}

	return users, err
}

// UserDelete deletes specified 用户: wipes completely (hard-delete) or marks as deleted.
func (a *adapter) UserDelete(uid t.Uid, hard bool) error {
	decoded_uid := store.DecodeUid(uid)

	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	// 检查用户是否存在以及是否已被软删除
	var state t.ObjState
	if err = tx.QueryRow(ctx, "SELECT state FROM users WHERE id=$1", decoded_uid).Scan(&state); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return t.ErrNotFound
		}
		return err
	}
	if !hard && state == t.StateDeleted {
		return t.ErrNotFound
	}

	query := "SELECT name FROM topics WHERE owner=$1"
	args := []any{decoded_uid}
	// 硬删除时，删除所有 Topic，包括之前软删除的。
	if !hard {
		query += " AND state!=$2"
		args = append(args, t.StateDeleted)
	}
	// Get a list of Topic names owned by the 用户 (as 'grp' and 'chn').
	ownTopics, err := a.topicNamesForUser(query, false, args...)
	if err != nil {
		return err
	}

	now := t.TimeNow()
	if _, err = tx.Exec(ctx, `DELETE FROM scheduledmessages WHERE "from"=$1`, decoded_uid); err != nil {
		return err
	}
	for _, topic := range ownTopics {
		if _, err = tx.Exec(ctx, "DELETE FROM scheduledmessages WHERE topic=$1", topic); err != nil {
			return err
		}
	}

	if hard {
		// Delete 用户's devices
		// t.ErrNotFound = 用户 has no devices.
		if err = deviceDelete(ctx, tx, uid, ""); err != nil && err != t.ErrNotFound {
			return err
		}

		// Delete 用户's 订阅 in all Topic.
		if err = subsDelForUser(ctx, tx, decoded_uid, true); err != nil {
			return err
		}

		// Delete records of 消息 soft-deleted for the 用户.
		if _, err = tx.Exec(ctx, "DELETE FROM dellog WHERE deletedfor=$1", decoded_uid); err != nil {
			return err
		}

		// Can't delete 用户's 消息 in all Topic because we cannot notify Topic of such deletion.
		// Just leave the 消息 there marked as sent by "not found" 用户.

		// Delete Topic where the 用户 is the owner.

		if len(ownTopics) > 0 {
			// First delete all 消息 in those Topic.
			if _, err = tx.Exec(ctx, "DELETE FROM dellog USING topics WHERE topics.name=dellog.topic AND topics.owner=$1",
				decoded_uid); err != nil {
				return err
			}

			// Deletion of 消息 will cascade to filemsglinks and so to fileuploads.
			if _, err = tx.Exec(ctx, "DELETE FROM messages USING topics WHERE topics.name=messages.topic AND topics.owner=$1",
				decoded_uid); err != nil {
				return err
			}
			// Delete 订阅 for all 用户 where the 用户 is the owner of the Topic.
			sql, args, _ := sqlx.In("DELETE FROM subscriptions AS s WHERE topic IN (?)", ownTopics)
			if _, err = tx.Exec(ctx, sqlx.Rebind(sqlx.DOLLAR, sql), args...); err != nil {
				return err
			}

			// 删除 Topic 标签。
			if _, err = tx.Exec(ctx, "DELETE FROM topictags USING topics WHERE topics.name=topictags.topic AND topics.owner=$1",
				decoded_uid); err != nil {
				return err
			}

			// 最后删除 Topic。
			if _, err = tx.Exec(ctx, "DELETE FROM topics WHERE owner=$1", decoded_uid); err != nil {
				return err
			}
		}

		// Delete 用户's authentication records.
		if _, err = tx.Exec(ctx, "DELETE FROM auth WHERE userid=$1", decoded_uid); err != nil {
			return err
		}

		// 删除所有凭据。
		if err = credDel(ctx, tx, uid, "", ""); err != nil && err != t.ErrNotFound {
			return err
		}

		if _, err = tx.Exec(ctx, "DELETE FROM usertags WHERE userid=$1", decoded_uid); err != nil {
			return err
		}

		if _, err = tx.Exec(ctx, "DELETE FROM users WHERE id=$1", decoded_uid); err != nil {
			return err
		}
	} else {
		// Disable all 用户's 订阅. That includes p2p 订阅. No need to delete them.
		if err = subsDelForUser(ctx, tx, decoded_uid, false); err != nil {
			return err
		}

		if len(ownTopics) > 0 {
			// Disable all 订阅 to Topic where the 用户 is the owner.
			sql, args, _ := sqlx.In("UPDATE subscriptions SET updatedat=?,deletedat=? WHERE topic IN (?)", now, now, ownTopics)
			if _, err = tx.Exec(ctx, sqlx.Rebind(sqlx.DOLLAR, sql), args...); err != nil {
				return err
			}

			// Disable group Topic where the 用户 is the owner.
			if _, err = tx.Exec(ctx, "UPDATE topics SET updatedat=$1,touchedat=$1,state=$2,stateat=$1 WHERE owner=$3",
				now, t.StateDeleted, decoded_uid); err != nil {
				return err
			}
		}

		// Disable p2p Topic with the 用户 (p2p Topic's owner is 0).
		if _, err = tx.Exec(ctx, "UPDATE topics SET updatedat=$1,touchedat=$1,state=$2,stateat=$1 "+
			"FROM subscriptions WHERE topics.name=subscriptions.topic "+
			"AND topics.owner=0 AND subscriptions.userid=$3",
			now, t.StateDeleted, decoded_uid); err != nil {
			return err
		}

		// Disable the other 用户's 订阅 to a disabled p2p Topic.
		if _, err = tx.Exec(ctx, "UPDATE subscriptions AS s_one SET updatedat=$1,deletedat=$1 "+
			"FROM subscriptions AS s_two WHERE s_one.topic=s_two.topic "+
			"AND s_two.userid=$2 AND s_two.topic LIKE 'p2p%'",
			now, decoded_uid); err != nil {
			return err
		}

		// Disable 用户.
		if _, err = tx.Exec(ctx, "UPDATE users SET updatedat=$1,state=$2,stateat=$1 WHERE id=$3",
			now, t.StateDeleted, decoded_uid); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// topicStateForUser 由 UserUpdate 在更新包含状态变更时调用。
// 软删除的 Topic 保持软删除状态。
func (a *adapter) topicStateForUser(ctx context.Context, tx pgx.Tx, decoded_uid int64, now time.Time, update any) error {
	var err error

	state, ok := update.(t.ObjState)
	if !ok {
		return t.ErrMalformed
	}

	if now.IsZero() {
		now = t.TimeNow()
	}

	// Change state of all Topic where the 用户 is the owner.
	if _, err = tx.Exec(ctx, "UPDATE topics SET state=$1, stateat=$2 WHERE owner=$3 AND state!=$4",
		state, now, decoded_uid, t.StateDeleted); err != nil {
		return err
	}

	// Change state of p2p Topic with the 用户 (p2p Topic's owner is 0)
	if _, err = tx.Exec(ctx, "UPDATE topics SET state=$1, stateat=$2 "+
		"FROM subscriptions WHERE topics.name=subscriptions.topic AND "+
		"topics.owner=0 AND subscriptions.userid=$3 AND topics.state!=$4",
		state, now, decoded_uid, t.StateDeleted); err != nil {
		return err
	}

	// 订阅 don't need to be updated:
	// 订阅 of a disabled 用户 are not disabled and still can be manipulated.

	return nil
}

// UserUpdate updates 用户 object.
func (a *adapter) UserUpdate(uid t.Uid, update map[string]any) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	cols, args := common.UpdateByMap(update)
	decoded_uid := store.DecodeUid(uid)
	args = append(args, decoded_uid)
	sql, args := expandQuery("UPDATE users SET "+strings.Join(cols, ",")+" WHERE id=?", args...)
	_, err = tx.Exec(ctx, sql, args...)
	if err != nil {
		return err
	}

	if state, ok := update["State"]; ok {
		now, _ := update["StateAt"].(time.Time)
		err = a.topicStateForUser(ctx, tx, decoded_uid, now, state)
		if err != nil {
			return err
		}
	}

	// 标签也存储在单独的表中
	if tags := common.ExtractTags(update); tags != nil {
		// First delete all 用户 tags
		_, err = tx.Exec(ctx, "DELETE FROM usertags WHERE userid=$1", decoded_uid)
		if err != nil {
			return err
		}
		// 现在插入新标签
		err = addTags(ctx, tx, "usertags", "userid", decoded_uid, tags, false)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// tempFetchTags 完成tempFetchTags所需的内部处理。
func tempFetchTags(ctx context.Context, tx pgx.Tx, decoded_uid int64) ([]string, error) {
	var allTags []string
	rows, err := tx.Query(ctx, "SELECT tag FROM usertags WHERE userid=$1", decoded_uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var tag string
		rows.Scan(&tag)
		allTags = append(allTags, tag)
	}
	return allTags, nil
}

// UserUpdateTags adds or resets 用户's tags
func (a *adapter) UserUpdateTags(uid t.Uid, add, remove, reset []string) ([]string, error) {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	decoded_uid := store.DecodeUid(uid)

	if reset != nil {
		// 重置时先删除所有标签。
		_, err = tx.Exec(ctx, "DELETE FROM usertags WHERE userid=$1", decoded_uid)
		if err != nil {
			return nil, err
		}
		add = reset
		remove = nil
	}

	// 现在插入新标签。重置时忽略重复。
	err = addTags(ctx, tx, "usertags", "userid", decoded_uid, add, reset == nil)
	if err != nil {
		return nil, err
	}

	// 删除标签。
	err = removeTags(ctx, tx, "usertags", "userid", decoded_uid, remove)
	if err != nil {
		return nil, err
	}

	var allTags []string
	rows, err := tx.Query(ctx, "SELECT tag FROM usertags WHERE userid=$1", decoded_uid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var tag string
		rows.Scan(&tag)
		allTags = append(allTags, tag)
	}

	_, err = tx.Exec(ctx, "UPDATE users SET tags=$1 WHERE id=$2", t.StringSlice(allTags), decoded_uid)
	if err != nil {
		return nil, err
	}

	return allTags, tx.Commit(ctx)
}

// UserGetByCred returns 用户 ID for the given validated credential.
func (a *adapter) UserGetByCred(method, value string) (t.Uid, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var decoded_uid int64
	err := a.db.QueryRow(ctx, "SELECT userid FROM credentials WHERE synthetic=$1", method+":"+value).Scan(&decoded_uid)
	if err == nil {
		return store.EncodeUid(decoded_uid), nil
	}

	if err == pgx.ErrNoRows {
		// Clear the 错误 if 用户 does not exist
		return t.ZeroUid, nil
	}
	return t.ZeroUid, err
}

// UserUnreadCount returns the total number of unread 消息 in all Topic with
// the R 权限. If read fails, the counts are still returned with the original
// 用户 IDs but with the unread count undefined and non-nil 错误.
// UserUnreadCount does not count unread 消息 in Channel although it should.
func (a *adapter) UserUnreadCount(ids ...t.Uid) (map[t.Uid]int, error) {
	counts, uids := common.InitUnreadCountMap(ids)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	// 联表查询未读消息数：利用 CASE WHEN 动态支持 Channel (将 chn... 前缀映射为 topics 主表中的 grp...)
	query, uids := expandQuery("SELECT s.userid, SUM(t.seqid)-SUM(s.readseqid) AS unreadcount FROM topics AS t JOIN subscriptions AS s "+
		"ON t.name = CASE WHEN s.topic LIKE 'chn%' THEN 'grp' || SUBSTRING(s.topic FROM 4) ELSE s.topic END "+
		"WHERE s.userid IN (?) AND s.deletedat IS NULL AND t.state!=? AND "+
		"POSITION('R' IN s.modewant)>0 AND POSITION('R' IN s.modegiven)>0 GROUP BY s.userid", uids, t.StateDeleted)
	rows, err := a.db.Query(ctx, query, uids...)
	if err != nil {
		return counts, err
	}
	defer rows.Close()

	var userId int64
	var unreadCount int
	for rows.Next() {
		if err = rows.Scan(&userId, &unreadCount); err != nil {
			break
		}
		counts[store.EncodeUid(userId)] = unreadCount
	}
	if err == nil {
		err = rows.Err()
	}

	return counts, err
}

// UserGetUnvalidated 返回从未登录、没有已验证凭据且自 lastUpdatedBefore 以来未更新过的用户 ID 列表。
func (a *adapter) UserGetUnvalidated(lastUpdatedBefore time.Time, limit int) ([]t.Uid, error) {
	var uids []t.Uid

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	rows, err := a.db.Query(ctx,
		"SELECT u.id, COALESCE(SUM(CASE WHEN c.done THEN 1 ELSE 0 END), 0) AS total "+
			"FROM users u LEFT JOIN credentials c ON u.id = c.userid "+
			"WHERE u.lastseen IS NULL AND u.updatedat < $1 GROUP BY u.id, u.updatedat "+
			"HAVING COALESCE(SUM(CASE WHEN c.done THEN 1 ELSE 0 END), 0) = 0 ORDER BY u.updatedat ASC LIMIT $2",
		lastUpdatedBefore, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var userId int64
		var unused int
		if err = rows.Scan(&userId, &unused); err != nil {
			break
		}
		uids = append(uids, store.EncodeUid(userId))
	}
	if err == nil {
		err = rows.Err()
	}

	return uids, err
}

// *****************************

// topicCreate 将输入编码为picCreate。
func (a *adapter) topicCreate(ctx context.Context, tx pgx.Tx, topic *t.Topic) error {
	_, err := tx.Exec(ctx, "INSERT INTO topics(createdat,updatedat,touchedat,state,name,usebt,owner,access,public,trusted,tags,aux) "+
		"VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)",
		topic.CreatedAt, topic.UpdatedAt, topic.TouchedAt, topic.State, topic.Id, topic.UseBt,
		store.DecodeUid(t.ParseUid(topic.Owner)), topic.Access, common.ToJSON(topic.Public), common.ToJSON(topic.Trusted),
		topic.Tags, common.ToJSON(topic.Aux))
	if err != nil {
		return err
	}

	// 保存 Topic 的标签到单独的表以便 Topic 可被搜索。
	return addTags(ctx, tx, "topictags", "topic", topic.Id, topic.Tags, false)
}

// TopicCreate saves Topic object to 数据库.
func (a *adapter) TopicCreate(topic *t.Topic) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	err = a.topicCreate(ctx, tx, topic)
	if err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// If undelete = true - update 订阅 on duplicate key, otherwise ignore the duplicate.
func createSubscription(ctx context.Context, tx pgx.Tx, sub *t.Subscription, undelete bool) error {

	isOwner := (sub.ModeGiven & sub.ModeWant).IsOwner()

	jpriv := common.ToJSON(sub.Private)
	decoded_uid := store.DecodeUid(t.ParseUid(sub.User))
	_, err2 := tx.Exec(ctx, "SAVEPOINT createSub")
	if err2 != nil {
		log.Println("Error: Failed to create savepoint: ", err2.Error())
	}
	_, err := tx.Exec(ctx,
		"INSERT INTO subscriptions(createdat,updatedat,deletedat,userid,topic,modeWant,modeGiven,private) "+
			"VALUES($1,$2,NULL,$3,$4,$5,$6,$7)",
		sub.CreatedAt, sub.UpdatedAt, decoded_uid, sub.Topic, sub.ModeWant.String(), sub.ModeGiven.String(), jpriv)

	if err != nil && isDupe(err) {
		_, err2 = tx.Exec(ctx, "ROLLBACK TO SAVEPOINT createSub")
		if err2 != nil {
			log.Println("Error: Failed to rollback savepoint: ", err2.Error())
		}
		if undelete {
			_, err = tx.Exec(ctx, "UPDATE subscriptions SET createdat=$1,updatedat=$2,deletedat=NULL,modeWant=$3,modeGiven=$4,"+
				"delid=0,recvseqid=0,readseqid=0 WHERE topic=$5 AND userid=$6",
				sub.CreatedAt, sub.UpdatedAt, sub.ModeWant.String(), sub.ModeGiven.String(), sub.Topic, decoded_uid)
		} else {
			_, err = tx.Exec(ctx, "UPDATE subscriptions SET createdat=$1,updatedat=$2,deletedat=NULL,modeWant=$3,modeGiven=$4,"+
				"delid=0,recvseqid=0,readseqid=0,private=$5 WHERE topic=$6 AND userid=$7",
				sub.CreatedAt, sub.UpdatedAt, sub.ModeWant.String(), sub.ModeGiven.String(), jpriv,
				sub.Topic, decoded_uid)
		}
	} else {
		_, err2 = tx.Exec(ctx, "RELEASE SAVEPOINT createSub")
		if err2 != nil {
			log.Println("Error: Failed to release savepoint: ", err2.Error())
		}
	}
	if err == nil && isOwner {
		_, err = tx.Exec(ctx, "UPDATE topics SET owner=$1 WHERE name=$2", decoded_uid, sub.Topic)
	}
	return err
}

// TopicCreateP2P given two 用户 creates a p2p Topic
func (a *adapter) TopicCreateP2P(initiator, invited *t.Subscription) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	err = createSubscription(ctx, tx, initiator, false)
	if err != nil {
		return err
	}

	err = createSubscription(ctx, tx, invited, true)
	if err != nil {
		return err
	}

	topic := &t.Topic{ObjHeader: t.ObjHeader{Id: initiator.Topic}}
	topic.ObjHeader.MergeTimes(&initiator.ObjHeader)
	topic.TouchedAt = initiator.GetTouchedAt()
	err = a.topicCreate(ctx, tx, topic)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// TopicGet 按名称加载单个 Topic（如果存在）。如果 Topic 不存在返回 (nil, nil)
func (a *adapter) TopicGet(topic string) (*t.Topic, error) {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}

	// 按名称获取 Topic
	var tt = new(t.Topic)
	var owner int64
	err := a.db.QueryRow(ctx,
		"SELECT createdat,updatedat,state,stateat,touchedat,name AS id,usebt,access,owner,seqid,delid,subcnt,public,trusted,tags,aux "+
			"FROM topics WHERE name=$1",
		topic).Scan(&tt.CreatedAt, &tt.UpdatedAt, &tt.State, &tt.StateAt, &tt.TouchedAt, &tt.Id,
		&tt.UseBt, &tt.Access, &owner, &tt.SeqId, &tt.DelId, &tt.SubCnt, &tt.Public, &tt.Trusted, &tt.Tags, &tt.Aux)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Nothing found - clear the 错误
			err = nil
		}
		return nil, err
	}

	if t.GetTopicCat(topic) == t.TopicCatGrp {
		// Topic 已找到，获取订阅数。同时尝试 Topic 和 Channel 名称。
		var subCnt int
		if err = a.db.QueryRow(ctx,
			"SELECT COUNT(*) FROM subscriptions WHERE topic IN ($1,$2) AND deletedat IS NULL", topic, t.GrpToChn(topic)).
			Scan(&subCnt); err != nil {
			return nil, err
		}

		if subCnt != tt.SubCnt {
			// Update the Topic with the correct 订阅 count.
			tt.SubCnt = subCnt
			if _, err = a.db.Exec(ctx, "UPDATE topics SET subcnt=$1 WHERE name=$2", subCnt, topic); err != nil {
				return nil, err
			}
		}
	}

	tt.Owner = store.EncodeUid(owner).String()

	return tt, err
}

// TopicsForUser loads 用户's contact list: p2p and grp Topic, except for 'me' & 'fnd' 订阅.
// 读取并反归一化 Public 值。
func (a *adapter) TopicsForUser(uid t.Uid, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	// Fetch ALL 用户's 订阅, even those which has not been modified recently.
	// We are going to use these 订阅 to fetch Topic and 用户 which may have been modified recently.
	q := `SELECT createdat,updatedat,deletedat,topic,delid,recvseqid,
		readseqid,modewant,modegiven,private FROM subscriptions WHERE userid=?`
	args := []any{store.DecodeUid(uid)}
	if !keepDeleted {
		// 过滤已删除的行。
		q += " AND deletedat IS NULL"
	}

	limit := 0
	ims := time.Time{}
	if opts != nil {
		if opts.Topic != "" {
			q += " AND topic=?"
			args = append(args, opts.Topic)
		}

		// 仅在客户端不管理缓存（或冷启动）时应用限制。
		// Otherwise have to get all 订阅 and do a manual join with 用户/Topic.
		if opts.IfModifiedSince == nil {
			if opts.Limit > 0 && opts.Limit < a.maxResults {
				limit = opts.Limit
			} else {
				limit = a.maxResults
			}
		} else {
			ims = *opts.IfModifiedSince
		}
	} else {
		limit = a.maxResults
	}

	if limit > 0 {
		q += " LIMIT ?"
		args = append(args, limit)
	}

	q, args = expandQuery(q, args...)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	// 必须手动关闭 rows，因为我们将重用它们。

	// Fetch 订阅. Two queries are needed: 用户 table (p2p) and Topic table (grp).
	// Prepare a list of separate 订阅 to 用户 vs Topic
	join := make(map[string]t.Subscription) // Keeping these to make a join with table for .private and .access
	topq := make([]any, 0, 16)
	usrq := make([]any, 0, 16)
	for rows.Next() {
		var sub t.Subscription
		var modeWant, modeGiven []byte
		if err = rows.Scan(&sub.CreatedAt, &sub.UpdatedAt, &sub.DeletedAt, &sub.Topic, &sub.DelId,
			&sub.RecvSeqId, &sub.ReadSeqId, &modeWant, &modeGiven, &sub.Private); err != nil {
			break
		}
		sub.ModeWant.Scan(modeWant)
		sub.ModeGiven.Scan(modeGiven)
		tname := sub.Topic
		sub.User = uid.String()
		tcat := t.GetTopicCat(tname)

		if tcat == t.TopicCatMe || tcat == t.TopicCatFnd {
			// One of 'me', 'fnd' 订阅, skip.
			// Don't skip 'sys' 订阅.
			continue
		} else if tcat == t.TopicCatP2P {
			// P2P 订阅, find the other 用户 to get 用户.Public and 用户.Trusted.
			uid1, uid2, _ := t.ParseP2P(tname)
			if uid1 == uid {
				usrq = append(usrq, store.DecodeUid(uid2))
				sub.SetWith(uid2.UserId())
			} else {
				usrq = append(usrq, store.DecodeUid(uid1))
				sub.SetWith(uid1.UserId())
			}
		} else if tcat == t.TopicCatGrp {
			// 可能将 Channel 名称转换为 Topic 名称。
			tname = t.ChnToGrp(tname)
		}
		// No special handling needed for 'slf', 'sys' 订阅.

		topq = append(topq, tname)
		sub.Private = common.FromJSON(sub.Private)
		join[tname] = sub
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()

	if err != nil {
		return nil, err
	}

	var subs []t.Subscription
	if len(join) == 0 {
		return subs, nil
	}

	// Fetch grp Topic and join to 订阅.
	if len(topq) > 0 {
		q = "SELECT updatedat,state,touchedat,name AS id,usebt,access,seqid,delid,subcnt,public,trusted " +
			"FROM topics WHERE name IN (?)"
		newargs := []any{topq}

		if !keepDeleted {
			// 可选跳过已删除的 Topic。
			q += " AND state!=?"
			newargs = append(newargs, t.StateDeleted)
		}

		if !ims.IsZero() {
			// 如果提供了缓存时间戳：仅获取较新的条目。
			q += " AND touchedat>?"
			newargs = append(newargs, ims)

			if limit > 0 && limit < len(topq) {
				// 没有意义获取超过请求的限制。
				q += " ORDER BY touchedat LIMIT ?"
				newargs = append(newargs, limit)
			}
		}
		q, newargs = expandQuery(q, newargs...)

		ctx2, cancel2 := a.getContext()
		if cancel2 != nil {
			defer cancel2()
		}
		rows, err = a.db.Query(ctx2, q, newargs...)
		if err != nil {
			return nil, err
		}

		var top t.Topic
		for rows.Next() {
			if err = rows.Scan(&top.UpdatedAt, &top.State, &top.TouchedAt, &top.Id, &top.UseBt,
				&top.Access, &top.SeqId, &top.DelId, &top.SubCnt, &top.Public, &top.Trusted); err != nil {
				break
			}

			sub := join[top.Id]
			// 检查是否 sub.UpdatedAt needs to be adjusted to earlier or later time.
			sub.UpdatedAt = common.SelectLatestTime(sub.UpdatedAt, top.UpdatedAt)
			sub.SetState(top.State)
			sub.SetTouchedAt(top.TouchedAt)
			sub.SetSeqId(top.SeqId)
			if t.GetTopicCat(sub.Topic) == t.TopicCatGrp {
				sub.SetSubCnt(top.SubCnt)
				sub.SetPublic(top.Public)
				sub.SetTrusted(top.Trusted)
			}
			// 放回订阅的更新值，将在下面进一步处理
			join[top.Id] = sub
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()

		if err != nil {
			return nil, err
		}
	}

	// Fetch p2p 用户 and join to p2p 订阅.
	if len(usrq) > 0 {
		q = "SELECT id,updatedat,state,access,lastseen,useragent,public,trusted " +
			"FROM users WHERE id IN (?)"
		newargs := []any{usrq}
		if !keepDeleted {
			// Optionally skip deleted 用户.
			q += " AND state!=?"
			newargs = append(newargs, t.StateDeleted)
		}

		// Ignoring ipg: we need all 用户 to get LastSeen and UserAgent.

		q, newargs = expandQuery(q, newargs...)

		ctx3, cancel3 := a.getContext()
		if cancel3 != nil {
			defer cancel3()
		}
		rows, err = a.db.Query(ctx3, q, newargs...)
		if err != nil {
			return nil, err
		}

		for rows.Next() {
			var usr2 t.User
			var id int64
			if err = rows.Scan(&id, &usr2.UpdatedAt, &usr2.State, &usr2.Access, &usr2.LastSeen, &usr2.UserAgent,
				&usr2.Public, &usr2.Trusted); err != nil {
				break
			}

			usr2.Id = store.EncodeUid(id).String()
			joinOn := uid.P2PName(t.ParseUid(usr2.Id))
			if sub, ok := join[joinOn]; ok {
				sub.UpdatedAt = common.SelectLatestTime(sub.UpdatedAt, usr2.UpdatedAt)
				sub.SetState(usr2.State)
				sub.SetPublic(usr2.Public)
				sub.SetTrusted(usr2.Trusted)
				sub.SetDefaultAccess(usr2.Access.Auth, usr2.Access.Anon)
				sub.SetLastSeenAndUA(usr2.LastSeen, usr2.UserAgent)
				join[joinOn] = sub
			}
		}
		if err == nil {
			err = rows.Err()
		}
		rows.Close()

		if err != nil {
			return nil, err
		}
	}

	subs = make([]t.Subscription, 0, len(join))
	for _, sub := range join {
		subs = append(subs, sub)
	}

	return common.SelectEarliestUpdatedSubs(subs, opts, a.maxResults), nil
}

// UsersForTopic loads 用户 subscribed to the given Topic.
// The difference between UsersForTopic vs SubsForTopic is that the former loads 用户.Public,
// 后者不加载。
func (a *adapter) UsersForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	tcat := t.GetTopicCat(topic)

	// Fetch all subscribed 用户. The number of 用户 is not large
	q := `SELECT s.createdat,s.updatedat,s.deletedat,s.userid,s.topic,s.delid,s.recvseqid,
		s.readseqid,s.modewant,s.modegiven,u.public,u.trusted,u.lastseen,u.useragent,s.private
		FROM subscriptions AS s JOIN users AS u ON s.userid=u.id
		WHERE s.topic=?`
	args := []any{topic}
	if !keepDeleted {
		// Filter out rows with 用户 deleted
		q += " AND u.state!=?"
		args = append(args, t.StateDeleted)

		// For p2p Topic we must load all 订阅 including deleted.
		// 否则将无法交换 Public 值。
		if tcat != t.TopicCatP2P {
			// Filter out deleted 订阅.
			q += " AND s.deletedat IS NULL"
		}
	}

	limit := a.maxResults
	var oneUser t.Uid
	if opts != nil {
		// 忽略 IfModifiedSince：加载所有条目，因为 Topic 不会有太多订阅者。
		// 未修改的将去除 Public 和 Private。

		if !opts.User.IsZero() {
			// For p2p Topic we have to fetch both 用户 otherwise public cannot be swapped.
			if tcat != t.TopicCatP2P {
				q += " AND s.userid=?"
				args = append(args, store.DecodeUid(opts.User))
			}
			oneUser = opts.User
		}
		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}
	q += " LIMIT ?"
	args = append(args, limit)
	q, args = expandQuery(q, args...)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Fetch 订阅
	var sub t.Subscription
	var subs []t.Subscription
	var userId int64
	var modeWant, modeGiven []byte
	var lastSeen *time.Time = nil
	var userAgent string
	var public, trusted any
	for rows.Next() {
		if err = rows.Scan(
			&sub.CreatedAt, &sub.UpdatedAt, &sub.DeletedAt,
			&userId, &sub.Topic, &sub.DelId, &sub.RecvSeqId,
			&sub.ReadSeqId, &modeWant, &modeGiven,
			&public, &trusted, &lastSeen, &userAgent, &sub.Private); err != nil {
			break
		}

		sub.User = store.EncodeUid(userId).String()
		sub.SetPublic(public)
		sub.SetTrusted(trusted)
		sub.SetLastSeenAndUA(lastSeen, userAgent)
		sub.ModeWant.Scan(modeWant)
		sub.ModeGiven.Scan(modeGiven)
		subs = append(subs, sub)
	}
	if err == nil {
		err = rows.Err()
	}

	if err == nil && tcat == t.TopicCatP2P && len(subs) > 0 {
		// 按预期交换 P2P Topic 的 public 和 lastSeen 值。
		if len(subs) == 1 {
			// The other 用户 is deleted, nothing we can do.
			subs[0].SetPublic(nil)
			subs[0].SetTrusted(nil)
			subs[0].SetLastSeenAndUA(nil, "")
		} else {
			tmp := subs[0].GetPublic()
			subs[0].SetPublic(subs[1].GetPublic())
			subs[1].SetPublic(tmp)

			tmp = subs[0].GetTrusted()
			subs[0].SetTrusted(subs[1].GetTrusted())
			subs[1].SetTrusted(tmp)

			lastSeen := subs[0].GetLastSeen()
			userAgent = subs[0].GetUserAgent()
			subs[0].SetLastSeenAndUA(subs[1].GetLastSeen(), subs[1].GetUserAgent())
			subs[1].SetLastSeenAndUA(lastSeen, userAgent)
		}

		// Remove deleted and unneeded 订阅
		if !keepDeleted || !oneUser.IsZero() {
			var xsubs []t.Subscription
			for i := range subs {
				if (subs[i].DeletedAt != nil && !keepDeleted) || (!oneUser.IsZero() && subs[i].Uid() != oneUser) {
					continue
				}
				xsubs = append(xsubs, subs[i])
			}
			subs = xsubs
		}
	}

	return subs, err
}

// topicNamesForUser 使用提供的查询读取字符串切片。
func (a *adapter) topicNamesForUser(sqlQuery string, includeChan bool, args ...any) ([]string, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.Query(ctx, sqlQuery, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var names []string
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		names = append(names, name)
		// 如果名称是群组 Topic，同时添加 Channel 名称（如果请求）。
		if includeChan {
			if channel := t.GrpToChn(name); channel != "" {
				names = append(names, channel)
			}
		}
	}
	if err == nil {
		err = rows.Err()
	}

	return names, err
}

// OwnTopics loads a slice of Topic names where the 用户 is the owner.
func (a *adapter) OwnTopics(uid t.Uid) ([]string, error) {
	return a.topicNamesForUser("SELECT name FROM topics WHERE owner=$1 AND state!=$2",
		false, store.DecodeUid(uid), t.StateDeleted)
}

// ChannelsForUser loads a slice of Topic names where the 用户 is a Channel reader and notifications (P) are enabled.
func (a *adapter) ChannelsForUser(uid t.Uid) ([]string, error) {
	return a.topicNamesForUser("SELECT topic FROM subscriptions WHERE userid=$1 AND topic LIKE 'chn%' "+
		"AND POSITION('P' IN modewant)>0 AND POSITION('P' IN modegiven)>0 AND deletedat IS NULL",
		false, store.DecodeUid(uid))
}

// TopicShare creates Topic 订阅 and increments the Topic's subcnt.
func (a *adapter) TopicShare(topic string, shares []*t.Subscription) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	for _, sub := range shares {
		err = createSubscription(ctx, tx, sub, true)
		if err != nil {
			return err
		}
	}

	if topic != "" {
		if _, err = tx.Exec(ctx, "UPDATE topics SET subcnt=subcnt+$1 WHERE name=$2", len(shares), topic); err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// TopicDelete deletes Topic, 订阅, 消息.
func (a *adapter) TopicDelete(topic string, isChan, hard bool) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	// If the Topic is a Channel, must try to delete 订阅 under both grpXXX and chnXXX names.
	args := []any{topic}
	if isChan {
		args = append(args, t.GrpToChn(topic))
	}

	if hard {
		// Delete 订阅. If this is a Channel, delete both group 订阅 and Channel 订阅.
		q, args := expandQuery("DELETE FROM subscriptions WHERE topic IN (?)", args)
		if _, err = tx.Exec(ctx, q, args...); err != nil {
			return err
		}

		if err = messageDeleteList(ctx, tx, topic, nil); err != nil {
			return err
		}

		if _, err = tx.Exec(ctx, "DELETE FROM topictags WHERE topic=$1", topic); err != nil {
			return err
		}

		if _, err = tx.Exec(ctx, "DELETE FROM topics WHERE name=$1", topic); err != nil {
			return err
		}
	} else {
		now := t.TimeNow()

		q, args := expandQuery("UPDATE subscriptions SET updatedat=?,deletedat=? WHERE topic IN (?)", now, now, args)
		if _, err = tx.Exec(ctx, q, args...); err != nil {
			return err
		}

		if _, err = tx.Exec(ctx, "UPDATE topics SET updatedat=$1,touchedat=$1,state=$2,stateat=$1 WHERE name=$3",
			now, t.StateDeleted, topic); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

// TopicUpdateOnMessage 将输入编码为picUpdateOn消息。
func (a *adapter) TopicUpdateOnMessage(topic string, msg *t.Message) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.Exec(ctx, "UPDATE topics SET seqid=$1,touchedat=$2 WHERE name=$3", msg.SeqId, msg.CreatedAt, topic)

	return err
}

// TopicUpdateSubCnt 更新 Topic 中反归一化的订阅者计数。
func (a *adapter) TopicUpdateSubCnt(topic string) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.Exec(ctx,
		"UPDATE topics SET subcnt=(SELECT COUNT(*) FROM subscriptions WHERE topic IN ($1,$2) AND deletedat IS NULL) WHERE name=$1",
		topic, t.GrpToChn(topic))
	return err
}

// TopicUpdate 将输入编码为picUpdate。
func (a *adapter) TopicUpdate(topic string, update map[string]any) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	if t, u := update["TouchedAt"], update["UpdatedAt"]; t == nil && u != nil {
		update["TouchedAt"] = u
	}
	cols, args := common.UpdateByMap(update)
	q, args := expandQuery("UPDATE topics SET "+strings.Join(cols, ",")+" WHERE name=?", args, topic)
	_, err = tx.Exec(ctx, q, args...)
	if err != nil {
		return err
	}

	// 标签也存储在单独的表中
	if tags := common.ExtractTags(update); tags != nil {
		// First delete all 用户 tags
		_, err = tx.Exec(ctx, "DELETE FROM topictags WHERE topic=$1", topic)
		if err != nil {
			return err
		}
		// 现在插入新标签
		err = addTags(ctx, tx, "topictags", "topic", topic, tags, false)
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// TopicOwnerChange 将输入编码为picOwnerChange。
func (a *adapter) TopicOwnerChange(topic string, newOwner t.Uid) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.Exec(ctx, "UPDATE topics SET owner=$1 WHERE name=$2", store.DecodeUid(newOwner), topic)
	return err
}

// Get a 订阅 of a 用户 to a Topic.
func (a *adapter) SubscriptionGet(topic string, user t.Uid, keepDeleted bool) (*t.Subscription, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	query := `SELECT createdat,updatedat,deletedat,userid AS user,topic,delid,recvseqid,
		readseqid,modewant,modegiven,private FROM subscriptions WHERE topic=$1 AND userid=$2`
	if !keepDeleted {
		query += " AND deletedat IS NULL"
	}
	var sub t.Subscription
	var userId int64
	var modeWant, modeGiven []byte
	err := a.db.QueryRow(ctx, query, topic, store.DecodeUid(user)).Scan(&sub.CreatedAt, &sub.UpdatedAt, &sub.DeletedAt, &userId,
		&sub.Topic, &sub.DelId, &sub.RecvSeqId, &sub.ReadSeqId, &modeWant, &modeGiven, &sub.Private)

	if err != nil {
		if err == pgx.ErrNoRows {
			// Nothing found - clear the 错误
			err = nil
		}
		return nil, err
	}

	sub.User = store.EncodeUid(userId).String()
	sub.ModeWant.Scan(modeWant)
	sub.ModeGiven.Scan(modeGiven)

	return &sub, nil
}

// SubsForUser loads all 用户's 订阅. Does NOT load Public or Private values and does
// not load deleted 订阅.
func (a *adapter) SubsForUser(forUser t.Uid) ([]t.Subscription, error) {
	q := `SELECT createdat,updatedat,deletedat,userid AS user,topic,delid,recvseqid,
		readseqid,modewant,modegiven FROM subscriptions WHERE userid=$1 AND deletedat IS NULL`
	args := []any{store.DecodeUid(forUser)}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []t.Subscription
	var sub t.Subscription
	var userId int64
	var modeWant, modeGiven []byte
	for rows.Next() {
		if err = rows.Scan(&sub.CreatedAt, &sub.UpdatedAt, &sub.DeletedAt, &userId, &sub.Topic, &sub.DelId,
			&sub.RecvSeqId, &sub.ReadSeqId, &modeWant, &modeGiven); err != nil {
			break
		}

		sub.User = store.EncodeUid(userId).String()
		sub.ModeWant.Scan(modeWant)
		sub.ModeGiven.Scan(modeGiven)
		subs = append(subs, sub)
	}
	if err == nil {
		err = rows.Err()
	}

	return subs, err
}

// SubsForTopic 获取 Topic 的所有订阅。不加载 Public 值。
// UsersForTopic 与 SubsForTopic 的区别在于前者加载用户的 public+trusted，
// 后者不加载。
func (a *adapter) SubsForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	q := `SELECT createdat,updatedat,deletedat,userid AS user,topic,delid,recvseqid,
		readseqid,modewant,modegiven,private FROM subscriptions WHERE topic=?`

	args := []any{topic}
	if !keepDeleted {
		// 过滤已删除的行。
		q += " AND deletedat IS NULL"
	}
	limit := a.maxResults
	if opts != nil {
		// 忽略 IfModifiedSince - 必须返回所有条目
		// 未修改的将去除 Public 和 Private。

		if !opts.User.IsZero() {
			q += " AND userid=?"
			args = append(args, store.DecodeUid(opts.User))
		}
		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}

	q += " LIMIT ?"
	args = append(args, limit)
	q, args = expandQuery(q, args...)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []t.Subscription
	var sub t.Subscription
	var userId int64
	var modeWant, modeGiven []byte
	for rows.Next() {
		if err = rows.Scan(&sub.CreatedAt, &sub.UpdatedAt, &sub.DeletedAt, &userId, &sub.Topic, &sub.DelId,
			&sub.RecvSeqId, &sub.ReadSeqId, &modeWant, &modeGiven, &sub.Private); err != nil {
			break
		}

		sub.User = store.EncodeUid(userId).String()
		sub.ModeWant.Scan(modeWant)
		sub.ModeGiven.Scan(modeGiven)
		subs = append(subs, sub)
	}
	if err == nil {
		err = rows.Err()
	}

	return subs, err
}

// SubsUpdate updates one or multiple 订阅 to a Topic.
func (a *adapter) SubsUpdate(topic string, user t.Uid, update map[string]any) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	cols, args := common.UpdateByMap(update)
	q := "UPDATE subscriptions SET " + strings.Join(cols, ",") + " WHERE topic=?"
	args = append(args, topic)
	if !user.IsZero() {
		// Update just one Topic 订阅
		q += " AND userid=?"
		args = append(args, store.DecodeUid(user))
	}
	q, args = expandQuery(q, args...)

	if _, err = tx.Exec(ctx, q, args...); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// SubsDelete marks at most one 订阅 as deleted.
func (a *adapter) SubsDelete(topic string, user t.Uid) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	tx, err := a.db.Begin(ctx)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	decoded_id := store.DecodeUid(user)
	now := t.TimeNow()
	res, err := tx.Exec(ctx,
		"UPDATE subscriptions SET updatedat=$1,deletedat=$2 WHERE topic=$3 AND userid=$4 AND deletedat IS NULL",
		now, now, topic, decoded_id)
	if err != nil {
		return err
	}

	affected := res.RowsAffected()
	if affected == 0 {
		// 确保上面的 tx.Rollback() 被执行
		err = t.ErrNotFound
		return err
	}

	// Channel readers cannot delete 消息.
	if !t.IsChannel(topic) {
		// Remove records of 消息 soft-deleted by this 用户.
		_, err = tx.Exec(ctx, "DELETE FROM dellog WHERE topic=$1 AND deletedfor=$2", topic, decoded_id)
		if err != nil {
			return err
		}
	}

	if t.GetTopicCat(topic) == t.TopicCatGrp {
		// Decrement Topic 订阅 count (only one 订阅 is	deleted).
		_, err = tx.Exec(ctx, "UPDATE topics SET subcnt=subcnt-1 WHERE name=$1", t.ChnToGrp(topic))
		if err != nil {
			return err
		}
	}

	return tx.Commit(ctx)
}

// subsDelForUser marks 用户's 订阅 as deleted.
func subsDelForUser(ctx context.Context, tx pgx.Tx, decoded_uid int64, hard bool) error {
	// Decrement 订阅 count for all Topic the 用户 is subscribed to.
	rows, err := tx.Query(ctx, "SELECT topic FROM subscriptions WHERE userid=$1 AND deletedat IS NULL", decoded_uid)
	if err != nil {
		return err
	}
	var topics []any
	for rows.Next() {
		var name string
		if err = rows.Scan(&name); err != nil {
			break
		}
		if t.IsChannel(name) {
			// 将 Channel 名称转换为群组名称。
			name = t.ChnToGrp(name)
		}
		topics = append(topics, name)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	if len(topics) > 0 {
		sql, args, _ := sqlx.In("UPDATE topics SET subcnt=subcnt-1 WHERE name IN (?)", topics)
		_, err = tx.Exec(ctx, sqlx.Rebind(sqlx.DOLLAR, sql), args...)
		if err != nil {
			return err
		}
	}

	if hard {
		// Hard delete: remove all 订阅 for the 用户.
		_, err = tx.Exec(ctx, "DELETE FROM subscriptions WHERE userid=$1", decoded_uid)
	} else {
		now := t.TimeNow()
		_, err = tx.Exec(ctx, "UPDATE subscriptions SET updatedat=$1,deletedat=$2 WHERE userid=$3 AND deletedat IS NULL;",
			now, now, decoded_uid)
	}
	return err
}

// SubsDelForUser marks 用户's 订阅 as deleted.
func (a *adapter) SubsDelForUser(user t.Uid, hard bool) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}

	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	if err = subsDelForUser(ctx, tx, store.DecodeUid(user), hard); err != nil {
		return err
	}

	return tx.Commit(ctx)

}

// Find returns a list of 用户 and group Topic which match the given tags, such as "email:jdoe@example.com" or "tel:+18003287448".
func (a *adapter) Find(caller, promoPrefix string, req [][]string, opt []string, activeOnly bool) ([]t.Subscription, error) {
	index := make(map[string]struct{})
	var args []any
	constraint := ""
	allReq := t.FlattenDoubleSlice(req)
	for _, tag := range append(allReq, opt...) {
		args = append(args, tag)
		index[tag] = struct{}{}
	}
	if len(args) == 0 {
		// 没有要搜索的内容。
		return nil, nil
	}
	constraint += "tg.tag IN (?) "
	constraint, args, err := sqlx.In(constraint, args)
	if err != nil {
		return nil, err
	}
	if activeOnly {
		args = append(args, t.StateOK)
		constraint += "AND state=? "
	}
	constraint = sqlx.Rebind(sqlx.DOLLAR, constraint)

	var matcher string
	if promoPrefix != "" {
		// 最大标签数为 16。使用 20 确保一个前缀匹配大于所有非前缀匹配的总和。
		matcher = "SUM(CASE WHEN POSITION('" + promoPrefix + "' IN tg.tag)=1 THEN 20 ELSE 1 END)"
	} else {
		matcher = "COUNT(*)"
	}

	query := "SELECT CAST(u.id AS VARCHAR) AS topic,u.createdat,u.updatedat,FALSE,u.access::jsonb,0 AS subcnt,u.public::jsonb,u.trusted::jsonb,u.tags::jsonb," +
		matcher + " AS matches " +
		"FROM users AS u JOIN usertags AS tg ON tg.userid=u.id " +
		"WHERE " + constraint +
		"GROUP BY u.id,u.createdat,u.updatedat,u.access::jsonb,u.public::jsonb,u.trusted::jsonb,u.tags::jsonb "

	having := ""
	if len(allReq) > 0 {
		var a []any
		having, a = common.DisjunctionSql(req, "tg.tag")
		having = rebindWithStart(having, len(args)+1)
		query += having
		args = append(args, a...)
	}

	query += "UNION ALL "

	query += "SELECT t.name AS topic,t.createdat,t.updatedat,t.usebt,t.access::jsonb,t.subcnt,t.public::jsonb,t.trusted::jsonb,t.tags::jsonb," +
		matcher + " AS matches " +
		"FROM topics AS t JOIN topictags AS tg ON t.name=tg.topic " +
		"WHERE " + constraint +
		"GROUP BY t.name,t.createdat,t.updatedat,t.usebt,t.access::jsonb,t.subcnt,t.public::jsonb,t.trusted::jsonb,t.tags::jsonb "
	if having != "" {
		query += having
	}
	args = append(args, a.maxResults)
	query += "ORDER BY matches DESC, subcnt DESC LIMIT $" + strconv.Itoa(len(args))

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	// Get 用户 matched by tags, sort by number of matches from high to low.
	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Fetch 订阅
	var public, trusted any
	var access t.DefaultAccess
	var subcnt int
	var setTags t.StringSlice
	var ignored int
	var isChan bool
	var sub t.Subscription
	var subs []t.Subscription
	for rows.Next() {
		if err = rows.Scan(&sub.Topic, &sub.CreatedAt, &sub.UpdatedAt, &isChan, &access, &subcnt,
			&public, &trusted, &setTags, &ignored); err != nil {
			subs = nil
			break
		}

		if id, err := strconv.ParseInt(sub.Topic, 10, 64); err == nil {
			sub.Topic = store.EncodeUid(id).UserId()
			if sub.Topic == caller {
				// 跳过调用者自身。
				continue
			}
		}

		if isChan {
			// 这是一个 Channel，将 grp 转换为 chn 名称。
			sub.Topic = t.GrpToChn(sub.Topic)
		}

		sub.SetSubCnt(subcnt)
		sub.SetPublic(public)
		sub.SetTrusted(trusted)
		sub.SetDefaultAccess(access.Auth, access.Anon)
		// 表示模式未设置，不是 'N'。
		sub.ModeGiven = t.ModeUnset
		sub.ModeWant = t.ModeUnset
		sub.Private = common.FilterFoundTags(setTags, index)
		subs = append(subs, sub)
	}

	if err == nil {
		err = rows.Err()
	}

	return subs, err

}

// FindByName 按公开 alias 子串发现用户，并按 alias 或 Public.fn 发现公开 Topic。
func (a *adapter) FindByName(caller string, search *t.PeerSearchQuery) ([]t.Subscription, error) {
	if search == nil || search.Query == "" {
		return nil, nil
	}
	needle := common.EscapeLike(strings.ToLower(search.Query))
	aliasPattern := strings.ToLower(search.AliasPrefix) + ":%" + needle + "%"
	namePattern := "%" + needle + "%"
	stateFilter := ""
	var userArgs []any
	if search.ActiveOnly {
		stateFilter = "u.state=? AND "
		userArgs = append(userArgs, t.StateOK)
	}
	aliasConstraint := "FALSE"
	if search.AliasPrefix != "" {
		aliasConstraint = "EXISTS(SELECT 1 FROM usertags ut WHERE ut.userid=u.id AND LOWER(ut.tag) LIKE ? ESCAPE '!')"
		userArgs = append(userArgs, aliasPattern)
	}
	userArgs = append(userArgs, a.maxResults)
	userQuery, userArgs := expandQuery(
		"SELECT CAST(u.id AS TEXT),u.createdat,u.updatedat,u.access::jsonb,u.public::jsonb,u.trusted::jsonb,u.tags::jsonb"+
			" FROM users u WHERE "+stateFilter+aliasConstraint+" LIMIT ?", userArgs...)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	userRows, err := a.db.Query(ctx, userQuery, userArgs...)
	if err != nil {
		return nil, err
	}
	var found []t.Subscription
	for userRows.Next() {
		var rawTopic string
		var sub t.Subscription
		var access t.DefaultAccess
		var public, trusted any
		var tags t.StringSlice
		if err = userRows.Scan(&rawTopic, &sub.CreatedAt, &sub.UpdatedAt, &access, &public, &trusted, &tags); err != nil {
			break
		}
		id, parseErr := strconv.ParseInt(rawTopic, 10, 64)
		if parseErr != nil {
			continue
		}
		sub.Topic = store.EncodeUid(id).UserId()
		if sub.Topic == caller {
			continue
		}
		decodedPublic := common.FromJSON(public)
		score, matched := common.RankPeerSearch(sub.Topic, search.Query, search.AliasPrefix, tags, decodedPublic)
		if score == 0 {
			continue
		}
		sub.SetPublic(decodedPublic)
		sub.SetTrusted(common.FromJSON(trusted))
		sub.SetDefaultAccess(access.Auth, access.Anon)
		sub.SetSearchScore(score)
		sub.Private = matched
		found = append(found, sub)
	}
	userRows.Close()
	if err == nil {
		err = userRows.Err()
	}
	if err != nil {
		return nil, err
	}

	stateFilter = ""
	var topicArgs []any
	if search.ActiveOnly {
		stateFilter = "t.state=? AND "
		topicArgs = append(topicArgs, t.StateOK)
	}
	aliasConstraint = "FALSE"
	if search.AliasPrefix != "" {
		aliasConstraint = "EXISTS(SELECT 1 FROM topictags tt WHERE tt.topic=t.name AND LOWER(tt.tag) LIKE ? ESCAPE '!')"
		topicArgs = append(topicArgs, aliasPattern)
	}
	topicArgs = append(topicArgs, namePattern, a.maxResults)
	topicQuery, topicArgs := expandQuery(
		"SELECT t.name,t.createdat,t.updatedat,t.usebt,t.access::jsonb,t.subcnt,t.public::jsonb,t.trusted::jsonb,t.tags::jsonb"+
			" FROM topics t WHERE "+stateFilter+"("+aliasConstraint+
			" OR LOWER(COALESCE(t.public::jsonb->>'fn','')) LIKE ? ESCAPE '!') LIMIT ?", topicArgs...)
	topicRows, err := a.db.Query(ctx, topicQuery, topicArgs...)
	if err != nil {
		return nil, err
	}
	defer topicRows.Close()
	for topicRows.Next() {
		var sub t.Subscription
		var isChan bool
		var access t.DefaultAccess
		var subCnt int
		var public, trusted any
		var tags t.StringSlice
		if err = topicRows.Scan(&sub.Topic, &sub.CreatedAt, &sub.UpdatedAt, &isChan, &access,
			&subCnt, &public, &trusted, &tags); err != nil {
			break
		}
		decodedPublic := common.FromJSON(public)
		score, matched := common.RankPeerSearch(sub.Topic, search.Query, search.AliasPrefix, tags, decodedPublic)
		if score == 0 {
			continue
		}
		if isChan {
			sub.Topic = t.GrpToChn(sub.Topic)
		}
		sub.SetSubCnt(subCnt)
		sub.SetPublic(decodedPublic)
		sub.SetTrusted(common.FromJSON(trusted))
		sub.SetDefaultAccess(access.Auth, access.Anon)
		sub.SetSearchScore(score)
		sub.Private = matched
		found = append(found, sub)
	}
	if err == nil {
		err = topicRows.Err()
	}
	return found, err
}

// FindOne returns Topic or 用户 which matches the given tag.
func (a *adapter) FindOne(tag string) (string, error) {
	var args []any
	query := "SELECT t.name AS topic FROM topics AS t LEFT JOIN topictags AS tt ON t.name=tt.topic " +
		"WHERE tt.tag=?"
	args = append(args, tag)

	query += " UNION ALL "

	query += "SELECT CAST(u.id AS VARCHAR) AS topic FROM users AS u LEFT JOIN usertags AS ut ON ut.userid=u.id " +
		"WHERE ut.tag=?"
	args = append(args, tag)

	// LIMIT 应用于所有结果行。
	query += " LIMIT 1"

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	query, args = expandQuery(query, args)
	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return "", err
	}
	defer rows.Close()

	var found string
	if rows.Next() {
		if err = rows.Scan(&found); err != nil {
			return "", err
		}

		// 检查是否 the found value is a Topic name or a 用户 ID.
		// 用户 IDs are returned as decoded decimal strings.
		if id, err := strconv.ParseInt(found, 10, 64); err == nil {
			found = store.EncodeUid(id).UserId()
		}
	}
	if err == nil {
		err = rows.Err()
	}

	return found, err
}

// 消息
func (a *adapter) MessageSave(msg *t.Message) error {
	msg.InitClientKey()
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	// 存储 assignes 消息 ID, but we don't use it. 消息 IDs are not used anywhere.
	// Using a sequential ID provided by the 数据库.
	var id int
	var clientID any
	if msg.ClientId != "" {
		clientID = msg.ClientId
	}
	err := a.db.QueryRow(ctx,
		`INSERT INTO messages(createdAt,updatedAt,seqid,topic,"from",clientid,clientkey,head,content,searchtext)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		msg.CreatedAt, msg.UpdatedAt, msg.SeqId, msg.Topic,
		store.DecodeUid(t.ParseUid(msg.From)), clientID, common.NullableString(msg.ClientKey), msg.Head,
		common.ToJSON(msg.Content), msg.SearchText).Scan(&id)
	if err == nil {
		// Replacing ID given by 存储 by ID given by the DB.
		msg.SetUid(t.Uid(id))
	}
	return err
}

// MessageSaveAtomic 在同一事务中推进 Topic 游标并保存消息。
func (a *adapter) MessageSaveAtomic(msg *t.Message) error {
	msg.InitClientKey()
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	if _, err = tx.Exec(ctx,
		"UPDATE topics SET seqid=$1,touchedat=$2 WHERE name=$3",
		msg.SeqId, msg.CreatedAt, msg.Topic); err != nil {
		return err
	}
	var clientID any
	if msg.ClientId != "" {
		clientID = msg.ClientId
	}
	var id int
	err = tx.QueryRow(ctx,
		`INSERT INTO messages(createdAt,updatedAt,seqid,topic,"from",clientid,clientkey,head,content,searchtext)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) RETURNING id`,
		msg.CreatedAt, msg.UpdatedAt, msg.SeqId, msg.Topic,
		store.DecodeUid(t.ParseUid(msg.From)), clientID, common.NullableString(msg.ClientKey), msg.Head,
		common.ToJSON(msg.Content), msg.SearchText).Scan(&id)
	if err != nil {
		return err
	}
	if err = tx.Commit(ctx); err != nil {
		return err
	}
	msg.SetUid(t.Uid(id))
	return nil
}

// MessageGetByClientId 按 Topic、发送者和客户端幂等键查询已投递消息。
func (a *adapter) MessageGetByClientId(topic string, from t.Uid, clientID string) (*t.Message, error) {
	if clientID == "" {
		return nil, nil
	}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var msg t.Message
	var fromID int64
	err := a.db.QueryRow(ctx,
		`SELECT createdat,updatedat,deletedat,delid,seqid,topic,"from",clientid,head,content
		 FROM messages WHERE topic=$1 AND clientkey=$2 LIMIT 1`,
		topic, t.MessageClientKey(from, clientID)).Scan(
		&msg.CreatedAt, &msg.UpdatedAt, &msg.DeletedAt, &msg.DelId, &msg.SeqId,
		&msg.Topic, &fromID, &msg.ClientId, &msg.Head, &msg.Content)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	msg.From = store.EncodeUid(fromID).String()
	msg.Content = common.FromJSON(msg.Content)
	return &msg, nil
}

// MessageGet 按 Topic 和 SeqId 查询一条未硬删除消息。
func (a *adapter) MessageGet(topic string, seqID int) (*t.Message, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var msg t.Message
	var id, fromID int64
	err := a.db.QueryRow(ctx,
		`SELECT id,createdat,updatedat,deletedat,delid,seqid,topic,"from",COALESCE(clientid,''),head,content
		 FROM messages WHERE topic=$1 AND seqid=$2 AND delid=0 LIMIT 1`,
		topic, seqID).Scan(
		&id, &msg.CreatedAt, &msg.UpdatedAt, &msg.DeletedAt, &msg.DelId, &msg.SeqId,
		&msg.Topic, &fromID, &msg.ClientId, &msg.Head, &msg.Content)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	msg.SetUid(t.Uid(id))
	msg.From = store.EncodeUid(fromID).String()
	msg.Content = common.FromJSON(msg.Content)
	return &msg, nil
}

// MessageUpdate 更新现存消息的正文、消息头和修改时间。
func (a *adapter) MessageUpdate(msg *t.Message) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	tag, err := a.db.Exec(ctx,
		`UPDATE messages SET updatedat=$1,head=$2,content=$3,searchtext=$4 WHERE topic=$5 AND seqid=$6 AND delid=0`,
		msg.UpdatedAt, msg.Head, common.ToJSON(msg.Content), msg.SearchText, msg.Topic, msg.SeqId)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return t.ErrNotFound
	}
	return nil
}

// MessageSchedule 将消息快照写入持久化定时队列。
func (a *adapter) MessageSchedule(msg *t.ScheduledMessage) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.Exec(ctx,
		`INSERT INTO scheduledmessages(id,createdat,updatedat,publishat,topic,"from",clientid,noecho,head,content,attachmenturls,attachments)
		 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)`,
		store.DecodeUid(msg.Uid()), msg.CreatedAt, msg.UpdatedAt, msg.PublishAt, msg.Topic,
		store.DecodeUid(t.ParseUid(msg.From)), msg.ClientId, msg.NoEcho, msg.Head,
		common.ToJSON(msg.Content), msg.AttachmentURLs, msg.Attachments)
	return err
}

// MessageGetScheduledByClientId 按发送者范围内的幂等键查询待投递消息。
func (a *adapter) MessageGetScheduledByClientId(topic string, from t.Uid, clientID string) (*t.ScheduledMessage, error) {
	if clientID == "" {
		return nil, nil
	}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var msg t.ScheduledMessage
	var id, fromID int64
	err := a.db.QueryRow(ctx,
		`SELECT id,createdat,updatedat,publishat,topic,"from",clientid,noecho,head,content,attachmenturls,attachments
		 FROM scheduledmessages WHERE topic=$1 AND "from"=$2 AND clientid=$3 LIMIT 1`,
		topic, store.DecodeUid(from), clientID).Scan(
		&id, &msg.CreatedAt, &msg.UpdatedAt, &msg.PublishAt, &msg.Topic, &fromID,
		&msg.ClientId, &msg.NoEcho, &msg.Head, &msg.Content, &msg.AttachmentURLs, &msg.Attachments)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	msg.SetUid(store.EncodeUid(id))
	msg.From = store.EncodeUid(fromID).String()
	msg.Content = common.FromJSON(msg.Content)
	return &msg, nil
}

// MessageGetDueScheduled 按计划时间升序读取一批已到期消息。
func (a *adapter) MessageGetDueScheduled(now time.Time, limit int) ([]t.ScheduledMessage, error) {
	if limit <= 0 {
		limit = a.maxMessageResults
	}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.Query(ctx,
		`SELECT id,createdat,updatedat,publishat,topic,"from",clientid,noecho,head,content,attachmenturls,attachments
		 FROM scheduledmessages WHERE publishat<=$1 ORDER BY publishat LIMIT $2`, now, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []t.ScheduledMessage
	for rows.Next() {
		var msg t.ScheduledMessage
		var id, fromID int64
		if err = rows.Scan(&id, &msg.CreatedAt, &msg.UpdatedAt, &msg.PublishAt, &msg.Topic, &fromID,
			&msg.ClientId, &msg.NoEcho, &msg.Head, &msg.Content, &msg.AttachmentURLs, &msg.Attachments); err != nil {
			return nil, err
		}
		msg.SetUid(store.EncodeUid(id))
		msg.From = store.EncodeUid(fromID).String()
		msg.Content = common.FromJSON(msg.Content)
		out = append(out, msg)
	}
	return out, rows.Err()
}

// MessageDeleteScheduled 删除指定发送者拥有的定时消息。
func (a *adapter) MessageDeleteScheduled(id, topic string, from t.Uid) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.Exec(ctx, `DELETE FROM scheduledmessages WHERE id=$1 AND topic=$2 AND "from"=$3`,
		store.DecodeUid(t.ParseUid(id)), topic, store.DecodeUid(from))
	return err
}

// MessageGetAll 完成消息GetAll所需的内部处理。
func (a *adapter) MessageGetAll(topic string, forUser t.Uid, opts *t.QueryOpt) ([]t.Message, error) {
	var limit = a.maxMessageResults
	order := "DESC"

	args := []any{store.DecodeUid(forUser), topic}
	seqIdConstraint := ""
	modifiedConstraint := ""
	if opts != nil {
		seqIdConstraint = "AND m.seqid "
		if len(opts.IdRanges) > 0 {
			constr, newargs := common.RangesToSql(opts.IdRanges)
			seqIdConstraint += constr
			args = append(args, newargs...)
		} else {
			seqIdConstraint += "BETWEEN ? AND ?"
			if opts.Since > 0 {
				args = append(args, opts.Since)
			} else {
				args = append(args, 0)
			}
			if opts.Before > 0 {
				// BETWEEN 是包含两端的，IM API 要求包含起始不包含结束，因此 -1
				args = append(args, opts.Before-1)
			} else {
				args = append(args, 1<<31-1)
			}
		}

		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
		if opts.Forward {
			order = "ASC"
		}
		if opts.IfModifiedSince != nil {
			modifiedConstraint = " AND m.updatedat>?"
			args = append(args, *opts.IfModifiedSince)
			order = "ASC"
		}
	}

	args = append(args, limit)
	orderBy := "m.seqid " + order
	if opts != nil && opts.IfModifiedSince != nil {
		orderBy = "m.updatedat " + order + ",m.seqid " + order
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	query, args := expandQuery(`SELECT m.createdat,m.updatedat,m.deletedat,m.delid,m.seqid,m.topic,m."from",COALESCE(m.clientid,''),m.head,m.content`+
		" FROM messages AS m LEFT JOIN dellog AS d"+
		" ON d.topic=m.topic AND m.seqid BETWEEN d.low AND d.hi-1 AND d.deletedfor=?"+
		" WHERE m.delid=0 AND m.topic=? "+seqIdConstraint+modifiedConstraint+" AND d.deletedfor IS NULL"+
		" ORDER BY "+orderBy+" LIMIT ?", args...)
	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	msgs := make([]t.Message, 0, limit)
	for rows.Next() {
		var msg t.Message
		var from int64
		if err = rows.Scan(&msg.CreatedAt, &msg.UpdatedAt, &msg.DeletedAt, &msg.DelId, &msg.SeqId,
			&msg.Topic, &from, &msg.ClientId, &msg.Head, &msg.Content); err != nil {
			break
		}
		msg.From = store.EncodeUid(from).String()
		msgs = append(msgs, msg)
	}
	if err == nil {
		err = rows.Err()
	}

	return msgs, err
}

// MessageSearch 在单个 Topic 内按规范化正文搜索消息，并排除调用者已删除的消息。
func (a *adapter) MessageSearch(topic string, forUser t.Uid, search *t.MessageSearchQuery) ([]t.Message, error) {
	if search == nil || search.Query == "" {
		return nil, nil
	}
	limit := search.Limit
	if limit <= 0 || limit > a.maxMessageResults {
		limit = a.maxMessageResults
	}

	// deletedFor 参数位于 JOIN 中，因此必须排在 Topic 和正文条件之前。
	args := []any{store.DecodeUid(forUser), topic, "%" + common.EscapeLike(strings.ToLower(search.Query)) + "%"}
	where := "m.delid=0 AND m.topic=? AND LOWER(COALESCE(m.searchtext,'')) LIKE ? ESCAPE '!' AND d.deletedfor IS NULL"
	if !search.From.IsZero() {
		where += ` AND m."from"=?`
		args = append(args, store.DecodeUid(search.From))
	}
	if len(search.Kinds) > 0 {
		where += " AND COALESCE(m.head::jsonb->>'x-kind','') IN (?" + strings.Repeat(",?", len(search.Kinds)-1) + ")"
		for _, kind := range search.Kinds {
			args = append(args, kind)
		}
	}
	if search.MinDate != nil {
		where += " AND m.createdat>=?"
		args = append(args, *search.MinDate)
	}
	if search.MaxDate != nil {
		where += " AND m.createdat<?"
		args = append(args, *search.MaxDate)
	}
	if search.BeforeSeq > 0 {
		where += " AND m.seqid<?"
		args = append(args, search.BeforeSeq)
	}
	args = append(args, limit)

	query, args := expandQuery(
		`SELECT m.createdat,m.updatedat,m.deletedat,m.delid,m.seqid,m.topic,m."from",`+
			`COALESCE(m.clientid,''),m.head,m.content,m.searchtext`+
			` FROM messages AS m LEFT JOIN dellog AS d`+
			` ON d.topic=m.topic AND m.seqid BETWEEN d.low AND d.hi-1 AND d.deletedfor=?`+
			` WHERE `+where+` ORDER BY m.seqid DESC LIMIT ?`, args...)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	messages := make([]t.Message, 0, limit)
	for rows.Next() {
		var msg t.Message
		var from int64
		if err = rows.Scan(&msg.CreatedAt, &msg.UpdatedAt, &msg.DeletedAt, &msg.DelId,
			&msg.SeqId, &msg.Topic, &from, &msg.ClientId, &msg.Head, &msg.Content,
			&msg.SearchText); err != nil {
			return nil, err
		}
		msg.From = store.EncodeUid(from).String()
		msg.Content = common.FromJSON(msg.Content)
		messages = append(messages, msg)
	}
	return messages, rows.Err()
}

// Get ranges of deleted 消息
func (a *adapter) MessageGetDeleted(topic string, forUser t.Uid, opts *t.QueryOpt) ([]t.DelMessage, error) {
	var limit = a.maxResults
	var lower = 0
	var upper = 1<<31 - 1

	if opts != nil {
		if opts.Since > 0 {
			lower = opts.Since
		}
		if opts.Before > 1 {
			// DelRange 是包含起始不包含结束，而 BETWEEN 是包含两端的。
			upper = opts.Before - 1
		}

		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}

	// 获取删除日志
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.Query(ctx, "SELECT topic,deletedfor,delid,low,hi FROM dellog WHERE topic=$1 AND delid BETWEEN $2 AND $3"+
		" AND (deletedFor=0 OR deletedFor=$4) ORDER BY delid LIMIT $5",
		topic, lower, upper, store.DecodeUid(forUser), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var dellog struct {
		Topic      string
		Deletedfor int64
		Delid      int
		Low        int
		Hi         int
	}
	var dmsgs []t.DelMessage
	var dmsg t.DelMessage
	for rows.Next() {
		if err = rows.Scan(&dellog.Topic, &dellog.Deletedfor, &dellog.Delid, &dellog.Low, &dellog.Hi); err != nil {
			dmsgs = nil
			break
		}

		if dellog.Delid != dmsg.DelId {
			if dmsg.DelId > 0 {
				dmsgs = append(dmsgs, dmsg)
			}
			dmsg.DelId = dellog.Delid
			dmsg.Topic = dellog.Topic
			if dellog.Deletedfor > 0 {
				dmsg.DeletedFor = store.EncodeUid(dellog.Deletedfor).String()
			} else {
				dmsg.DeletedFor = ""
			}
			dmsg.SeqIdRanges = nil
		}
		if dellog.Hi <= dellog.Low+1 {
			dellog.Hi = 0
		}
		dmsg.SeqIdRanges = append(dmsg.SeqIdRanges, t.Range{Low: dellog.Low, Hi: dellog.Hi})
	}
	if err == nil {
		err = rows.Err()
	}

	if err == nil {
		if dmsg.DelId > 0 {
			dmsgs = append(dmsgs, dmsg)
		}
	}

	return dmsgs, err
}

// messageDeleteList 完成消息删除List所需的内部处理。
func messageDeleteList(ctx context.Context, tx pgx.Tx, topic string, toDel *t.DelMessage) error {
	var err error

	if toDel == nil {
		// Whole Topic is being deleted, thus also deleting all 消息.
		_, err = tx.Exec(ctx, "DELETE FROM dellog WHERE topic=$1", topic)
		if err == nil {
			_, err = tx.Exec(ctx, "DELETE FROM messages WHERE topic=$1", topic)
		}
		// filemsglinks 将因 ON DELETE CASCADE 而被删除
		return err
	}

	// Only some 消息 are being deleted

	delRanges := toDel.SeqIdRanges

	if toDel.DeletedFor == "" {
		// Hard-deleting 消息 requires updates to the 消息 table.
		where := "m.topic=? "
		args := []any{topic}

		if len(delRanges) > 0 {
			rSql, rArgs := common.RangesToSql(delRanges)
			where += " AND m.seqid " + rSql
			args = append(args, rArgs...)
		}

		where += " AND m.deletedat IS NULL"

		// We are asked to delete 消息 no older than newerThan.
		if newerThan := toDel.GetNewerThan(); newerThan != nil {
			where += " AND m.createdat>?"
			args = append(args, newerThan)
		}

		// Find the actual IDs still present in the 数据库.
		var seqIDs []int
		query, newargs := expandQuery("SELECT seqid FROM messages AS m WHERE "+where, args)
		rows, err := tx.Query(ctx, query, newargs...)
		if err != nil {
			return err
		}
		defer rows.Close()

		for rows.Next() {
			var seqID int
			if err := rows.Scan(&seqID); err != nil {
				return err
			}
			seqIDs = append(seqIDs, seqID)
		}
		if err = rows.Err(); err != nil {
			return err
		}

		if len(seqIDs) == 0 {
			// 无需删除。无需记录日志。完成。
			return nil
		}

		// 重新计算实际要删除的范围。
		sort.Ints(seqIDs)
		delRanges = t.SliceToRanges(seqIDs)

		// 用新范围组成新查询。
		where = "m.topic=?"
		args = []any{topic}
		rSql, rArgs := common.RangesToSql(delRanges)
		where += " AND m.seqid " + rSql
		args = append(args, rArgs...)

		// 无需添加其他内容：deletedat 等已被考虑。

		query, newargs = expandQuery("DELETE FROM filemsglinks AS fml USING messages AS m WHERE m.id=fml.msgid AND "+
			where, args...)
		_, err = tx.Exec(ctx, query, newargs...)
		if err != nil {
			return err
		}

		query, newargs = expandQuery(`UPDATE messages AS m SET deletedat=?,delid=?,"from"=0,head=NULL,content=NULL WHERE `+
			where, t.TimeNow(), toDel.DelId, args)
		_, err = tx.Exec(ctx, query, newargs...)
		if err != nil {
			return err
		}
	}

	// 现在记录日志。硬删除和软删除都需要。

	// 不需要预处理语句，因为驱动在首次使用时准备语句并缓存。
	forUser := common.DecodeUidString(toDel.DeletedFor)
	for _, rng := range toDel.SeqIdRanges {
		if rng.Hi == 0 {
			// Dellog 必须包含有效的 Low 和 *Hi*。
			rng.Hi = rng.Low + 1
		}

		if _, err = tx.Exec(ctx, "INSERT INTO dellog(topic,deletedfor,delid,low,hi) VALUES($1,$2,$3,$4,$5)",
			topic, forUser, toDel.DelId, rng.Low, rng.Hi); err != nil {
			break
		}
	}

	if err != nil {
		return err
	}

	if toDel.DelId > 0 {
		if _, err = tx.Exec(ctx, "UPDATE topics SET delid=$1 WHERE id=$2", toDel.DelId, topic); err != nil {
			return err
		}
		if forUser == 0 {
			_, err = tx.Exec(ctx, "UPDATE subscriptions SET delid=$1 WHERE topic=$2", toDel.DelId, topic)
		} else {
			_, err = tx.Exec(ctx, "UPDATE subscriptions SET delid=$1 WHERE topic=$2 AND user=$3", toDel.DelId, topic, toDel.DeletedFor)
		}
	}

	return err
}

// MessageDeleteList deletes 消息 in the given Topic with seqIds from the list.
func (a *adapter) MessageDeleteList(topic string, toDel *t.DelMessage) (err error) {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	if err = messageDeleteList(ctx, tx, topic, toDel); err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// deviceHasher 完成设备Hasher所需的内部处理。
func deviceHasher(deviceID string) string {
	// 生成自定义键作为设备 ID 的 64 位哈希，以确保
	// 键的长度可预测
	hasher := fnv.New64()
	hasher.Write([]byte(deviceID))
	return strconv.FormatUint(uint64(hasher.Sum64()), 16)
}

// 设备管理（用于推送通知）
func (a *adapter) DeviceUpsert(uid t.Uid, def *t.DeviceDef) error {
	hash := deviceHasher(def.DeviceId)

	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	// 确保设备 ID 的唯一性：删除该设备 ID 的所有记录
	_, err = tx.Exec(ctx, "DELETE FROM devices WHERE hash=$1", hash)
	if err != nil {
		return err
	}

	// Actually add/update DeviceId for the new 用户
	_, err = tx.Exec(ctx, "INSERT INTO devices(userid, hash, deviceId, platform, lastseen, lang) VALUES($1,$2,$3,$4,$5,$6)",
		store.DecodeUid(uid), hash, def.DeviceId, def.Platform, def.LastSeen, def.Lang)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// DeviceGetAll 完成设备GetAll所需的内部处理。
func (a *adapter) DeviceGetAll(uids ...t.Uid) (map[t.Uid][]t.DeviceDef, int, error) {
	unums := common.DecodeUidSlice(uids)

	query, unums := expandQuery("SELECT userid,deviceid,platform,lastseen,lang FROM devices WHERE userid IN (?)", unums)
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.Query(ctx, query, unums...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()

	var device struct {
		Userid   int64
		Deviceid string
		Platform string
		Lastseen time.Time
		Lang     string
	}

	result := make(map[t.Uid][]t.DeviceDef)
	count := 0
	for rows.Next() {
		if err = rows.Scan(&device.Userid, &device.Deviceid, &device.Platform, &device.Lastseen, &device.Lang); err != nil {
			break
		}
		common.AddDeviceToMap(result, device.Userid, device.Deviceid, device.Platform, device.Lastseen, device.Lang)
		count++
	}
	if err == nil {
		err = rows.Err()
	}

	return result, count, err
}

// deviceDelete 完成设备删除所需的内部处理。
func deviceDelete(ctx context.Context, tx pgx.Tx, uid t.Uid, deviceID string) error {
	var err error
	var res pgconn.CommandTag
	if deviceID == "" {
		res, err = tx.Exec(ctx, "DELETE FROM devices WHERE userid=$1", store.DecodeUid(uid))
	} else {
		res, err = tx.Exec(ctx, "DELETE FROM devices WHERE userid=$1 AND hash=$2", store.DecodeUid(uid), deviceHasher(deviceID))
	}

	if err == nil {
		if count := res.RowsAffected(); count == 0 {
			err = t.ErrNotFound
		}
	}

	return err
}

// DeviceDelete 完成设备删除所需的内部处理。
func (a *adapter) DeviceDelete(uid t.Uid, deviceID string) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	err = deviceDelete(ctx, tx, uid, deviceID)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// 凭据管理

// CredUpsert 添加或更新验证记录。返回 true 表示插入，false 表示更新。
// 1. 如果凭据已验证：
// 1.1 硬删除未确认的等效记录（如果存在）。
// 1.2 插入新记录。重复则报告错误。
// 2. 如果凭据未验证：
// 2.1 检查已验证的等效记录是否存在。若存在则报告错误。
// 2.2 软删除同一方法的所有未验证记录。
// 2.3 恢复已有凭据。成功则返回。
// 2.4 插入新凭据记录。
func (a *adapter) CredUpsert(cred *t.Credential) (bool, error) {
	var err error

	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	now := t.TimeNow()
	userId := common.DecodeUidString(cred.User)

	// 强制唯一性：如果凭据已确认，“method:value”必须唯一。
	// 如果凭据尚未确认，“userid:method:value”是唯一的。
	synth := cred.Method + ":" + cred.Value

	if !cred.Done {
		// 检查是否 this credential is already validated.
		var done bool
		err = tx.QueryRow(ctx, "SELECT done FROM credentials WHERE synthetic=$1", synth).Scan(&done)
		if err == nil {
			// 赋值 err 以确保事务关闭。
			err = t.ErrDuplicate
			return false, err
		}
		if err != pgx.ErrNoRows {
			return false, err
		}
		// 我们将插入新记录。
		synth = cred.User + ":" + synth

		// Adding new unvalidated credential. Deactivate all unvalidated records of this 用户 and method.
		_, err = tx.Exec(ctx, "UPDATE credentials SET deletedat=$1 WHERE userid=$2 AND method=$3 AND done=FALSE",
			now, userId, cred.Method)
		if err != nil {
			return false, err
		}
		// Assume that the record exists and try to update it: undelete, update timestamp and 响应 value.
		res, err := tx.Exec(ctx, "UPDATE credentials SET updatedat=$1,deletedat=NULL,resp=$2,done=FALSE WHERE synthetic=$3",
			cred.UpdatedAt, cred.Resp, synth)
		if err != nil {
			return false, err
		}
		// 如果记录已更新，则一切正常。
		if numrows := res.RowsAffected(); numrows > 0 {
			return false, tx.Commit(ctx)
		}
	} else {
		// 硬删除未确认的记录（如果存在）。
		_, err = tx.Exec(ctx, "DELETE FROM credentials WHERE synthetic=$1", cred.User+":"+synth)
		if err != nil {
			return false, err
		}
	}

	_, err = tx.Exec(ctx, "INSERT INTO credentials(createdat,updatedat,method,value,synthetic,userid,resp,done) "+
		"VALUES($1,$2,$3,$4,$5,$6,$7,$8)",
		cred.CreatedAt, cred.UpdatedAt, cred.Method, cred.Value, synth, userId, cred.Resp, cred.Done)
	if err != nil {
		if isDupe(err) {
			return true, t.ErrDuplicate
		}
		return true, err
	}
	return true, tx.Commit(ctx)
}

// credDel 删除给定用户的指定验证方法或所有方法。
// 1. 如果用户正在被删除，硬删除所有记录（method == ""）
// 2. 如果删除单个值：
// 2.1 如果已验证或没有验证尝试则删除它
// （否则可能被用来规避验证尝试次数限制）。
// 2.2 否则标记为软删除。
func credDel(ctx context.Context, tx pgx.Tx, uid t.Uid, method, value string) error {
	constraints := " WHERE userid=?"
	args := []any{store.DecodeUid(uid)}

	if method != "" {
		constraints += " AND method=?"
		args = append(args, method)

		if value != "" {
			constraints += " AND value=?"
			args = append(args, value)
		}
	}
	where, _ := expandQuery(constraints, args...)

	var err error
	var res pgconn.CommandTag
	if method == "" {
		// 情况 1
		res, err = tx.Exec(ctx, "DELETE FROM credentials"+where, args...)
		if err == nil {
			if count := res.RowsAffected(); count == 0 {
				err = t.ErrNotFound
			}
		}
		return err
	}

	// 情况 2.1
	res, err = tx.Exec(ctx, "DELETE FROM credentials"+where+" AND (done=TRUE OR retries=0)", args...)
	if err != nil {
		return err
	}
	if count := res.RowsAffected(); count > 0 {
		return nil
	}

	// 情况 2.2
	query, args := expandQuery("UPDATE credentials SET deletedat=?"+constraints, t.TimeNow(), args)
	res, err = tx.Exec(ctx, query, args...)
	if err == nil {
		if count := res.RowsAffected(); count >= 0 {
			err = t.ErrNotFound
		}
	}

	return err
}

// CredDel 删除给定用户的凭据。如果方法为空，所有
// 凭据被移除。如果值为空，给定方法的所有凭据被移除。
func (a *adapter) CredDel(uid t.Uid, method, value string) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	err = credDel(ctx, tx, uid, method, value)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// CredConfirm 将指定的凭据方法标记为已确认。
func (a *adapter) CredConfirm(uid t.Uid, method string) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	res, err := a.db.Exec(
		ctx,
		"UPDATE credentials SET updatedat=$1,done=TRUE,synthetic=CONCAT(method,':',value) "+
			"WHERE userid=$2 AND method=$3 AND deletedat IS NULL AND done=FALSE",
		t.TimeNow(), store.DecodeUid(uid), method)
	if err != nil {
		if isDupe(err) {
			return t.ErrDuplicate
		}
		return err
	}
	if numrows := res.RowsAffected(); numrows < 1 {
		return t.ErrNotFound
	}
	return nil
}

// CredFail 增加指定验证方法的失败计数。
func (a *adapter) CredFail(uid t.Uid, method string) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.Exec(ctx, "UPDATE credentials SET updatedat=$1,retries=retries+1 WHERE userid=$2 AND method=$3 AND done=FALSE",
		t.TimeNow(), store.DecodeUid(uid), method)
	return err
}

// CredGetActive returns currently active unvalidated credential of the given 用户 and method.
func (a *adapter) CredGetActive(uid t.Uid, method string) (*t.Credential, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var cred t.Credential

	err := a.db.QueryRow(ctx, "SELECT createdat,updatedat,method,value,resp,done,retries "+
		"FROM credentials WHERE userid=$1 AND deletedat IS NULL AND method=$2 AND done=FALSE",
		store.DecodeUid(uid), method).Scan(&cred.CreatedAt, &cred.UpdatedAt, &cred.Method, &cred.Value, &cred.Resp, &cred.Done, &cred.Retries)
	if err != nil {
		if err == pgx.ErrNoRows {
			err = nil
		}
		return nil, err
	}
	cred.User = uid.String()

	return &cred, nil
}

// CredGetAll returns credential records for the given 用户 and method, all or validated only.
func (a *adapter) CredGetAll(uid t.Uid, method string, validatedOnly bool) ([]t.Credential, error) {
	query := "SELECT createdat,updatedat,method,value,resp,done,retries FROM credentials WHERE userid=$1 AND deletedat IS NULL"
	args := []any{store.DecodeUid(uid)}
	if method != "" {
		query += " AND method=$2"
		args = append(args, method)
	}
	if validatedOnly {
		query += " AND done=TRUE"
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var credentials []t.Credential
	rows, err := a.db.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var cred t.Credential
		if err = rows.Scan(&cred.CreatedAt, &cred.UpdatedAt, &cred.Method, &cred.Value, &cred.Resp, &cred.Done, &cred.Retries); err != nil {
			credentials = nil
			break
		}

		credentials = append(credentials, cred)
	}

	user := uid.String()
	for i := range credentials {
		credentials[i].User = user
	}

	return credentials, err
}

// 文件上传

// FileStartUpload 初始化文件上传
func (a *adapter) FileStartUpload(fd *t.FileDef) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var user any
	if fd.User != "" {
		user = store.DecodeUid(t.ParseUid(fd.User))
	}
	_, err := a.db.Exec(ctx,
		"INSERT INTO fileuploads(id,createdat,updatedat,userid,status,mimetype,size,etag,location) "+
			"VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)",
		store.DecodeUid(fd.Uid()), fd.CreatedAt, fd.UpdatedAt, user,
		fd.Status, fd.MimeType, fd.Size, fd.ETag, fd.Location)
	return err
}

// FileFinishUpload 标记文件上传完成，无论成功与否
func (a *adapter) FileFinishUpload(fd *t.FileDef, success bool, size int64) (*t.FileDef, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	now := t.TimeNow()
	if success {
		_, err = tx.Exec(ctx, "UPDATE fileuploads SET updatedat=$1,status=$2,size=$3,etag=$4,location=$5 WHERE id=$6",
			now, t.UploadCompleted, size, fd.ETag, fd.Location, store.DecodeUid(fd.Uid()))
		if err != nil {
			return nil, err
		}

		fd.Status = t.UploadCompleted
		fd.Size = size
	} else {
		// 删除记录：保留在数据库中没有意义。
		_, err = tx.Exec(ctx, "DELETE FROM fileuploads WHERE id=$1", store.DecodeUid(fd.Uid()))
		if err != nil {
			return nil, err
		}

		fd.Status = t.UploadFailed
		fd.Size = 0
	}
	fd.UpdatedAt = now

	return fd, tx.Commit(ctx)
}

// FileGet 获取指定文件的记录
func (a *adapter) FileGet(fid string) (*t.FileDef, error) {
	id := t.ParseUid(fid)
	if id.IsZero() {
		return nil, t.ErrMalformed
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var fd t.FileDef
	var ID int64
	var userId int64
	err := a.db.QueryRow(ctx, "SELECT id,createdat,updatedat,userid AS user,status,mimetype,size,etag,location "+
		"FROM fileuploads WHERE id=$1", store.DecodeUid(id)).Scan(&ID, &fd.CreatedAt, &fd.UpdatedAt, &userId, &fd.Status,
		&fd.MimeType, &fd.Size, &fd.ETag, &fd.Location)
	if err == pgx.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	fd.Id = common.EncodeUidString(fd.Id).String()
	fd.User = store.EncodeUid(userId).String()

	return &fd, nil
}

// FileDeleteUnused 删除文件上传记录。
func (a *adapter) FileDeleteUnused(olderThan time.Time, limit int) ([]string, error) {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	// Garbage collecting entries which as either marked as deleted, or lack 消息 references, or have no 用户 assigned.
	query := "SELECT fu.id,fu.location FROM fileuploads AS fu LEFT JOIN filemsglinks AS fml ON fml.fileid=fu.id " +
		"LEFT JOIN scheduledfilelinks AS sfl ON sfl.fileid=fu.id WHERE fml.id IS NULL AND sfl.id IS NULL"
	var args []any
	if !olderThan.IsZero() {
		query += " AND fu.updatedat<?"
		args = append(args, olderThan)
	}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}
	query, _ = expandQuery(query, args...)

	rows, err := tx.Query(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var locations []string
	var ids []any
	for rows.Next() {
		var id int
		var loc string
		if err = rows.Scan(&id, &loc); err != nil {
			break
		}
		if loc != "" {
			locations = append(locations, loc)
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}

	if err != nil {
		return nil, err
	}

	if len(ids) > 0 {
		query, ids = expandQuery("DELETE FROM fileuploads WHERE id IN (?)", ids)
		_, err = tx.Exec(ctx, query, ids...)
		if err != nil {
			return nil, err
		}
	}

	return locations, tx.Commit(ctx)
}

// FileLinkAttachments connects given Topic or 消息 to the file record IDs from the list.
func (a *adapter) FileLinkAttachments(topic string, userId, msgId t.Uid, fids []string) error {
	if (topic == "" && msgId.IsZero() && userId.IsZero()) ||
		(len(fids) == 0 && msgId.IsZero()) {
		return t.ErrMalformed
	}
	now := t.TimeNow()

	var args []any
	var linkId any
	var linkBy string
	if !msgId.IsZero() {
		linkBy = "msgid"
		linkId = int64(msgId)
	} else if topic != "" {
		linkBy = "topic"
		linkId = topic
		// 目前每个 Topic 只允许一个附件。
		fids = fids[0:1]
	} else {
		linkBy = "userid"
		linkId = store.DecodeUid(userId)
		// Only one attachment per 用户 is permitted at this time.
		fids = fids[0:1]
	}

	// 已解码的 id
	var dids []any
	for _, fid := range fids {
		id := t.ParseUid(fid)
		if id.IsZero() {
			return t.ErrMalformed
		}
		dids = append(dids, store.DecodeUid(id))
	}

	for _, id := range dids {
		// 创建时间,文件ID,[消息ID|Topic|用户ID]
		args = append(args, now, id, linkId)
	}

	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback(ctx)
		}
	}()

	// Messages are editable too: replace their links instead of accumulating stale files.
	sql := "DELETE FROM filemsglinks WHERE " + linkBy + "=$1"
	_, err = tx.Exec(ctx, sql, linkId)
	if err != nil {
		return err
	}
	if len(dids) == 0 {
		return tx.Commit(ctx)
	}

	query, args := expandQuery("INSERT INTO filemsglinks(createdat,fileid,"+linkBy+") VALUES (?,?,?)"+
		strings.Repeat(",(?,?,?)", len(dids)-1), args...)
	_, err = tx.Exec(ctx, query, args...)
	if err != nil {
		return err
	}

	return tx.Commit(ctx)
}

// FileLinkScheduled 建立定时消息与待投递文件的关联，防止文件提前回收。
func (a *adapter) FileLinkScheduled(scheduledId t.Uid, fids []string) error {
	if scheduledId.IsZero() || len(fids) == 0 {
		return t.ErrMalformed
	}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	for _, fid := range fids {
		fileID := t.ParseUid(fid)
		if fileID.IsZero() {
			return t.ErrMalformed
		}
		if _, err := a.db.Exec(ctx,
			`INSERT INTO scheduledfilelinks(scheduledid,fileid) VALUES($1,$2) ON CONFLICT DO NOTHING`,
			store.DecodeUid(scheduledId), store.DecodeUid(fileID)); err != nil {
			return err
		}
	}
	return nil
}

// PCacheGet 完成P缓存Get所需的内部处理。
func (a *adapter) PCacheGet(key string) (string, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	var value string
	if err := a.db.QueryRow(ctx, `SELECT "value" FROM kvmeta WHERE "key"=$1 LIMIT 1`, key).Scan(&value); err != nil {
		if err == pgx.ErrNoRows {
			return "", t.ErrNotFound
		}
		return "", err
	}
	return value, nil
}

// PCacheUpsert 创建或更新持久缓存条目。
func (a *adapter) PCacheUpsert(key string, value string, failOnDuplicate bool) error {
	if strings.Contains(key, "%") {
		// 不允许键中包含 %：它会干扰 LIKE 查询。
		return t.ErrMalformed
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	var action string
	if !failOnDuplicate {
		action = ` ON CONFLICT ("key") DO UPDATE SET createdat=$2,"value"=$3`
	}

	_, err := a.db.Exec(ctx, `INSERT INTO kvmeta("key",createdat,"value") VALUES($1,$2,$3)`+action,
		key, t.TimeNow(), value)
	if isDupe(err) {
		return t.ErrDuplicate
	}
	return err
}

// PCacheDelete 删除一条持久缓存条目。
func (a *adapter) PCacheDelete(key string) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	_, err := a.db.Exec(ctx, `DELETE FROM kvmeta WHERE "key"=$1`, key)
	return err
}

// PCacheExpire 使具有给定键前缀的旧条目过期。
func (a *adapter) PCacheExpire(keyPrefix string, olderThan time.Time) error {
	if keyPrefix == "" {
		return t.ErrMalformed
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	_, err := a.db.Exec(ctx, `DELETE FROM kvmeta WHERE "key" LIKE $1 AND createdat<$2`, keyPrefix+"%", olderThan)
	return err
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
