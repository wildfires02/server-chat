//go:build mysql
// +build mysql

// Package mysql 是 MySQL 的数据库适配器。
package mysql

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"

	ms "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
	"chat/server/auth"
	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"
)

// adapter 保存 MySQL 连接数据。
type adapter struct {
	db     *sqlx.DB
	dsn    string
	dbName string
	// 最大返回记录数
	maxResults int
	// Maximum number of 消息 records to return
	maxMessageResults int
	version           int

	// 单次查询超时。
	sqlTimeout time.Duration
	// 数据库事务超时。
	txTimeout time.Duration
}

const (
	adpVersion  = 116
	adapterName = "mysql"

	defaultDSN      = "root:@tcp(localhost:3306)/im?parseTime=true"
	defaultDatabase = "im"

	defaultMaxResults = 1024
	// 此值受 Session 发送队列上限 (128) 限制。
	defaultMaxMessageResults = 100

	// 如果指定了数据库请求超时，
	// 事务将分配 txTimeoutMultiplier 倍的时间。
	txTimeoutMultiplier = 1.5
)

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

func (a *adapter) getContext() (context.Context, context.CancelFunc) {
	if a.sqlTimeout > 0 {
		return context.WithTimeout(context.Background(), a.sqlTimeout)
	}
	return context.Background(), nil
}

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

// CreateDb 初始化存储。
func (a *adapter) CreateDb(reset bool) error {
	var err error
	var tx *sql.Tx

	// 不能使用现有连接，因为它配置了可能不存在的数据库名。
	// 不干净关闭也没关系。
	a.db.Close()

	// 此 DSN 之前已解析且无错误，不再检查错误。
	cfg, _ := ms.ParseDSN(a.dsn)
	// 清除数据库名
	cfg.DBName = ""

	a.db, err = sqlx.Open("mysql", cfg.FormatDSN())
	if err != nil {
		return err
	}

	if tx, err = a.db.Begin(); err != nil {
		return err
	}

	defer func() {
		if err != nil {
			// 建表过程中 DDL 会触发 MySQL 隐式提交，事务 Rollback 无法回滚已创建的表。创建失败时显式删除数据库
			a.db.Exec("DROP DATABASE IF EXISTS " + a.dbName)
			tx.Rollback()
		}
	}()

	if reset {
		if _, err = tx.Exec("DROP DATABASE IF EXISTS " + a.dbName); err != nil {
			return err
		}
	}

	if _, err = tx.Exec("CREATE DATABASE " + a.dbName + " CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci"); err != nil {
		return err
	}

	if _, err = tx.Exec("USE " + a.dbName); err != nil {
		return err
	}

	if _, err = tx.Exec(
		`CREATE TABLE users(
			id        BIGINT NOT NULL,
			createdat DATETIME(3) NOT NULL,
			updatedat DATETIME(3) NOT NULL,
			state     SMALLINT NOT NULL DEFAULT 0,
			stateat   DATETIME(3),
			access    JSON,
			lastseen  DATETIME,
			useragent VARCHAR(255) DEFAULT '',
			public    JSON,
			trusted   JSON,
			tags      JSON,
			PRIMARY KEY(id),
			INDEX users_state_stateat(state, stateat),
			INDEX users_lastseen_updatedat(lastseen, updatedat)
		)`); err != nil {
		return err
	}

	// 已索引的用户标签。
	if _, err = tx.Exec(
		`CREATE TABLE usertags(
			id     INT NOT NULL AUTO_INCREMENT,
			userid BIGINT NOT NULL,
			tag    VARCHAR(96) NOT NULL,
			PRIMARY KEY(id),
			FOREIGN KEY(userid) REFERENCES users(id),
			INDEX usertags_tag(tag),
			UNIQUE INDEX usertags_userid_tag(userid, tag)
		)`); err != nil {
		return err
	}

	// 已索引的设备。归一化到单独的表中。
	if _, err = tx.Exec(
		`CREATE TABLE devices(
			id       INT NOT NULL AUTO_INCREMENT,
			userid   BIGINT NOT NULL,
			hash     CHAR(16) NOT NULL,
			deviceid TEXT NOT NULL,
			platform VARCHAR(32),
			lastseen DATETIME NOT NULL,
			lang     VARCHAR(8),
			PRIMARY KEY(id),
			FOREIGN KEY(userid) REFERENCES users(id),
			UNIQUE INDEX devices_hash(hash)
		)`); err != nil {
		return err
	}

	// 基础认证方案的认证记录。
	if _, err = tx.Exec(
		`CREATE TABLE auth(
			id      INT NOT NULL AUTO_INCREMENT,
			uname   VARCHAR(32) NOT NULL,
			userid  BIGINT NOT NULL,
			scheme  VARCHAR(16) NOT NULL,
			authlvl INT NOT NULL,
			secret  VARCHAR(255) NOT NULL,
			expires DATETIME,
			PRIMARY KEY(id),
			FOREIGN KEY(userid) REFERENCES users(id),
			UNIQUE INDEX auth_userid_scheme(userid, scheme),
			UNIQUE INDEX auth_uname(uname)
		)`); err != nil {
		return err
	}

	// Topic 管理
	if _, err = tx.Exec(
		`CREATE TABLE topics(
			id        INT NOT NULL AUTO_INCREMENT,
			createdat DATETIME(3) NOT NULL,
			updatedat DATETIME(3) NOT NULL,
			state     SMALLINT NOT NULL DEFAULT 0,
			stateat   DATETIME(3),
			touchedat DATETIME(3),
			name      CHAR(25) NOT NULL,
			usebt     TINYINT DEFAULT 0,
			owner     BIGINT NOT NULL DEFAULT 0,
			access    JSON,
			seqid     INT NOT NULL DEFAULT 0,
			delid     INT DEFAULT 0,
			subcnt		INT DEFAULT 0,
			public    JSON,
			trusted   JSON,
			tags      JSON,
			aux       JSON,
			PRIMARY KEY(id),
			UNIQUE INDEX topics_name(name),
			INDEX topics_owner(owner),
			INDEX topics_state_stateat(state, stateat),
			INDEX topics_name_state_seqid(name, state, seqid)
		)`); err != nil {
		return err
	}

	// 创建系统 Topic 'sys'。
	if err = createSystemTopic(tx); err != nil {
		return err
	}

	// 已索引的 Topic 标签。
	if _, err = tx.Exec(
		`CREATE TABLE topictags(
			id    INT NOT NULL AUTO_INCREMENT,
			topic CHAR(25) NOT NULL,
			tag   VARCHAR(96) NOT NULL,
			PRIMARY KEY(id),
			FOREIGN KEY(topic) REFERENCES topics(name),
			INDEX topictags_tag(tag),
			UNIQUE INDEX topictags_topic_tag(topic, tag)
		)`); err != nil {
		return err
	}

	// 订阅
	if _, err = tx.Exec(
		`CREATE TABLE subscriptions(
			id        INT NOT NULL AUTO_INCREMENT,
			createdat DATETIME(3) NOT NULL,
			updatedat DATETIME(3) NOT NULL,
			deletedat DATETIME(3),
			userid    BIGINT NOT NULL,
			topic     CHAR(25) NOT NULL,
			delid     INT DEFAULT 0,
			recvseqid INT DEFAULT 0,
			readseqid INT DEFAULT 0,
			modewant  CHAR(8),
			modegiven CHAR(8),
			private   JSON,
			PRIMARY KEY(id),
			FOREIGN KEY(userid) REFERENCES users(id),
			UNIQUE INDEX subscriptions_topic_userid(topic, userid),
			INDEX subscriptions_topic(topic),
			INDEX subscriptions_deletedat(deletedat),
			INDEX subscriptions_userid_topic_deletedat(userid, topic, deletedat)
		)`); err != nil {
		return err
	}

	// 消息
	if _, err = tx.Exec(
		`CREATE TABLE messages(
			id        INT NOT NULL AUTO_INCREMENT,
			createdat DATETIME(3) NOT NULL,
			updatedat DATETIME(3) NOT NULL,
			deletedat DATETIME(3),
			delid     INT DEFAULT 0,
			seqid     INT NOT NULL,
			topic     CHAR(25) NOT NULL,` +
			"`from`   BIGINT NOT NULL," +
			`head     JSON,
			content   JSON,
			PRIMARY KEY(id),
			FOREIGN KEY(topic) REFERENCES topics(name),
			UNIQUE INDEX messages_topic_seqid(topic, seqid)
		)`); err != nil {
		return err
	}

	// 删除日志
	if _, err = tx.Exec(
		`CREATE TABLE dellog(
			id         INT NOT NULL AUTO_INCREMENT,
			topic      CHAR(25) NOT NULL,
			deletedfor BIGINT NOT NULL DEFAULT 0,
			delid      INT NOT NULL,
			low        INT NOT NULL,
			hi         INT NOT NULL,
			PRIMARY KEY(id),
			FOREIGN KEY(topic) REFERENCES topics(name),
			INDEX dellog_topic_delid_deletedfor(topic,delid,deletedfor),
			INDEX dellog_topic_deletedfor_low_hi(topic,deletedfor,low,hi),
			INDEX dellog_deletedfor(deletedfor)
		)`); err != nil {
		return err
	}

	// 用户 credentials
	if _, err = tx.Exec(
		`CREATE TABLE credentials(
			id        INT NOT NULL AUTO_INCREMENT,
			createdat DATETIME(3) NOT NULL,
			updatedat DATETIME(3) NOT NULL,
			deletedat DATETIME(3),
			method    VARCHAR(16) NOT NULL,
			value     VARCHAR(128) NOT NULL,
			synthetic VARCHAR(192) NOT NULL,
			userid    BIGINT NOT NULL,
			resp      VARCHAR(255),
			done      TINYINT NOT NULL DEFAULT 0,
			retries   INT NOT NULL DEFAULT 0,
			PRIMARY KEY(id),
			FOREIGN KEY(userid) REFERENCES users(id),
			UNIQUE credentials_uniqueness(synthetic)
		)`); err != nil {
		return err
	}

	// 上传文件的记录。
	// 不要在 userid 上添加外键。不需要，而且会破坏用户删除。
	if _, err = tx.Exec(
		`CREATE TABLE fileuploads(
			id        BIGINT NOT NULL,
			createdat DATETIME(3) NOT NULL,
			updatedat DATETIME(3) NOT NULL,
			userid    BIGINT,
			status    INT NOT NULL,
			mimetype  VARCHAR(255) NOT NULL,
			size      BIGINT NOT NULL,
			etag      VARCHAR(128),
			location  VARCHAR(2048) NOT NULL,
			PRIMARY KEY(id),
			INDEX fileuploads_status(status)
		)`); err != nil {
		return err
	}

	// 上传文件与所附加的 Topic、用户或消息之间的链接。
	if _, err = tx.Exec(
		`CREATE TABLE filemsglinks(
			id        INT NOT NULL AUTO_INCREMENT,
			createdat DATETIME(3) NOT NULL,
			fileid    BIGINT NOT NULL,
			msgid     INT,
			topic     CHAR(25),
			userid    BIGINT,
			PRIMARY KEY(id),
			FOREIGN KEY(fileid) REFERENCES fileuploads(id) ON DELETE CASCADE,
			FOREIGN KEY(msgid) REFERENCES messages(id) ON DELETE CASCADE,
			FOREIGN KEY(topic) REFERENCES topics(name) ON DELETE CASCADE,
			FOREIGN KEY(userid) REFERENCES users(id) ON DELETE CASCADE
		)`); err != nil {
		return err
	}

	if _, err = tx.Exec(
		`CREATE TABLE kvmeta(` +
			"`key`       VARCHAR(64) NOT NULL," +
			"createdat   DATETIME(3)," +
			"`value`     TEXT," +
			"PRIMARY KEY(`key`)," +
			"INDEX kvmeta_createdat_key(createdat, `key`)" +
			`)`); err != nil {
		return err
	}
	if _, err = tx.Exec("INSERT INTO kvmeta(`key`, `value`) VALUES('version',?)", adpVersion); err != nil {
		return err
	}

	return tx.Commit()
}

// UpgradeDb 升级数据库（如需要）。
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

	if a.version == 106 {
		// 执行数据库从版本 106 升级到 107。

		if _, err := a.db.Exec("CREATE UNIQUE INDEX usertags_userid_tag ON usertags(userid, tag)"); err != nil {
			return err
		}

		if _, err := a.db.Exec("CREATE UNIQUE INDEX topictags_topic_tag ON topictags(topic, tag)"); err != nil {
			return err
		}

		if _, err := a.db.Exec("ALTER TABLE credentials ADD deletedat DATETIME(3) AFTER updatedat"); err != nil {
			return err
		}

		if err := bumpVersion(a, 107); err != nil {
			return err
		}
	}

	if a.version == 107 {
		// 执行数据库从版本 107 升级到 108。

		// 将默认用户访问权限从 JRWPA 替换为 JRWPAS。
		if _, err := a.db.Exec(`UPDATE users SET access=JSON_REPLACE(access, '$.Auth', 'JRWPAS')
			WHERE CAST(JSON_EXTRACT(access, '$.Auth') AS CHAR) LIKE '"JRWPA"'`); err != nil {
			return err
		}

		if err := bumpVersion(a, 108); err != nil {
			return err
		}
	}

	if a.version == 108 {
		// 执行数据库从版本 108 升级到 109。

		tx, err := a.db.Begin()
		if err != nil {
			return err
		}
		if err = createSystemTopic(tx); err != nil {
			tx.Rollback()
			return err
		}
		if err = tx.Commit(); err != nil {
			return err
		}

		if err = bumpVersion(a, 109); err != nil {
			return err
		}
	}

	if a.version == 109 {
		// 执行数据库从版本 109 升级到 110。
		if _, err := a.db.Exec("UPDATE topics SET touchedat=updatedat WHERE touchedat IS NULL"); err != nil {
			return err
		}

		if err := bumpVersion(a, 110); err != nil {
			return err
		}
	}

	if a.version == 110 {
		// 用户
		if _, err := a.db.Exec("ALTER TABLE users MODIFY state SMALLINT NOT NULL DEFAULT 0 AFTER updatedat"); err != nil {
			return err
		}

		if _, err := a.db.Exec("ALTER TABLE users CHANGE deletedat stateat DATETIME(3)"); err != nil {
			return err
		}

		if _, err := a.db.Exec("ALTER TABLE users DROP INDEX users_deletedat"); err != nil {
			return err
		}

		// 添加状态到以前软删除的用户。
		if _, err := a.db.Exec("UPDATE users SET state=? WHERE stateat IS NOT NULL", t.StateDeleted); err != nil {
			return err
		}

		if _, err := a.db.Exec("ALTER TABLE users ADD INDEX users_state(state)"); err != nil {
			return err
		}

		// Topic 管理
		if _, err := a.db.Exec("ALTER TABLE topics ADD state SMALLINT NOT NULL DEFAULT 0 AFTER updatedat"); err != nil {
			return err
		}

		if _, err := a.db.Exec("ALTER TABLE topics CHANGE deletedat stateat DATETIME(3)"); err != nil {
			return err
		}

		// 添加状态到以前软删除的 Topic。
		if _, err := a.db.Exec("UPDATE topics SET state=? WHERE stateat IS NOT NULL", t.StateDeleted); err != nil {
			return err
		}

		if _, err := a.db.Exec("ALTER TABLE topics ADD INDEX topics_state(state)"); err != nil {
			return err
		}

		// 订阅
		if _, err := a.db.Exec("ALTER TABLE subscriptions ADD INDEX topics_deletedat(deletedat)"); err != nil {
			return err
		}

		if err := bumpVersion(a, 111); err != nil {
			return err
		}
	}

	if a.version == 111 {
		// 执行数据库从版本 111 升级到 112。
		if _, err := a.db.Exec("ALTER TABLE users ADD trusted JSON AFTER public"); err != nil {
			return err
		}

		if _, err := a.db.Exec("ALTER TABLE topics ADD trusted JSON AFTER public"); err != nil {
			return err
		}

		// 移除 NOT NULL 约束，以便在注册时完成头像上传。
		if _, err := a.db.Exec("ALTER TABLE fileuploads MODIFY userid BIGINT"); err != nil {
			return err
		}

		if _, err := a.db.Exec("ALTER TABLE fileuploads ADD INDEX fileuploads_status(status)"); err != nil {
			return err
		}

		// 移除 NOT NULL 约束以启用到用户和 Topic 的链接。
		if _, err := a.db.Exec("ALTER TABLE filemsglinks MODIFY msgid INT"); err != nil {
			return err
		}

		if _, err := a.db.Exec("ALTER TABLE filemsglinks ADD topic CHAR(25)"); err != nil {
			return err
		}

		if _, err := a.db.Exec("ALTER TABLE filemsglinks ADD userid BIGINT"); err != nil {
			return err
		}

		if _, err := a.db.Exec("ALTER TABLE filemsglinks ADD FOREIGN KEY(topic) REFERENCES topics(name) ON DELETE CASCADE"); err != nil {
			return err
		}

		if _, err := a.db.Exec("ALTER TABLE filemsglinks ADD FOREIGN KEY(userid) REFERENCES users(id) ON DELETE CASCADE"); err != nil {
			return err
		}

		if err := bumpVersion(a, 112); err != nil {
			return err
		}
	}

	if a.version == 112 {
		// 执行数据库从版本 112 升级到 113。

		// 删除未验证账户的索引。
		if _, err := a.db.Exec("ALTER TABLE users ADD INDEX users_lastseen_updatedat(lastseen,updatedat)"); err != nil {
			return err
		}

		// 为 kvmeta 添加时间戳。
		if _, err := a.db.Exec("ALTER TABLE kvmeta MODIFY `key` VARCHAR(64) NOT NULL"); err != nil {
			return err
		}

		// 为 kvmeta 添加时间戳。
		if _, err := a.db.Exec("ALTER TABLE kvmeta ADD createdat DATETIME(3) AFTER `key`"); err != nil {
			return err
		}

		// 在新字段和键上添加复合索引（可按键前缀搜索）。
		if _, err := a.db.Exec("ALTER TABLE kvmeta ADD INDEX kvmeta_createdat_key(createdat, `key`)"); err != nil {
			return err
		}

		if err := bumpVersion(a, 113); err != nil {
			return err
		}
	}

	if a.version == 113 {
		// 执行数据库从版本 113 升级到 114。

		if _, err := a.db.Exec("ALTER TABLE topics ADD aux JSON"); err != nil {
			return err
		}

		if _, err := a.db.Exec("ALTER TABLE fileuploads ADD etag VARCHAR(128) AFTER size"); err != nil {
			return err
		}

		if err := bumpVersion(a, 114); err != nil {
			return err
		}
	}

	if a.version == 114 {
		// 执行数据库从版本 114 升级到 115。

		// 高效查找给定用户的相关订阅，并使用连接键。
		if _, err := a.db.Exec("CREATE INDEX idx_subs_user_topic_del ON subscriptions(userid, topic, deletedat)"); err != nil {
			return err
		}

		// 优化连接；状态过滤；seqid 支持 SUM 操作。
		if _, err := a.db.Exec("CREATE INDEX idx_topics_name_state_seqid ON topics(name, state, seqid)"); err != nil {
			return err
		}

		if err := bumpVersion(a, 115); err != nil {
			return err
		}
	}

	if a.version == 115 {
		// 执行数据库从版本 115 升级到 116。

		// 为 Topic 表添加订阅者计数字段。
		if _, err := a.db.Exec("ALTER TABLE topics ADD COLUMN subcnt INT DEFAULT 0 AFTER delid"); err != nil {
			return err
		}

		if err := bumpVersion(a, 116); err != nil {
			return err
		}
	}

	if a.version != adpVersion {
		return errors.New("Failed to perform database upgrade to version " + strconv.Itoa(adpVersion) +
			". DB is still at " + strconv.Itoa(a.version))
	}
	return nil
}

// 创建系统 Topic 'sys'。
func createSystemTopic(tx *sql.Tx) error {
	now := t.TimeNow()
	query := `INSERT INTO topics(createdat,updatedat,state,touchedat,name,access,public)
				VALUES(?,?,?,?,'sys','{"Auth": "N","Anon": "N"}','{"fn": "System"}')`
	_, err := tx.Exec(query, now, now, t.StateOK, now)
	return err
}

func addTags(tx *sqlx.Tx, table, keyName string, keyVal any, tags []string, ignoreDups bool) error {
	if len(tags) == 0 {
		return nil
	}

	insert, err := tx.Prepare("INSERT INTO " + table + "(" + keyName + ",tag) VALUES(?,?)")
	if err != nil {
		return err
	}

	for _, tag := range tags {
		if _, err = insert.Exec(keyVal, tag); err != nil {
			if isDupe(err) {
				if ignoreDups {
					err = nil
					continue
				}
				return t.ErrDuplicate
			}
			return err
		}
	}
	return nil
}

func removeTags(tx *sqlx.Tx, table, keyName string, keyVal any, tags []string) error {
	if len(tags) == 0 {
		return nil
	}

	var args []any
	for _, tag := range tags {
		args = append(args, tag)
	}

	query, args, _ := sqlx.In("DELETE FROM "+table+" WHERE "+keyName+"=? AND tag IN (?)", keyVal, args)
	_, err := tx.Exec(tx.Rebind(query), args...)

	return err
}

// UserCreate 创建新用户。如果错误是由于用户名重复导致的，返回错误和 true，
// 其他错误返回 false
func (a *adapter) UserCreate(user *t.User) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	decoded_uid := store.DecodeUid(user.Uid())
	if _, err = tx.Exec("INSERT INTO users(id,createdat,updatedat,state,access,public,trusted,tags) VALUES(?,?,?,?,?,?,?,?)",
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

	// 保存用户的标签到单独的表以便用户可以被搜索到。
	if err = addTags(tx, "usertags", "userid", decoded_uid, user.Tags, false); err != nil {
		return err
	}

	return tx.Commit()
}

// 添加用户的认证记录
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

	if _, err := a.db.ExecContext(ctx, "INSERT INTO auth(uname,userid,scheme,authLvl,secret,expires) VALUES(?,?,?,?,?,?)",
		unique, store.DecodeUid(uid), scheme, authLvl, secret, exp); err != nil {
		if isDupe(err) {
			return t.ErrDuplicate
		}
		return err
	}
	return nil
}

// AuthDelScheme 删除用户的现有认证方案。
func (a *adapter) AuthDelScheme(user t.Uid, scheme string) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.ExecContext(ctx, "DELETE FROM auth WHERE userid=? AND scheme=?", store.DecodeUid(user), scheme)
	return err
}

// AuthDelAllRecords 删除用户的所有认证记录。
func (a *adapter) AuthDelAllRecords(user t.Uid) (int, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	res, err := a.db.ExecContext(ctx, "DELETE FROM auth WHERE userid=?", store.DecodeUid(user))
	if err != nil {
		return 0, err
	}
	count, _ := res.RowsAffected()

	return int(count), nil
}

// 更新用户的认证唯一值、密钥、认证级别。
func (a *adapter) AuthUpdRecord(uid t.Uid, scheme, unique string, authLvl auth.Level,
	secret []byte, expires time.Time) error {

	params := []string{"authLvl=?"}
	args := []any{authLvl}

	if unique != "" {
		params = append(params, "uname=?")
		args = append(args, unique)
	}
	if len(secret) > 0 {
		params = append(params, "secret=?")
		args = append(args, secret)
	}
	if !expires.IsZero() {
		params = append(params, "expires=?")
		args = append(args, expires)
	}
	args = append(args, store.DecodeUid(uid), scheme)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	sql := "UPDATE auth SET " + strings.Join(params, ",") + " WHERE userid=? AND scheme=?"
	resp, err := a.db.ExecContext(ctx, sql, args...)
	if isDupe(err) {
		return t.ErrDuplicate
	}

	if count, _ := resp.RowsAffected(); count <= 0 {
		return t.ErrNotFound
	}

	return err
}

// 获取用户的认证记录
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
	if err := a.db.GetContext(ctx, &record, "SELECT uname,secret,expires,authlvl FROM auth WHERE userid=? AND scheme=?",
		store.DecodeUid(uid), scheme); err != nil {
		if err == sql.ErrNoRows {
			// 未找到 - 使用标准错误。
			err = t.ErrNotFound
		}
		return "", 0, nil, expires, err
	}

	if record.Expires != nil {
		expires = *record.Expires
	}

	return record.Uname, record.Authlvl, record.Secret, expires, nil
}

// 获取用户的认证记录
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
	if err := a.db.GetContext(ctx, &record, "SELECT userid,secret,expires,authlvl FROM auth WHERE uname=?", unique); err != nil {
		if err == sql.ErrNoRows {
			// 未找到 - 清除错误
			err = nil
		}
		return t.ZeroUid, 0, nil, expires, err
	}

	if record.Expires != nil {
		expires = *record.Expires
	}

	return store.EncodeUid(record.Userid), record.Authlvl, record.Secret, expires, nil
}

// UserGet 按用户 ID 获取单个用户。如果用户不存在返回 (nil, nil)
func (a *adapter) UserGet(uid t.Uid) (*t.User, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var user t.User
	err := a.db.GetContext(ctx, &user, "SELECT * FROM users WHERE id=? AND state!=?", store.DecodeUid(uid), t.StateDeleted)
	if err == nil {
		user.SetUid(uid)
		user.Public = common.FromJSON(user.Public)
		user.Trusted = common.FromJSON(user.Trusted)
		return &user, nil
	}

	if err == sql.ErrNoRows {
		// 如果用户不存在或标记为软删除则清除错误。
		return nil, nil
	}

	return nil, err
}

func (a *adapter) UserGetAll(ids ...t.Uid) ([]t.User, error) {
	uids := make([]any, len(ids))
	for i, id := range ids {
		if id.IsZero() {
			continue
		}
		uids[i] = store.DecodeUid(id)
	}

	users := []t.User{}
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	q, uids, _ := sqlx.In("SELECT * FROM users WHERE id IN (?) AND state!=?", uids, t.StateDeleted)
	rows, err := a.db.QueryxContext(ctx, a.db.Rebind(q), uids...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	for rows.Next() {
		var user t.User
		if err = rows.StructScan(&user); err != nil {
			users = nil
			break
		}
		user.SetUid(common.EncodeUidString(user.Id))
		user.Public = common.FromJSON(user.Public)
		user.Trusted = common.FromJSON(user.Trusted)

		users = append(users, user)
	}
	if err == nil {
		err = rows.Err()
	}

	return users, err
}

// UserDelete 删除指定用户：完全擦除（硬删除）或标记为已删除。
func (a *adapter) UserDelete(uid t.Uid, hard bool) error {
	decoded_uid := store.DecodeUid(uid)

	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// 检查用户是否存在以及是否已被软删除
	var state t.ObjState
	if err = tx.QueryRowContext(ctx, "SELECT state FROM users WHERE id=?", decoded_uid).Scan(&state); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return t.ErrNotFound
		}
		return err
	}
	if !hard && state == t.StateDeleted {
		return t.ErrNotFound
	}

	query := "SELECT name FROM topics WHERE owner=?"
	args := []any{decoded_uid}
	// 硬删除时，删除所有 Topic，包括之前软删除的。
	if !hard {
		query += " AND state!=?"
		args = append(args, t.StateDeleted)
	}
	// 获取用户拥有的 Topic 名称列表（'grp' 和 'chn' 格式）。
	ownTopics, err := a.topicNamesForUser(query, true, args...)
	if err != nil {
		return err
	}

	now := t.TimeNow()

	if hard {
		// 删除用户的设备
		// t.ErrNotFound = 用户没有设备。
		if err = deviceDelete(tx, uid, ""); err != nil && err != t.ErrNotFound {
			return err
		}

		// 删除用户在所有 Topic 中的订阅。
		if err = subsDelForUser(tx, decoded_uid, true); err != nil {
			return err
		}

		// 删除用户在所有 Topic 中软删除的消息记录。
		if _, err = tx.Exec("DELETE FROM dellog WHERE deletedfor=?", decoded_uid); err != nil {
			return err
		}

		// 无法删除用户在所有 Topic 中的消息，因为无法通知 Topic 此类删除。
		// 只需将消息保留并标记为由“未找到”用户发送。

			// 删除用户作为所有者的 Topic。
		if len(ownTopics) > 0 {
			// 首先删除这些 Topic 中的所有消息。
			if _, err = tx.Exec("DELETE dellog FROM dellog JOIN topics ON topics.name=dellog.topic WHERE topics.owner=?",
				decoded_uid); err != nil {
				return err
			}

			// 消息删除将级联到 filemsglinks，进而到 fileuploads。
			if _, err = tx.Exec("DELETE messages FROM messages JOIN topics ON topics.name=messages.topic WHERE topics.owner=?",
				decoded_uid); err != nil {
				return err
			}

			// 删除用户作为 Topic 所有者的所有用户的订阅。
			sql, args, _ := sqlx.In("DELETE FROM subscriptions AS s WHERE topic IN (?)", ownTopics)
			if _, err = tx.Exec(tx.Rebind(sql), args); err != nil {
				return err
			}

			// 删除 Topic 标签。
			if _, err = tx.Exec("DELETE tt FROM topictags AS tt JOIN topics AS t ON t.name=tt.topic WHERE t.owner=?",
				decoded_uid); err != nil {
				return err
			}

			// 最后删除 Topic。
			if _, err = tx.Exec("DELETE FROM topics WHERE owner=?", decoded_uid); err != nil {
				return err
			}
		}

		// 删除用户的认证记录。
		if _, err = tx.Exec("DELETE FROM auth WHERE userid=?", decoded_uid); err != nil {
			return err
		}

		// 删除所有凭据。
		if err = credDel(tx, uid, "", ""); err != nil && err != t.ErrNotFound {
			return err
		}

		if _, err = tx.Exec("DELETE FROM usertags WHERE userid=?", decoded_uid); err != nil {
			return err
		}

		if _, err = tx.Exec("DELETE FROM users WHERE id=?", decoded_uid); err != nil {
			return err
		}
	} else {
		// 禁用用户的所有订阅。包括 p2p 订阅。无需删除它们。
		if err = subsDelForUser(tx, decoded_uid, false); err != nil {
			return err
		}

		if len(ownTopics) > 0 {
			// 禁用用户作为所有者的 Topic 的所有订阅。
			sql, args, _ := sqlx.In("UPDATE subscriptions SET updatedat=?,deletedat=? WHERE topic IN (?)", now, now, ownTopics)
			if _, err = tx.Exec(tx.Rebind(sql), args...); err != nil {
				return err
			}
		}

		// 禁用用户作为所有者的群组 Topic。
		if _, err = tx.Exec("UPDATE topics SET updatedat=?,touchedat=?,state=?,stateat=? WHERE owner=?",
			now, now, t.StateDeleted, now, decoded_uid); err != nil {
			return err
		}

		// 禁用与该用户的 p2p Topic（p2p Topic 的所有者为 0）。
		if _, err = tx.Exec("UPDATE topics AS t JOIN subscriptions AS s ON t.name=s.topic "+
			"SET t.updatedat=?,t.touchedat=?,t.state=?,t.stateat=? "+
			"WHERE t.owner=0 AND s.userid=? AND t.name LIKE 'p2p%'",
			now, now, t.StateDeleted, now, decoded_uid); err != nil {
			return err
		}

		// 禁用其他用户对已禁用 p2p Topic 的订阅。
		if _, err = tx.Exec("UPDATE subscriptions AS s_one JOIN subscriptions AS s_two "+
			"ON s_one.topic=s_two.topic "+
			"SET s_two.updatedat=?, s_two.deletedat=? WHERE s_one.userid=? AND s_one.topic LIKE 'p2p%'",
			now, now, decoded_uid); err != nil {
			return err
		}

		// 最后禁用用户。
		if _, err = tx.Exec("UPDATE users SET updatedat=?,state=?,stateat=? WHERE id=?",
			now, t.StateDeleted, now, decoded_uid); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// topicStateForUser 由 UserUpdate 在更新包含状态变更时调用。
// 软删除的 Topic 保持软删除状态。
func (a *adapter) topicStateForUser(tx *sqlx.Tx, decoded_uid int64, now time.Time, update any) error {
	var err error

	state, ok := update.(t.ObjState)
	if !ok {
		return t.ErrMalformed
	}

	if now.IsZero() {
		now = t.TimeNow()
	}

	// 变更用户作为所有者的所有 Topic 的状态。
	if _, err = tx.Exec("UPDATE topics SET state=?, stateat=? WHERE owner=? AND state!=?",
		state, now, decoded_uid, t.StateDeleted); err != nil {
		return err
	}

	// 变更与该用户的 p2p Topic 的状态（p2p Topic 的所有者为 0）
	if _, err = tx.Exec("UPDATE topics JOIN subscriptions ON topics.name=subscriptions.topic "+
		"SET topics.state=?, topics.stateat=? WHERE topics.owner=0 AND subscriptions.userid=? AND topics.state!=?",
		state, now, decoded_uid, t.StateDeleted); err != nil {
		return err
	}

	// 订阅无需更新：
	// 已禁用用户的订阅不会被禁用，仍可操作。
	return nil
}

// UserUpdate 更新用户对象。
func (a *adapter) UserUpdate(uid t.Uid, update map[string]any) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	cols, args := common.UpdateByMap(update)
	decoded_uid := store.DecodeUid(uid)
	args = append(args, decoded_uid)
	_, err = tx.Exec("UPDATE users SET "+strings.Join(cols, ",")+" WHERE id=?", args...)
	if err != nil {
		return err
	}

	if state, ok := update["State"]; ok {
		now, _ := update["StateAt"].(time.Time)
		err = a.topicStateForUser(tx, decoded_uid, now, state)
		if err != nil {
			return err
		}
	}

	// 标签也存储在单独的表中
	if tags := common.ExtractTags(update); tags != nil {
		// 首先删除所有用户标签
		_, err = tx.Exec("DELETE FROM usertags WHERE userid=?", decoded_uid)
		if err != nil {
			return err
		}
		// 现在插入新标签
			err = addTags(tx, "usertags", "userid", decoded_uid, tags, false)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// UserUpdateTags 添加、移除或重置用户的标签。
func (a *adapter) UserUpdateTags(uid t.Uid, add, remove, reset []string) ([]string, error) {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	decoded_uid := store.DecodeUid(uid)

	if reset != nil {
		// 重置时先删除所有标签。
		_, err = tx.Exec("DELETE FROM usertags WHERE userid=?", decoded_uid)
		if err != nil {
			return nil, err
		}
		add = reset
		remove = nil
	}

	// 现在插入新标签。重置时忽略重复。
	err = addTags(tx, "usertags", "userid", decoded_uid, add, reset == nil)
	if err != nil {
		return nil, err
	}

	// 删除标签。
	err = removeTags(tx, "usertags", "userid", decoded_uid, remove)
	if err != nil {
		return nil, err
	}

	var allTags []string
	err = tx.Select(&allTags, "SELECT tag FROM usertags WHERE userid=?", decoded_uid)
	if err != nil {
		return nil, err
	}

	_, err = tx.Exec("UPDATE users SET tags=? WHERE id=?", t.StringSlice(allTags), decoded_uid)
	if err != nil {
		return nil, err
	}

	return allTags, tx.Commit()
}

// UserGetByCred 返回给定已验证凭据的用户 ID。
func (a *adapter) UserGetByCred(method, value string) (t.Uid, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var decoded_uid int64
	err := a.db.GetContext(ctx, &decoded_uid, "SELECT userid FROM credentials WHERE synthetic=?", method+":"+value)
	if err == nil {
		return store.EncodeUid(decoded_uid), nil
	}

	if err == sql.ErrNoRows {
		// 如果用户不存在则清除错误
		return t.ZeroUid, nil
	}
	return t.ZeroUid, err
}

// UserUnreadCount 返回所有具有 R 权限的 Topic 中未读消息的总数。
// 如果读取失败，计数仍然返回，但带有原始用户 ID，未读计数未定义且错误非 nil。
// UserUnreadCount 不统计 Channel 中的未读消息，尽管它应该。
func (a *adapter) UserUnreadCount(ids ...t.Uid) (map[t.Uid]int, error) {
	uids := make([]any, len(ids))
	counts := make(map[t.Uid]int, len(ids))
	for i, id := range ids {
		uids[i] = store.DecodeUid(id)
		// 确保所有原始 uid 始终存在。
		counts[id] = 0
	}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	// 联表查询未读消息数：利用 IF/CONCAT 动态支持 Channel (将 chn... 前缀映射为 topics 主表中的 grp...)
	q, args, _ := sqlx.In("SELECT s.userid, SUM(t.seqid)-SUM(s.readseqid) AS unreadcount FROM topics AS t JOIN subscriptions AS s "+
		"ON t.name = IF(LEFT(s.topic, 3) = 'chn', CONCAT('grp', SUBSTRING(s.topic, 4)), s.topic) "+
		"WHERE s.userid IN (?) AND s.deletedat IS NULL AND t.state!=? AND "+
		"INSTR(s.modewant, 'R')>0 AND INSTR(s.modegiven, 'R')>0 GROUP BY s.userid", uids, int(t.StateDeleted))
	rows, err := a.db.QueryxContext(ctx, a.db.Rebind(q), args...)
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

	rows, err := a.db.QueryxContext(ctx,
		"SELECT u.id, IFNULL(SUM(c.done),0) AS total FROM users AS u "+
			"LEFT JOIN credentials AS c ON u.id=c.userid WHERE u.lastseen IS NULL AND u.updatedat<? "+
			"GROUP BY u.id, u.updatedat HAVING total=0 ORDER BY u.updatedat ASC LIMIT ?", lastUpdatedBefore, limit)
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

func (a *adapter) topicCreate(tx *sqlx.Tx, topic *t.Topic) error {
	_, err := tx.Exec("INSERT INTO topics(createdat,updatedat,touchedat,state,name,usebt,owner,access,public,trusted,tags,aux) "+
		"VALUES(?,?,?,?,?,?,?,?,?,?,?,?)",
		topic.CreatedAt, topic.UpdatedAt, topic.TouchedAt, topic.State, topic.Id, topic.UseBt,
		store.DecodeUid(t.ParseUid(topic.Owner)), topic.Access, common.ToJSON(topic.Public), common.ToJSON(topic.Trusted),
		topic.Tags, common.ToJSON(topic.Aux))
	if err != nil {
		return err
	}

	// 保存 Topic 的标签到单独的表以便 Topic 可被搜索。
	return addTags(tx, "topictags", "topic", topic.Id, topic.Tags, false)
}

// TopicCreate 将 Topic 对象保存到数据库。
func (a *adapter) TopicCreate(topic *t.Topic) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	err = a.topicCreate(tx, topic)
	if err != nil {
		return err
	}
	return tx.Commit()
}

// If undelete = true - update 订阅 on duplicate key, otherwise ignore the duplicate.
func createSubscription(tx *sqlx.Tx, sub *t.Subscription, undelete bool) error {

	isOwner := (sub.ModeGiven & sub.ModeWant).IsOwner()

	jpriv := common.ToJSON(sub.Private)
	decoded_uid := store.DecodeUid(t.ParseUid(sub.User))
	_, err := tx.Exec(
		"INSERT INTO subscriptions(createdat,updatedat,deletedat,userid,topic,modeWant,modeGiven,private) "+
			"VALUES(?,?,NULL,?,?,?,?,?)",
		sub.CreatedAt, sub.UpdatedAt, decoded_uid, sub.Topic, sub.ModeWant.String(), sub.ModeGiven.String(), jpriv)

	if err != nil && isDupe(err) {
		if undelete {
			_, err = tx.Exec("UPDATE subscriptions SET createdat=?,updatedat=?,deletedat=NULL,modeWant=?,modeGiven=?,"+
				"delid=0,recvseqid=0,readseqid=0 WHERE topic=? AND userid=?",
				sub.CreatedAt, sub.UpdatedAt, sub.ModeWant.String(), sub.ModeGiven.String(), sub.Topic, decoded_uid)
		} else {
			_, err = tx.Exec("UPDATE subscriptions SET createdat=?,updatedat=?,deletedat=NULL,modeWant=?,modeGiven=?,"+
				"delid=0,recvseqid=0,readseqid=0,private=? WHERE topic=? AND userid=?",
				sub.CreatedAt, sub.UpdatedAt, sub.ModeWant.String(), sub.ModeGiven.String(), jpriv,
				sub.Topic, decoded_uid)
		}
	}

	if err == nil && isOwner {
		// Update Topic owner if the 订阅 is with owner rights.
		// 不要在此处递增订阅者计数 - 在 TopicShare 中批量完成。
		_, err = tx.Exec("UPDATE topics SET owner=? WHERE name=?", decoded_uid, sub.Topic)
	}
	return err
}

// TopicCreateP2P given two 用户 creates a p2p Topic.
func (a *adapter) TopicCreateP2P(initiator, invited *t.Subscription) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	err = createSubscription(tx, initiator, false)
	if err != nil {
		return err
	}

	// If the second 订阅 exists, don't overwrite it. Just make sure it's not deleted.
	err = createSubscription(tx, invited, true)
	if err != nil {
		return err
	}

	topic := &t.Topic{ObjHeader: t.ObjHeader{Id: initiator.Topic}}
	topic.ObjHeader.MergeTimes(&initiator.ObjHeader)
	topic.TouchedAt = initiator.GetTouchedAt()
	err = a.topicCreate(tx, topic)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// TopicGet 按名称加载单个 Topic（如果存在）。如果 Topic 不存在返回 (nil, nil)
func (a *adapter) TopicGet(topic string) (*t.Topic, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	// 按名称获取 Topic
	var tt = new(t.Topic)
	if err := a.db.GetContext(ctx, tt,
		"SELECT createdat,updatedat,state,stateat,touchedat,name AS id,usebt,access,owner,seqid,delid,subcnt,public,trusted,tags,aux "+
			"FROM topics WHERE name=?", topic); err != nil {
		if err == sql.ErrNoRows {
			// 未找到 - 清除错误
			err = nil
		}
		return nil, err
	}

	if t.GetTopicCat(topic) == t.TopicCatGrp {
		// Topic 已找到，获取订阅数（忽略 Topic.subcnt 中设置的值）。同时尝试 Topic 和 Channel 名称。
		var subCnt int
		if err := a.db.GetContext(ctx, &subCnt,
			"SELECT COUNT(*) FROM subscriptions WHERE topic IN (?,?) AND deletedat IS NULL", topic, t.GrpToChn(topic)); err != nil {
			return nil, err
		}

		if subCnt != tt.SubCnt {
			// Update the Topic with the correct 订阅 count.
			tt.SubCnt = subCnt
			if _, err := a.db.ExecContext(ctx, "UPDATE topics SET subcnt=? WHERE name=?", subCnt, topic); err != nil {
				return nil, err
			}
		}
	}

	tt.Owner = common.EncodeUidString(tt.Owner).String()
	tt.Public = common.FromJSON(tt.Public)
	tt.Trusted = common.FromJSON(tt.Trusted)

	return tt, nil
}

// TopicsForUser loads 用户's contact list: p2p and grp Topic, except for 'me' & 'fnd' 订阅.
// 读取并反归一化 Public 和 Trusted 值。
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

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx, q, args...)
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
		if err = rows.StructScan(&sub); err != nil {
			break
		}
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
			// 可能将 Channel 名称转换为群组 Topic 名称。
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
		q, args, _ = sqlx.In(q, topq)

		if !keepDeleted {
			// 可选跳过已删除的 Topic。
			q += " AND state!=?"
			args = append(args, t.StateDeleted)
		}

		if !ims.IsZero() {
			// 如果提供了缓存时间戳：仅获取较新的条目。
			q += " AND touchedat>?"
			args = append(args, ims)

			if limit > 0 && limit < len(topq) {
				// 没有意义获取超过请求的限制。
				q += " ORDER BY touchedat LIMIT ?"
				args = append(args, limit)
			}
		}

		ctx2, cancel2 := a.getContext()
		if cancel2 != nil {
			defer cancel2()
		}
		rows, err = a.db.QueryxContext(ctx2, a.db.Rebind(q), args...)
		if err != nil {
			return nil, err
		}

		var top t.Topic
		for rows.Next() {
			if err = rows.StructScan(&top); err != nil {
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
				sub.SetPublic(common.FromJSON(top.Public))
				sub.SetTrusted(common.FromJSON(top.Trusted))
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
		q, args, _ = sqlx.In(q, usrq)
		if !keepDeleted {
			// Optionally skip deleted 用户.
			q += " AND state!=?"
			args = append(args, t.StateDeleted)
		}

		// Ignoring ims: we need all 用户 to get LastSeen and UserAgent.

		ctx3, cancel3 := a.getContext()
		if cancel3 != nil {
			defer cancel3()
		}
		rows, err = a.db.QueryxContext(ctx3, a.db.Rebind(q), args...)
		if err != nil {
			return nil, err
		}

		for rows.Next() {
			var usr2 t.User
			if err = rows.StructScan(&usr2); err != nil {
				break
			}

			joinOn := uid.P2PName(common.EncodeUidString(usr2.Id))
			if sub, ok := join[joinOn]; ok {
				sub.UpdatedAt = common.SelectLatestTime(sub.UpdatedAt, usr2.UpdatedAt)
				sub.SetState(usr2.State)
				sub.SetPublic(common.FromJSON(usr2.Public))
				sub.SetTrusted(common.FromJSON(usr2.Trusted))
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

// UsersForTopic loads 用户 subscribed to the given Topic (not Channel readers).
// The difference between UsersForTopic vs SubsForTopic is that the former loads 用户.Public,
// 后者不加载。
func (a *adapter) UsersForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	tcat := t.GetTopicCat(topic)

	// Fetch all subscribed 用户. The number of 用户 is not large.
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

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Fetch 订阅.
	var sub t.Subscription
	var subs []t.Subscription
	var lastSeen sql.NullTime
	var userAgent string
	var public, trusted any
	for rows.Next() {
		if err = rows.Scan(
			&sub.CreatedAt, &sub.UpdatedAt, &sub.DeletedAt,
			&sub.User, &sub.Topic, &sub.DelId, &sub.RecvSeqId,
			&sub.ReadSeqId, &sub.ModeWant, &sub.ModeGiven,
			&public, &trusted, &lastSeen, &userAgent, &sub.Private); err != nil {
			break
		}

		sub.User = common.EncodeUidString(sub.User).String()
		sub.Private = common.FromJSON(sub.Private)
		sub.SetPublic(common.FromJSON(public))
		sub.SetTrusted(common.FromJSON(trusted))
		sub.SetLastSeenAndUA(&lastSeen.Time, userAgent)
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
// 如果 includeChan 为 true，查询将同时添加 Channel 名称和群组 Topic 名称。
func (a *adapter) topicNamesForUser(sqlQuery string, includeChan bool, args ...any) ([]string, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx, sqlQuery, args...)
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
	return a.topicNamesForUser("SELECT name FROM topics WHERE owner=? AND state!=?",
		false, store.DecodeUid(uid), t.StateDeleted)
}

// ChannelsForUser loads a slice of Topic names where the 用户 is a Channel reader and notifications (P) are enabled.
func (a *adapter) ChannelsForUser(uid t.Uid) ([]string, error) {
	return a.topicNamesForUser("SELECT topic FROM subscriptions WHERE userid=? AND topic LIKE 'chn%' "+
		"AND INSTR(modewant,'P')>0 AND INSTR(modegiven,'P')>0 AND deletedat IS NULL",
		false, store.DecodeUid(uid))
}

// TopicShare adds 订阅 to a Topic and increments the Topic's subcnt.
func (a *adapter) TopicShare(topic string, shares []*t.Subscription) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	for _, sub := range shares {
		err = createSubscription(tx, sub, true)
		if err != nil {
			return err
		}
	}

	if topic != "" {
		// Update Topic's 订阅 count.
		if _, err = tx.Exec("UPDATE topics SET subcnt=subcnt+? WHERE name=?", len(shares), topic); err != nil {
			return err
		}
	}

	return tx.Commit()
}

// TopicDelete deletes Topic, 订阅, 消息.
func (a *adapter) TopicDelete(topic string, isChan, hard bool) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// If the Topic is a Channel, must try to delete 订阅 under both grpXXX and chnXXX names.
	args := []any{topic}
	if isChan {
		args = append(args, t.GrpToChn(topic))
	}

	if hard {
		// Delete 订阅. If this is a Channel, delete both group 订阅 and Channel 订阅.
		q, args, _ := sqlx.In("DELETE FROM subscriptions WHERE topic IN (?)", args)
		if _, err = tx.Exec(tx.Rebind(q), args...); err != nil {
			return err
		}

		if err = messageDeleteList(tx, topic, nil); err != nil {
			return err
		}

		if _, err = tx.Exec("DELETE FROM topictags WHERE topic=?", topic); err != nil {
			return err
		}

		if _, err = tx.Exec("DELETE FROM topics WHERE name=?", topic); err != nil {
			return err
		}
	} else {
		now := t.TimeNow()

		q, args, _ := sqlx.In("UPDATE subscriptions SET updatedat=?,deletedat=? WHERE topic IN (?)", now, now, args)
		if _, err = tx.Exec(tx.Rebind(q), args...); err != nil {
			return err
		}

		if _, err = tx.Exec("UPDATE topics SET updatedat=?,touchedat=?,state=?,stateat=? WHERE name=?",
			now, now, t.StateDeleted, now, topic); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// TopicUpdateOnMessage updates Topic's seqid and touchedat when a new 消息 is posted.
func (a *adapter) TopicUpdateOnMessage(topic string, msg *t.Message) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.ExecContext(ctx, "UPDATE topics SET seqid=?,touchedat=? WHERE name=?", msg.SeqId, msg.CreatedAt, topic)

	return err
}

// TopicUpdateSubCnt 更新 Topic 中反归一化的订阅者计数。
func (a *adapter) TopicUpdateSubCnt(topic string) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.ExecContext(ctx,
		"UPDATE topics SET subcnt=(SELECT COUNT(*) FROM subscriptions WHERE topic IN (?,?) AND deletedat IS NULL) WHERE name=?",
		topic, t.GrpToChn(topic), topic)
	return err
}

// TopicUpdate 更新 Topic 中给定更新映射的字段。
// 如果更新包含 UpdatedAt 但不包含 TouchedAt，则 TouchedAt 设置为 UpdatedAt
func (a *adapter) TopicUpdate(topic string, update map[string]any) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	if t, u := update["TouchedAt"], update["UpdatedAt"]; t == nil && u != nil {
		update["TouchedAt"] = u
	}
	cols, args := common.UpdateByMap(update)
	args = append(args, topic)
	_, err = tx.Exec("UPDATE topics SET "+strings.Join(cols, ",")+" WHERE name=?", args...)
	if err != nil {
		return err
	}

	// 标签也存储在单独的表中
	if tags := common.ExtractTags(update); tags != nil {
		// 首先删除所有用户标签
		_, err = tx.Exec("DELETE FROM topictags WHERE topic=?", topic)
		if err != nil {
			return err
		}
		// 现在插入新标签
		err = addTags(tx, "topictags", "topic", topic, tags, false)
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

func (a *adapter) TopicOwnerChange(topic string, newOwner t.Uid) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	_, err := a.db.ExecContext(ctx, "UPDATE topics SET owner=? WHERE name=?", store.DecodeUid(newOwner), topic)
	return err
}

// Get a 订阅 of a 用户 to a Topic.
func (a *adapter) SubscriptionGet(topic string, user t.Uid, keepDeleted bool) (*t.Subscription, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	query := `SELECT createdat,updatedat,deletedat,userid AS user,topic,delid,recvseqid,
		readseqid,modewant,modegiven,private FROM subscriptions WHERE topic=? AND userid=?`
	if !keepDeleted {
		query += " AND deletedat IS NULL"
	}
	var sub t.Subscription
	err := a.db.GetContext(ctx, &sub, query, topic, store.DecodeUid(user))
	if err != nil {
		if err == sql.ErrNoRows {
			// 未找到 - 清除错误
			err = nil
		}
		return nil, err
	}

	sub.User = user.String()
	sub.Private = common.FromJSON(sub.Private)

	return &sub, nil
}

// SubsForUser loads all 用户's 订阅. Does NOT load Public or Private values and does
// not load deleted 订阅.
func (a *adapter) SubsForUser(forUser t.Uid) ([]t.Subscription, error) {
	q := `SELECT createdat,updatedat,deletedat,userid AS user,topic,delid,recvseqid,
		readseqid,modewant,modegiven FROM subscriptions WHERE userid=? AND deletedat IS NULL`
	args := []any{store.DecodeUid(forUser)}

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []t.Subscription
	var sub t.Subscription
	for rows.Next() {
		if err = rows.StructScan(&sub); err != nil {
			break
		}
		sub.User = forUser.String()
		subs = append(subs, sub)
	}
	if err == nil {
		err = rows.Err()
	}

	return subs, err
}

// SubsForTopic 获取 Topic 的所有订阅。不加载 Public 值，也不加载 Channel 读者。
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

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var subs []t.Subscription
	var sub t.Subscription
	for rows.Next() {
		if err = rows.StructScan(&sub); err != nil {
			break
		}

		sub.User = common.EncodeUidString(sub.User).String()
		sub.Private = common.FromJSON(sub.Private)
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
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
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

	if _, err = tx.Exec(q, args...); err != nil {
		return err
	}

	return tx.Commit()
}

// SubsDelete marks at most one 订阅 as deleted (soft-deleting).
func (a *adapter) SubsDelete(topic string, user t.Uid) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}

	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	decoded_id := store.DecodeUid(user)
	now := t.TimeNow()

	// Mark 订阅 as deleted.
	res, err := tx.ExecContext(ctx,
		"UPDATE subscriptions SET updatedat=?,deletedat=? WHERE topic=? AND userid=? AND deletedat IS NULL",
		now, now, topic, decoded_id)
	if err != nil {
		return err
	}

	affected, err := res.RowsAffected()
	if err == nil && affected == 0 {
		// 确保上面的 tx.Rollback() 被执行
		err = t.ErrNotFound
		return err
	}

	// Channel readers cannot delete 消息.
	if !t.IsChannel(topic) {
		// Remove records of 消息 soft-deleted by this 用户.
		_, err = tx.Exec("DELETE FROM dellog WHERE topic=? AND deletedfor=?", topic, decoded_id)
		if err != nil {
			return err
		}
	}

	if t.GetTopicCat(topic) == t.TopicCatGrp {
		// Decrement Topic 订阅 count (only one 订阅 is	deleted).
		_, err = tx.Exec("UPDATE topics SET subcnt=subcnt-1 WHERE name=?", t.ChnToGrp(topic))
		if err != nil {
			return err
		}
	}

	return tx.Commit()
}

// subsDelForUser marks 用户's 订阅 as deleted.
func subsDelForUser(tx *sqlx.Tx, decoded_uid int64, hard bool) error {
	// Decrement 订阅 count for all Topic the 用户 is subscribed to.
	rows, err := tx.Query("SELECT topic FROM subscriptions WHERE userid=? AND deletedat IS NULL", decoded_uid)
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
		sql, args, err := sqlx.In("UPDATE topics SET subcnt=subcnt-1 WHERE name IN (?)", topics)
		_, err = tx.Exec(tx.Rebind(sql), args...)
		if err != nil {
			return err
		}
	}

	if hard {
		_, err = tx.Exec("DELETE FROM subscriptions WHERE userid=?", decoded_uid)
	} else {
		now := t.TimeNow()
		_, err = tx.Exec("UPDATE subscriptions SET updatedat=?,deletedat=? WHERE userid=? AND deletedat IS NULL",
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

	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	if err = subsDelForUser(tx, store.DecodeUid(user), hard); err != nil {
		return err
	}

	return tx.Commit()
}

// Find returns a list of 用户 or group Topic who match given tags, such as "email:jdoe@example.com" or "tel:+18003287448".
func (a *adapter) Find(caller, promoPrefix string, req [][]string, opt []string, activeOnly bool) ([]t.Subscription, error) {
	var args []any
	stateConstraint := ""
	if activeOnly {
		args = append(args, t.StateOK)
		stateConstraint = "u.state=? AND "
	}
	index := make(map[string]struct{})
	allReq := t.FlattenDoubleSlice(req)
	for _, tag := range append(allReq, opt...) {
		args = append(args, tag)
		index[tag] = struct{}{}
	}

	var matcher string
	if promoPrefix != "" {
		// 最大标签数为 16。使用 20 确保一个前缀匹配大于所有非前缀匹配的总和。
		matcher = "SUM(CASE WHEN LOCATE('" + promoPrefix + "', tg.tag)=1 THEN 20 ELSE 1 END)"
	} else {
		matcher = "COUNT(*)"
	}

	query := "SELECT u.id,u.createdat,u.updatedat,0,u.access,0 AS subcnt,u.public,u.trusted,u.tags," + matcher + " AS matches " +
		"FROM users AS u JOIN usertags AS tg ON tg.userid=u.id " +
		"WHERE " + stateConstraint + "tg.tag IN (?" + strings.Repeat(",?", len(allReq)+len(opt)-1) + ") " +
		"GROUP BY u.id,u.createdat,u.updatedat,u.access,u.public,u.trusted,u.tags "
	if len(allReq) > 0 {
		q, a := common.DisjunctionSql(req, "tg.tag")
		query += q
		args = append(args, a...)
	}

	query += "UNION ALL "

	if activeOnly {
		args = append(args, t.StateOK)
		stateConstraint = "t.state=? AND "
	}
	for _, tag := range append(allReq, opt...) {
		args = append(args, tag)
	}

	query += "SELECT t.name AS topic,t.createdat,t.updatedat,t.usebt,t.access,t.subcnt,t.public,t.trusted,t.tags," + matcher + " AS matches " +
		"FROM topics AS t JOIN topictags AS tg ON t.name=tg.topic " +
		"WHERE " + stateConstraint + "tg.tag IN (?" + strings.Repeat(",?", len(allReq)+len(opt)-1) + ") " +
		"GROUP BY t.name,t.createdat,t.updatedat,t.usebt,t.access,t.subcnt,t.public,t.trusted,t.tags "
	if len(allReq) > 0 {
		q, a := common.DisjunctionSql(req, "tg.tag")
		query += q
		args = append(args, a...)
	}
	query += "ORDER BY matches DESC, subcnt DESC LIMIT ?"
	args = append(args, a.maxResults)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	// Get 用户 matched by tags, sort by number of matches from high to low.
	rows, err := a.db.QueryxContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	// Read results as 订阅.
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
			// 这是一个 Channel，将 grp 转换为 chn 名称：所有支持 Channel 的
			// Topic 在搜索结果中应显示为 Channel。
			sub.Topic = t.GrpToChn(sub.Topic)
		}

		sub.SetSubCnt(subcnt)
		sub.SetPublic(common.FromJSON(public))
		sub.SetTrusted(common.FromJSON(trusted))
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

// FindOne returns the first Topic or 用户 which matches the given tag.
func (a *adapter) FindOne(tag string) (string, error) {
	var args []any

	query := "SELECT t.name AS topic FROM topics AS t LEFT JOIN topictags AS tt ON t.name=tt.topic " +
		"WHERE tt.tag=?"
	args = append(args, tag)

	query += " UNION ALL "

	query += "SELECT u.id AS topic FROM users AS u LEFT JOIN usertags AS ut ON ut.userid=u.id " +
		"WHERE ut.tag=?"
	args = append(args, tag)

	// LIMIT 应用于所有结果行。
	query += " LIMIT 1"

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	rows, err := a.db.QueryxContext(ctx, query, args...)
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
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	// 存储 assignes 消息 ID, but we don't use it. 消息 IDs are not used anywhere.
	// Using a sequential ID provided by the 数据库.
	res, err := a.db.ExecContext(ctx,
		"INSERT INTO messages(createdAt,updatedAt,seqid,topic,`from`,head,content) VALUES(?,?,?,?,?,?,?)",
		msg.CreatedAt, msg.UpdatedAt, msg.SeqId, msg.Topic,
		store.DecodeUid(t.ParseUid(msg.From)), msg.Head, common.ToJSON(msg.Content))
	if err == nil {
		id, _ := res.LastInsertId()
		// Replacing ID given by 存储 by ID given by the DB.
		msg.SetUid(t.Uid(id))
	}
	return err
}

// MessageGetAll returns 消息 matching the query.
func (a *adapter) MessageGetAll(topic string, forUser t.Uid, opts *t.QueryOpt) ([]t.Message, error) {
	var limit = a.maxMessageResults

	args := []any{store.DecodeUid(forUser), topic}
	seqIdConstraint := ""
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
			if opts.Before > 1 {
				// MySQL BETWEEN 是包含两端的，IM API 要求包含起始不包含结束，因此 -1
				args = append(args, opts.Before-1)
			} else {
				args = append(args, 1<<31-1)
			}
		}

		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}

	args = append(args, limit)

	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	rows, err := a.db.QueryxContext(
		ctx,
		"SELECT m.createdat,m.updatedat,m.deletedat,m.delid,m.seqid,m.topic,m.`from`,m.head,m.content"+
			" FROM messages AS m LEFT JOIN dellog AS d"+
			" ON d.topic=m.topic AND m.seqid BETWEEN d.low AND d.hi-1 AND d.deletedfor=?"+
			" WHERE m.delid=0 AND m.topic=? "+seqIdConstraint+" AND d.deletedfor IS NULL"+
			" ORDER BY m.seqid DESC LIMIT ?",
		args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	msgs := make([]t.Message, 0, limit)
	for rows.Next() {
		var msg t.Message
		if err = rows.StructScan(&msg); err != nil {
			break
		}
		msg.From = common.EncodeUidString(msg.From).String()
		msg.Content = common.FromJSON(msg.Content)
		msgs = append(msgs, msg)
	}
	if err == nil {
		err = rows.Err()
	}

	return msgs, err
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
	rows, err := a.db.QueryxContext(ctx, "SELECT topic,deletedfor,delid,low,hi FROM dellog WHERE topic=? AND delid BETWEEN ? AND ?"+
		" AND (deletedFor=0 OR deletedFor=?)"+
		" ORDER BY delid LIMIT ?", topic, lower, upper, store.DecodeUid(forUser), limit)
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
		if err = rows.StructScan(&dellog); err != nil {
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

func messageDeleteList(tx *sqlx.Tx, topic string, toDel *t.DelMessage) error {
	var err error

	if toDel == nil {
		// Whole Topic is being deleted, thus also deleting all 消息.
		_, err = tx.Exec("DELETE FROM dellog WHERE topic=?", topic)
		if err == nil {
			_, err = tx.Exec("DELETE FROM messages WHERE topic=?", topic)
		}
		// filemsglinks 将因 ON DELETE CASCADE 而被删除
		return err
	}

	// Only some 消息 are being deleted.

	delRanges := toDel.SeqIdRanges

	if toDel.DeletedFor == "" {
		// Hard-deleting 消息 requires updates to the 消息 table.
		where := "m.topic=?"
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
		err = tx.Select(&seqIDs, "SELECT seqid FROM messages AS m WHERE "+where, args...)
		if err != nil {
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

		_, err = tx.Exec("DELETE fml.* FROM filemsglinks AS fml INNER JOIN messages AS m ON m.id=fml.msgid WHERE "+
			where, args...)
		if err != nil {
			return err
		}

		// Instead of deleting 消息, clear all content.
		_, err = tx.Exec("UPDATE messages AS m SET m.deletedat=?,m.delId=?,m.`from`=0,m.head=NULL,m.content=NULL WHERE "+
			where, append([]any{t.TimeNow(), toDel.DelId}, args...)...)
		if err != nil {
			return err
		}
	}

	// 现在记录日志。硬删除和软删除都需要。
	var insert *sql.Stmt
	if insert, err = tx.Prepare(
		"INSERT INTO dellog(topic,deletedfor,delid,low,hi) VALUES(?,?,?,?,?)"); err != nil {
		return err
	}

	forUser := common.DecodeUidString(toDel.DeletedFor)
	for _, rng := range delRanges {
		if rng.Hi == 0 {
			// Dellog 必须包含有效的 Low 和 *Hi*。
			rng.Hi = rng.Low + 1
		}
		// 每个范围一条日志记录。
		if _, err = insert.Exec(topic, forUser, toDel.DelId, rng.Low, rng.Hi); err != nil {
			break
		}
	}

	if err != nil {
		return err
	}

	if toDel.DelId > 0 {
		if _, err = tx.Exec("UPDATE topics SET delid=? WHERE id=?", toDel.DelId, topic); err != nil {
			return err
		}
		if forUser == 0 {
			_, err = tx.Exec("UPDATE subscriptions SET delid=? WHERE topic=?", toDel.DelId, topic)
		} else {
			_, err = tx.Exec("UPDATE subscriptions SET delid=? WHERE topic=? AND user=?", toDel.DelId, topic, toDel.DeletedFor)
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
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}

	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	if err = messageDeleteList(tx, topic, toDel); err != nil {
		return err
	}

	return tx.Commit()
}

func deviceHasher(deviceID string) string {
	// 生成自定义键作为设备 ID 的 64 位哈希，以确保
	// 键的长度可预测
	hasher := fnv.New64()
	hasher.Write([]byte(deviceID))
	return strconv.FormatUint(uint64(hasher.Sum64()), 16)
}

// 设备管理（用于推送通知）。

// DeviceUpsert 创建或更新设备记录。
func (a *adapter) DeviceUpsert(uid t.Uid, def *t.DeviceDef) error {
	hash := deviceHasher(def.DeviceId)

	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// 确保设备 ID 的唯一性：删除该设备 ID 的所有记录
	_, err = tx.Exec("DELETE FROM devices WHERE hash=?", hash)
	if err != nil {
		return err
	}

	// Actually add/update DeviceId for the new 用户
	_, err = tx.Exec("INSERT INTO devices(userid, hash, deviceId, platform, lastseen, lang) VALUES(?,?,?,?,?,?)",
		store.DecodeUid(uid), hash, def.DeviceId, def.Platform, def.LastSeen, def.Lang)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// DeviceGetAll returns all devices for a given set of 用户.
func (a *adapter) DeviceGetAll(uids ...t.Uid) (map[t.Uid][]t.DeviceDef, int, error) {
	unums := common.DecodeUidSlice(uids)

	q, unums, _ := sqlx.In("SELECT userid,deviceid,platform,lastseen,lang FROM devices WHERE userid IN (?)", unums)
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	rows, err := a.db.QueryxContext(ctx, q, unums...)
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
		if err = rows.StructScan(&device); err != nil {
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

func deviceDelete(tx *sqlx.Tx, uid t.Uid, deviceID string) error {
	var err error
	var res sql.Result
	if deviceID == "" {
		res, err = tx.Exec("DELETE FROM devices WHERE userid=?", store.DecodeUid(uid))
	} else {
		res, err = tx.Exec("DELETE FROM devices WHERE userid=? AND hash=?", store.DecodeUid(uid), deviceHasher(deviceID))
	}

	if err == nil {
		if count, _ := res.RowsAffected(); count == 0 {
			err = t.ErrNotFound
		}
	}

	return err
}

// DeviceDelete 删除设备记录（推送令牌）。
func (a *adapter) DeviceDelete(uid t.Uid, deviceID string) error {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	err = deviceDelete(tx, uid, deviceID)
	if err != nil {
		return err
	}

	return tx.Commit()
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
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return false, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	now := t.TimeNow()
	userId := common.DecodeUidString(cred.User)

	// 强制唯一性：如果凭据已确认，“method:value”必须唯一。
	// 如果凭据尚未确认，“userid:method:value”是唯一的。
	synth := cred.Method + ":" + cred.Value

	if !cred.Done {
		// 检查此凭据是否已被验证。
		var done bool
		err = tx.Get(&done, "SELECT done FROM credentials WHERE synthetic=?", synth)
		if err == nil {
			// 赋值 err 以确保事务关闭。
			err = t.ErrDuplicate
			return false, err
		}
		if err != sql.ErrNoRows {
			return false, err
		}
		// 我们将插入新记录。
		synth = cred.User + ":" + synth

		// Adding new unvalidated credential. Deactivate all unvalidated records of this 用户 and method.
		_, err = tx.Exec("UPDATE credentials SET deletedat=? WHERE userid=? AND method=? AND done=FALSE",
			now, userId, cred.Method)
		if err != nil {
			return false, err
		}
		// Assume that the record exists and try to update it: undelete, update timestamp and 响应 value.
		res, err := tx.Exec("UPDATE credentials SET updatedat=?,deletedat=NULL,resp=?,done=FALSE WHERE synthetic=?",
			cred.UpdatedAt, cred.Resp, synth)
		if err != nil {
			return false, err
		}
		// 如果记录已更新，则一切正常。
		if numrows, _ := res.RowsAffected(); numrows > 0 {
			return false, tx.Commit()
		}
	} else {
		// 硬删除未确认的记录（如果存在）。
		_, err = tx.Exec("DELETE FROM credentials WHERE synthetic=?", cred.User+":"+synth)
		if err != nil {
			return false, err
		}
	}
	// 添加新记录。
	_, err = tx.Exec("INSERT INTO credentials(createdat,updatedat,method,value,synthetic,userid,resp,done) "+
		"VALUES(?,?,?,?,?,?,?,?)",
		cred.CreatedAt, cred.UpdatedAt, cred.Method, cred.Value, synth, userId, cred.Resp, cred.Done)
	if err != nil {
		if isDupe(err) {
			return true, t.ErrDuplicate
		}
		return true, err
	}
	return true, tx.Commit()
}

// credDel 删除给定用户的指定验证方法或所有方法。
// 1. 如果用户正在被删除，硬删除所有记录（method == ""）
// 2. 如果删除单个值：
// 2.1 如果已验证或没有验证尝试则删除它
// （否则可能被用来规避验证尝试次数限制）。
// 2.2 否则标记为软删除。
func credDel(tx *sqlx.Tx, uid t.Uid, method, value string) error {
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

	var err error
	var res sql.Result
	if method == "" {
		// 情况 1
		res, err = tx.Exec("DELETE FROM credentials"+constraints, args...)
		if err == nil {
			if count, _ := res.RowsAffected(); count == 0 {
				err = t.ErrNotFound
			}
		}
		return err
	}

	// 情况 2.1
	res, err = tx.Exec("DELETE FROM credentials"+constraints+" AND (done=TRUE OR retries=0)", args...)
	if err != nil {
		return err
	}
	if count, _ := res.RowsAffected(); count > 0 {
		return nil
	}

	// 情况 2.2
	args = append([]any{t.TimeNow()}, args...)
	res, err = tx.Exec("UPDATE credentials SET deletedat=?"+constraints, args...)
	if err == nil {
		if count, _ := res.RowsAffected(); count >= 0 {
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
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	err = credDel(tx, uid, method, value)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// CredConfirm 将指定的凭据方法标记为已确认。
func (a *adapter) CredConfirm(uid t.Uid, method string) error {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	res, err := a.db.ExecContext(
		ctx,
		"UPDATE credentials SET updatedat=?,done=TRUE,synthetic=CONCAT(method,':',value) "+
			"WHERE userid=? AND method=? AND deletedat IS NULL AND done=FALSE",
		t.TimeNow(), store.DecodeUid(uid), method)
	if err != nil {
		if isDupe(err) {
			return t.ErrDuplicate
		}
		return err
	}
	if numrows, _ := res.RowsAffected(); numrows < 1 {
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
	_, err := a.db.ExecContext(ctx, "UPDATE credentials SET updatedat=?,retries=retries+1 WHERE userid=? AND method=? AND done=FALSE",
		t.TimeNow(), store.DecodeUid(uid), method)
	return err
}

// CredGetActive 返回指定用户和方法当前活跃的未验证凭据。
func (a *adapter) CredGetActive(uid t.Uid, method string) (*t.Credential, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}
	var cred t.Credential
	err := a.db.GetContext(ctx, &cred, "SELECT createdat,updatedat,method,value,resp,done,retries "+
		"FROM credentials WHERE userid=? AND deletedat IS NULL AND method=? AND done=FALSE",
		store.DecodeUid(uid), method)
	if err != nil {
		if err == sql.ErrNoRows {
			err = nil
		}
		return nil, err
	}
	cred.User = uid.String()

	return &cred, nil
}

// CredGetAll 返回指定用户和方法的凭据记录，可选仅已验证或全部。
func (a *adapter) CredGetAll(uid t.Uid, method string, validatedOnly bool) ([]t.Credential, error) {
	query := "SELECT createdat,updatedat,method,value,resp,done,retries FROM credentials WHERE userid=? AND deletedat IS NULL"
	args := []any{store.DecodeUid(uid)}
	if method != "" {
		query += " AND method=?"
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
	err := a.db.SelectContext(ctx, &credentials, query, args...)
	if err != nil {
		return nil, err
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
	} else {
		user = 0
	}
	_, err := a.db.ExecContext(ctx,
		"INSERT INTO fileuploads(id,createdat,updatedat,userid,status,mimetype,size,etag,location) "+
			"VALUES(?,?,?,?,?,?,?,?,?)",
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
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	now := t.TimeNow()
	if success {
		_, err = tx.ExecContext(ctx, "UPDATE fileuploads SET updatedat=?,status=?,size=?,etag=?,location=? WHERE id=?",
			now, t.UploadCompleted, size, fd.ETag, fd.Location, store.DecodeUid(fd.Uid()))
		if err != nil {
			return nil, err
		}

		fd.Status = t.UploadCompleted
		fd.Size = size
	} else {
		// 删除记录：保留在数据库中没有意义。
		_, err = tx.ExecContext(ctx, "DELETE FROM fileuploads WHERE id=?", store.DecodeUid(fd.Uid()))
		if err != nil {
			return nil, err
		}

		fd.Status = t.UploadFailed
		fd.Size = 0
	}
	fd.UpdatedAt = now

	return fd, tx.Commit()
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
	err := a.db.GetContext(ctx, &fd, "SELECT id,createdat,updatedat,userid AS user,status,mimetype,size,IFNULL(etag,'') AS etag,location "+
		"FROM fileuploads WHERE id=?", store.DecodeUid(id))
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	fd.Id = common.EncodeUidString(fd.Id).String()
	fd.User = common.EncodeUidString(fd.User).String()

	return &fd, nil
}

// FileDeleteUnused 删除 UseCount 为零的记录。若 olderThan 非零，则删除
// UpdatedAt 早于 olderThan 的未使用记录。
// 返回已删除文件记录的 FileDef.Location 数组，以便同时删除实际文件。
func (a *adapter) FileDeleteUnused(olderThan time.Time, limit int) ([]string, error) {
	ctx, cancel := a.getContextForTx()
	if cancel != nil {
		defer cancel()
	}
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// Garbage collecting entries which as either marked as deleted, or lack 消息 references, or have no 用户 assigned.
	query := "SELECT fu.id,fu.location FROM fileuploads AS fu LEFT JOIN filemsglinks AS fml ON fml.fileid=fu.id " +
		"WHERE fml.id IS NULL"
	var args []any
	if !olderThan.IsZero() {
		query += " AND fu.updatedat<?"
		args = append(args, olderThan)
	}
	if limit > 0 {
		query += " LIMIT ?"
		args = append(args, limit)
	}

	rows, err := tx.Query(query, args...)
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
		query, ids, _ = sqlx.In("DELETE FROM fileuploads WHERE id IN (?)", ids)
		_, err = tx.Exec(query, ids...)
		if err != nil {
			return nil, err
		}
	}

	return locations, tx.Commit()
}

// FileLinkAttachments connects given Topic or 消息 to the file record IDs from the list.
func (a *adapter) FileLinkAttachments(topic string, userId, msgId t.Uid, fids []string) error {
	if len(fids) == 0 || (topic == "" && msgId.IsZero() && userId.IsZero()) {
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
	tx, err := a.db.BeginTxx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() {
		if err != nil {
			tx.Rollback()
		}
	}()

	// Unlink earlier uploads on the same Topic or 用户 allowing them to be garbage-collected.
	if msgId.IsZero() {
		sql := "DELETE FROM filemsglinks WHERE " + linkBy + "=?"
		_, err = tx.Exec(sql, linkId)
		if err != nil {
			return err
		}
	}

	sql := "INSERT INTO filemsglinks(createdat,fileid," + linkBy + ") VALUES (?,?,?)"
	_, err = tx.Exec(sql+strings.Repeat(",(?,?,?)", len(dids)-1), args...)
	if err != nil {
		return err
	}

	return tx.Commit()
}

// PCacheGet 读取持久缓存条目。
func (a *adapter) PCacheGet(key string) (string, error) {
	ctx, cancel := a.getContext()
	if cancel != nil {
		defer cancel()
	}

	var value string
	if err := a.db.GetContext(ctx, &value, "SELECT `value` FROM kvmeta WHERE `key`=? LIMIT 1", key); err != nil {
		if err == sql.ErrNoRows {
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
	if failOnDuplicate {
		action = "INSERT"
	} else {
		action = "REPLACE"
	}

	_, err := a.db.ExecContext(ctx, action+" INTO kvmeta(`key`,createdat,`value`) VALUES(?,?,?)", key, t.TimeNow(), value)
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

	_, err := a.db.ExecContext(ctx, "DELETE FROM kvmeta WHERE `key`=?", key)
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

	_, err := a.db.ExecContext(ctx, "DELETE FROM kvmeta WHERE `key` LIKE ? AND createdat<?", keyPrefix+"%", olderThan)
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

	myerr, ok := err.(*ms.MySQLError)
	return ok && myerr.Number == 1062
}

func isMissingTable(err error) bool {
	if err == nil {
		return false
	}

	myerr, ok := err.(*ms.MySQLError)
	return ok && myerr.Number == 1146
}

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

func init() {
	store.RegisterAdapter(&adapter{})
}
