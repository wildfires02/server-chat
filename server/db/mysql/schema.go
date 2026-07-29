//go:build mysql
// +build mysql

package mysql

import (
	"database/sql"
	"errors"
	"strconv"

	t "chat/server/store/types"

	ms "github.com/go-sql-driver/mysql"
	"github.com/jmoiron/sqlx"
)

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
			id        BIGINT NOT NULL COMMENT '用户唯一ID',
			createdat DATETIME(3) NOT NULL COMMENT '用户创建时间',
			updatedat DATETIME(3) NOT NULL COMMENT '用户最近更新时间',
			state     SMALLINT NOT NULL DEFAULT 0 COMMENT '用户生命周期状态',
			stateat   DATETIME(3) COMMENT '状态最近变更时间',
			access    JSON COMMENT '用户默认访问权限',
			lastseen  DATETIME COMMENT '用户最近在线时间',
			useragent VARCHAR(255) DEFAULT '' COMMENT '最近在线客户端User-Agent',
			public    JSON COMMENT '对其他用户可见的公开资料',
			trusted   JSON COMMENT '仅可信客户端可见的资料',
			tags      JSON COMMENT '用于发现和搜索的反规范化标签',
			PRIMARY KEY(id),
			INDEX users_state_stateat(state, stateat),
			INDEX users_lastseen_updatedat(lastseen, updatedat)
		) COMMENT='用户账号及公开资料'`); err != nil {
		return err
	}

	// 已索引的用户标签。
	if _, err = tx.Exec(
		`CREATE TABLE usertags(
			id     INT NOT NULL AUTO_INCREMENT COMMENT '标签关联记录ID',
			userid BIGINT NOT NULL COMMENT '关联用户ID',
			tag    VARCHAR(96) NOT NULL COMMENT '标准化用户搜索标签',
			PRIMARY KEY(id),
			FOREIGN KEY(userid) REFERENCES users(id),
			INDEX usertags_tag(tag),
			UNIQUE INDEX usertags_userid_tag(userid, tag)
		) COMMENT='用户与可搜索标签的索引关联'`); err != nil {
		return err
	}

	// 已索引的设备。归一化到单独的表中。
	if _, err = tx.Exec(
		`CREATE TABLE devices(
			id       INT NOT NULL AUTO_INCREMENT COMMENT '设备记录ID',
			userid   BIGINT NOT NULL COMMENT '设备所属用户ID',
			hash     CHAR(16) NOT NULL COMMENT '设备标识的短哈希索引',
			deviceid TEXT NOT NULL COMMENT '推送服务使用的完整设备标识',
			platform VARCHAR(32) COMMENT '客户端平台名称',
			lastseen DATETIME NOT NULL COMMENT '设备最近活跃时间',
			lang     VARCHAR(8) COMMENT '设备首选语言',
			PRIMARY KEY(id),
			FOREIGN KEY(userid) REFERENCES users(id),
			UNIQUE INDEX devices_hash(hash)
		) COMMENT='用户登录设备与推送目标'`); err != nil {
		return err
	}

	// 基础认证方案的认证记录。
	if _, err = tx.Exec(
		`CREATE TABLE auth(
			id      INT NOT NULL AUTO_INCREMENT COMMENT '认证记录ID',
			uname   VARCHAR(32) NOT NULL COMMENT '认证方案内唯一登录名',
			userid  BIGINT NOT NULL COMMENT '认证记录所属用户ID',
			scheme  VARCHAR(16) NOT NULL COMMENT '认证方案名称',
			authlvl INT NOT NULL COMMENT '认证等级',
			secret  VARCHAR(255) NOT NULL COMMENT '认证方案保存的凭据摘要或密文',
			expires DATETIME COMMENT '认证记录过期时间',
			PRIMARY KEY(id),
			FOREIGN KEY(userid) REFERENCES users(id),
			UNIQUE INDEX auth_userid_scheme(userid, scheme),
			UNIQUE INDEX auth_uname(uname)
		) COMMENT='用户认证方案与登录凭据'`); err != nil {
		return err
	}

	// Topic 管理
	if _, err = tx.Exec(
		`CREATE TABLE topics(
			id        INT NOT NULL AUTO_INCREMENT COMMENT 'Topic内部记录ID',
			createdat DATETIME(3) NOT NULL COMMENT 'Topic创建时间',
			updatedat DATETIME(3) NOT NULL COMMENT 'Topic元数据最近更新时间',
			state     SMALLINT NOT NULL DEFAULT 0 COMMENT 'Topic生命周期状态',
			stateat   DATETIME(3) COMMENT '状态最近变更时间',
			touchedat DATETIME(3) COMMENT '最近一条消息通过Topic的时间',
			name      CHAR(25) NOT NULL COMMENT 'Topic全局唯一名称',
			usebt     TINYINT DEFAULT 0 COMMENT '是否使用广播频道语义',
			owner     BIGINT NOT NULL DEFAULT 0 COMMENT 'Topic所有者用户ID',
			access    JSON COMMENT '匿名和认证用户的默认访问权限',
			seqid     INT NOT NULL DEFAULT 0 COMMENT '最新服务端消息序列号',
			clusterowner VARCHAR(64) NOT NULL DEFAULT '' COMMENT '最后持有Topic写入权的集群节点名称',
			clusterepoch BIGINT NOT NULL DEFAULT 0 COMMENT '最后一次Topic写入使用的Cluster View Revision',
			delid     INT DEFAULT 0 COMMENT '最新删除操作序列号',
			subcnt		INT DEFAULT 0 COMMENT '当前有效订阅者数量',
			public    JSON COMMENT '订阅者可见的公共资料',
			trusted   JSON COMMENT '可信客户端可见的资料',
			tags      JSON COMMENT '用于发现和搜索的反规范化标签',
			aux       JSON COMMENT '服务端扩展元数据',
			PRIMARY KEY(id),
			UNIQUE INDEX topics_name(name),
			INDEX topics_owner(owner),
			INDEX topics_state_stateat(state, stateat),
			INDEX topics_name_state_seqid(name, state, seqid)
		) COMMENT='会话、群组和频道的核心状态'`); err != nil {
		return err
	}

	// 创建系统 Topic 'sys'。
	if err = createSystemTopic(tx); err != nil {
		return err
	}

	// 已索引的 Topic 标签。
	if _, err = tx.Exec(
		`CREATE TABLE topictags(
			id    INT NOT NULL AUTO_INCREMENT COMMENT '标签关联记录ID',
			topic CHAR(25) NOT NULL COMMENT '关联Topic名称',
			tag   VARCHAR(96) NOT NULL COMMENT '标准化Topic搜索标签',
			PRIMARY KEY(id),
			FOREIGN KEY(topic) REFERENCES topics(name),
			INDEX topictags_tag(tag),
			UNIQUE INDEX topictags_topic_tag(topic, tag)
		) COMMENT='Topic与可搜索标签的索引关联'`); err != nil {
		return err
	}

	// 订阅
	if _, err = tx.Exec(
		`CREATE TABLE subscriptions(
			id        INT NOT NULL AUTO_INCREMENT COMMENT '订阅记录ID',
			createdat DATETIME(3) NOT NULL COMMENT '订阅创建时间',
			updatedat DATETIME(3) NOT NULL COMMENT '订阅最近更新时间',
			deletedat DATETIME(3) COMMENT '订阅软删除时间',
			userid    BIGINT NOT NULL COMMENT '订阅用户ID',
			topic     CHAR(25) NOT NULL COMMENT '订阅Topic名称',
			delid     INT DEFAULT 0 COMMENT '用户已同步的最新删除操作序列号',
			recvseqid INT DEFAULT 0 COMMENT '用户已送达的最新消息序列号',
			readseqid INT DEFAULT 0 COMMENT '用户已读的最新消息序列号',
			modewant  CHAR(8) COMMENT '用户请求的访问模式',
			modegiven CHAR(8) COMMENT 'Topic授予的访问模式',
			private   JSON COMMENT '仅该订阅用户可见的私有资料',
			PRIMARY KEY(id),
			FOREIGN KEY(userid) REFERENCES users(id),
			UNIQUE INDEX subscriptions_topic_userid(topic, userid),
			INDEX subscriptions_topic(topic),
			INDEX subscriptions_deletedat(deletedat),
			INDEX subscriptions_userid_topic_deletedat(userid, topic, deletedat)
		) COMMENT='用户与Topic之间的订阅、权限及同步游标'`); err != nil {
		return err
	}

	// 消息
	if _, err = tx.Exec(
		`CREATE TABLE messages(
			id        INT NOT NULL AUTO_INCREMENT COMMENT '消息内部记录ID',
			createdat DATETIME(3) NOT NULL COMMENT '消息创建时间',
			updatedat DATETIME(3) NOT NULL COMMENT '消息最近编辑时间',
			deletedat DATETIME(3) COMMENT '消息删除时间',
			delid     INT DEFAULT 0 COMMENT '硬删除操作序列号；0表示未硬删除',
			seqid     INT NOT NULL COMMENT '消息在Topic内的服务端序列号',
			topic     CHAR(25) NOT NULL COMMENT '消息所属Topic名称',` +
			"`from`   BIGINT NOT NULL COMMENT '发送者用户ID'," +
			`clientid VARCHAR(64) COMMENT '客户端生成的发布幂等键',
			clientkey CHAR(43) COMMENT '发送者与clientid生成的唯一索引键',
			head     JSON COMMENT '客户端扩展头和服务端消息元数据',
			content   JSON COMMENT '纯文本或Drafty消息正文',
			searchtext MEDIUMTEXT COMMENT '从消息正文提取并经NFKC归一化的全文搜索文本',
			PRIMARY KEY(id),
			FOREIGN KEY(topic) REFERENCES topics(name),
			UNIQUE INDEX messages_topic_seqid(topic, seqid),
			UNIQUE INDEX messages_topic_clientkey(topic, clientkey),
			INDEX messages_topic_updatedat_seqid(topic,updatedat,seqid)
		) COMMENT='已进入Topic序列的持久化消息'`); err != nil {
		return err
	}

	if _, err = tx.Exec(
		`CREATE TABLE scheduledmessages(
			id        BIGINT NOT NULL COMMENT '服务端生成的定时消息唯一ID',
			createdat DATETIME(3) NOT NULL COMMENT '定时消息创建时间',
			updatedat DATETIME(3) NOT NULL COMMENT '定时消息最近更新时间',
			publishat DATETIME(3) NOT NULL COMMENT '计划投递时间',
			topic     CHAR(25) NOT NULL COMMENT '目标Topic名称',
			` + "`from`" + ` BIGINT NOT NULL COMMENT '发送者用户ID',
			clientid  VARCHAR(64) NOT NULL COMMENT '发送者范围内的客户端幂等键',
			noecho    TINYINT NOT NULL DEFAULT 0 COMMENT '投递时是否跳过发起会话',
			head      JSON COMMENT '已校验的消息头快照',
			content   JSON COMMENT '已校验的消息正文快照',
			attachmenturls JSON COMMENT '投递普通消息时使用的附件URL列表',
			attachments JSON COMMENT '垃圾回收保护使用的文件ID列表',
			PRIMARY KEY(id),
			FOREIGN KEY(topic) REFERENCES topics(name) ON DELETE CASCADE,
			FOREIGN KEY(` + "`from`" + `) REFERENCES users(id) ON DELETE CASCADE,
			UNIQUE INDEX scheduled_topic_from_clientid(topic,` + "`from`" + `,clientid),
			INDEX scheduled_publishat(publishat)
		) COMMENT='尚未进入Topic序列的持久化定时消息队列'`); err != nil {
		return err
	}

	// 删除日志
	if _, err = tx.Exec(
		`CREATE TABLE dellog(
			id         INT NOT NULL AUTO_INCREMENT COMMENT '删除日志记录ID',
			topic      CHAR(25) NOT NULL COMMENT '删除操作所属Topic',
			deletedfor BIGINT NOT NULL DEFAULT 0 COMMENT '软删除目标用户ID；0表示所有用户',
			delid      INT NOT NULL COMMENT '删除操作序列号',
			low        INT NOT NULL COMMENT '被删除消息范围下界（包含）',
			hi         INT NOT NULL COMMENT '被删除消息范围上界（不包含）',
			PRIMARY KEY(id),
			FOREIGN KEY(topic) REFERENCES topics(name),
			INDEX dellog_topic_delid_deletedfor(topic,delid,deletedfor),
			INDEX dellog_topic_deletedfor_low_hi(topic,deletedfor,low,hi),
			INDEX dellog_deletedfor(deletedfor)
		) COMMENT='消息软删除与硬删除的同步日志'`); err != nil {
		return err
	}

	// 用户 credentials
	if _, err = tx.Exec(
		`CREATE TABLE credentials(
			id        INT NOT NULL AUTO_INCREMENT COMMENT '凭据记录ID',
			createdat DATETIME(3) NOT NULL COMMENT '凭据创建时间',
			updatedat DATETIME(3) NOT NULL COMMENT '凭据最近更新时间',
			deletedat DATETIME(3) COMMENT '凭据软删除时间',
			method    VARCHAR(16) NOT NULL COMMENT '凭据类型，如email或tel',
			value     VARCHAR(128) NOT NULL COMMENT '规范化后的凭据值',
			synthetic VARCHAR(192) NOT NULL COMMENT '用于全局唯一约束的合成键',
			userid    BIGINT NOT NULL COMMENT '凭据所属用户ID',
			resp      VARCHAR(255) COMMENT '验证挑战的期望响应摘要',
			done      TINYINT NOT NULL DEFAULT 0 COMMENT '凭据是否已完成验证',
			retries   INT NOT NULL DEFAULT 0 COMMENT '验证失败重试次数',
			PRIMARY KEY(id),
			FOREIGN KEY(userid) REFERENCES users(id),
			UNIQUE credentials_uniqueness(synthetic)
		) COMMENT='用户邮箱、手机号等可验证身份凭据'`); err != nil {
		return err
	}

	// 上传文件的记录。
	// 不要在 userid 上添加外键。不需要，而且会破坏用户删除。
	if _, err = tx.Exec(
		`CREATE TABLE fileuploads(
			id        BIGINT NOT NULL COMMENT '上传文件唯一ID',
			createdat DATETIME(3) NOT NULL COMMENT '上传记录创建时间',
			updatedat DATETIME(3) NOT NULL COMMENT '上传状态最近更新时间',
			userid    BIGINT COMMENT '上传文件的用户ID',
			status    INT NOT NULL COMMENT '上传和垃圾回收状态',
			mimetype  VARCHAR(255) NOT NULL COMMENT '文件MIME类型',
			size      BIGINT NOT NULL COMMENT '文件字节大小',
			etag      VARCHAR(128) COMMENT '对象存储内容校验标签',
			location  VARCHAR(2048) NOT NULL COMMENT '媒体后端中的文件位置',
			PRIMARY KEY(id),
			INDEX fileuploads_status(status)
		) COMMENT='媒体后端中的上传文件元数据'`); err != nil {
		return err
	}

	// 上传文件与所附加的 Topic、用户或消息之间的链接。
	if _, err = tx.Exec(
		`CREATE TABLE filemsglinks(
			id        INT NOT NULL AUTO_INCREMENT COMMENT '文件关联记录ID',
			createdat DATETIME(3) NOT NULL COMMENT '关联创建时间',
			fileid    BIGINT NOT NULL COMMENT '关联文件ID',
			msgid     INT COMMENT '关联消息内部记录ID',
			topic     CHAR(25) COMMENT '关联Topic名称',
			userid    BIGINT COMMENT '关联用户ID',
			PRIMARY KEY(id),
			FOREIGN KEY(fileid) REFERENCES fileuploads(id) ON DELETE CASCADE,
			FOREIGN KEY(msgid) REFERENCES messages(id) ON DELETE CASCADE,
			FOREIGN KEY(topic) REFERENCES topics(name) ON DELETE CASCADE,
			FOREIGN KEY(userid) REFERENCES users(id) ON DELETE CASCADE
		) COMMENT='文件与消息、Topic或用户资料的引用关联'`); err != nil {
		return err
	}
	if _, err = tx.Exec(
		`CREATE TABLE scheduledfilelinks(
			id BIGINT NOT NULL AUTO_INCREMENT COMMENT '关联记录自增ID',
			scheduledid BIGINT NOT NULL COMMENT '定时消息ID',
			fileid BIGINT NOT NULL COMMENT '待投递文件ID',
			PRIMARY KEY(id),
			FOREIGN KEY(scheduledid) REFERENCES scheduledmessages(id) ON DELETE CASCADE,
			FOREIGN KEY(fileid) REFERENCES fileuploads(id) ON DELETE CASCADE,
			UNIQUE INDEX scheduledfilelinks_pair(scheduledid,fileid)
		) COMMENT='定时消息与待投递文件的垃圾回收保护关联'`); err != nil {
		return err
	}

	if _, err = tx.Exec(
		`CREATE TABLE kvmeta(` +
			"`key`       VARCHAR(64) NOT NULL COMMENT '元数据键'," +
			"createdat   DATETIME(3) COMMENT '元数据创建时间'," +
			"`value`     TEXT COMMENT '元数据字符串值'," +
			"PRIMARY KEY(`key`)," +
			"INDEX kvmeta_createdat_key(createdat, `key`)" +
			`) COMMENT='数据库版本及全局键值元数据'`); err != nil {
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

	if a.version == 116 {
		// 为客户端发布重试增加跨重启幂等约束。
		if _, err := a.db.Exec("ALTER TABLE messages ADD COLUMN clientid VARCHAR(64) NULL COMMENT '客户端生成的发布幂等键' AFTER `from`"); err != nil {
			return err
		}
		if _, err := a.db.Exec("ALTER TABLE messages ADD COLUMN clientkey CHAR(43) NULL COMMENT '发送者与clientid生成的唯一索引键' AFTER clientid"); err != nil {
			return err
		}
		if _, err := a.db.Exec("CREATE UNIQUE INDEX messages_topic_clientkey ON messages(topic, clientkey)"); err != nil {
			return err
		}
		if err := bumpVersion(a, 117); err != nil {
			return err
		}
	}

	if a.version == 117 {
		// 数据库 117→118：增加消息修改游标索引、定时队列及附件保护关联表。
		if _, err := a.db.Exec("CREATE INDEX messages_topic_updatedat_seqid ON messages(topic,updatedat,seqid)"); err != nil {
			return err
		}
		if _, err := a.db.Exec(`CREATE TABLE scheduledmessages(
			id BIGINT NOT NULL COMMENT '服务端生成的定时消息唯一ID',
			createdat DATETIME(3) NOT NULL COMMENT '定时消息创建时间',
			updatedat DATETIME(3) NOT NULL COMMENT '定时消息最近更新时间',
			publishat DATETIME(3) NOT NULL COMMENT '计划投递时间',
			topic CHAR(25) NOT NULL COMMENT '目标Topic名称',
			` + "`from`" + ` BIGINT NOT NULL COMMENT '发送者用户ID',
			clientid VARCHAR(64) NOT NULL COMMENT '发送者范围内的客户端幂等键',
			noecho TINYINT NOT NULL DEFAULT 0 COMMENT '投递时是否跳过发起会话',
			head JSON COMMENT '已校验的消息头快照',
			content JSON COMMENT '已校验的消息正文快照',
			attachmenturls JSON COMMENT '投递普通消息时使用的附件URL列表',
			attachments JSON COMMENT '垃圾回收保护使用的文件ID列表',
			PRIMARY KEY(id),
			FOREIGN KEY(topic) REFERENCES topics(name) ON DELETE CASCADE,
			FOREIGN KEY(` + "`from`" + `) REFERENCES users(id) ON DELETE CASCADE,
			UNIQUE INDEX scheduled_topic_from_clientid(topic,` + "`from`" + `,clientid),
			INDEX scheduled_publishat(publishat)
		) COMMENT='尚未进入Topic序列的持久化定时消息队列'`); err != nil {
			return err
		}
		if _, err := a.db.Exec(`CREATE TABLE scheduledfilelinks(
			id BIGINT NOT NULL AUTO_INCREMENT COMMENT '关联记录自增ID',
			scheduledid BIGINT NOT NULL COMMENT '定时消息ID',
			fileid BIGINT NOT NULL COMMENT '待投递文件ID',
			PRIMARY KEY(id),
			FOREIGN KEY(scheduledid) REFERENCES scheduledmessages(id) ON DELETE CASCADE,
			FOREIGN KEY(fileid) REFERENCES fileuploads(id) ON DELETE CASCADE,
			UNIQUE INDEX scheduledfilelinks_pair(scheduledid,fileid)
		) COMMENT='定时消息与待投递文件的垃圾回收保护关联'`); err != nil {
			return err
		}
		if err := bumpVersion(a, 118); err != nil {
			return err
		}
	}

	if a.version == 118 {
		// 数据库 118→119：增加跨数据库一致的消息搜索文本。
		if _, err := a.db.Exec("ALTER TABLE messages ADD COLUMN searchtext MEDIUMTEXT NULL COMMENT '从消息正文提取并经NFKC归一化的全文搜索文本' AFTER content"); err != nil {
			return err
		}
		if _, err := a.db.Exec(`UPDATE messages SET searchtext=CASE
			WHEN JSON_TYPE(content)='STRING' THEN JSON_UNQUOTE(content)
			WHEN JSON_TYPE(content)='OBJECT' THEN COALESCE(JSON_UNQUOTE(JSON_EXTRACT(content,'$.txt')),'')
			ELSE '' END`); err != nil {
			return err
		}
		if err := bumpVersion(a, 119); err != nil {
			return err
		}
	}

	if a.version == 119 {
		// 数据库 119→120：保存 Topic 当前 Owner 与 fencing epoch，阻断旧 Owner 双写。
		if _, err := a.db.Exec(`ALTER TABLE topics
			ADD COLUMN clusterowner VARCHAR(64) NOT NULL DEFAULT '' COMMENT '最后持有Topic写入权的集群节点名称' AFTER seqid,
			ADD COLUMN clusterepoch BIGINT NOT NULL DEFAULT 0 COMMENT '最后一次Topic写入使用的Cluster View Revision' AFTER clusterowner`); err != nil {
			return err
		}
		if err := bumpVersion(a, 120); err != nil {
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

// addTags 向当前集合添加Tags。
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

// removeTags 删除或清理Tags。
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
