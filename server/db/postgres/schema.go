//go:build postgres
// +build postgres

package postgres

import (
	"context"
	"errors"
	"fmt"
	pgx "github.com/jackc/pgx/v5"
	"strconv"

	t "chat/server/store/types"

	"github.com/jackc/pgx/v5/pgxpool"
)

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
			clusterowner VARCHAR(64) NOT NULL DEFAULT '',
			clusterepoch BIGINT NOT NULL DEFAULT 0,
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
		COMMENT ON COLUMN topics.clusterowner IS '最后持有Topic写入权的集群节点名称';
		COMMENT ON COLUMN topics.clusterepoch IS '最后一次Topic写入使用的Cluster View Revision';
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

	if a.version == 119 {
		// 数据库 119→120：保存 Topic 当前 Owner 与 fencing epoch，阻断旧 Owner 双写。
		if _, err := a.db.Exec(ctx, `ALTER TABLE topics
				ADD COLUMN clusterowner VARCHAR(64) NOT NULL DEFAULT '',
				ADD COLUMN clusterepoch BIGINT NOT NULL DEFAULT 0;
			COMMENT ON COLUMN topics.clusterowner IS '最后持有Topic写入权的集群节点名称';
			COMMENT ON COLUMN topics.clusterepoch IS '最后一次Topic写入使用的Cluster View Revision';`); err != nil {
			return err
		}
		if err := bumpVersion(a, 120); err != nil {
			return err
		}
	}

	if a.version == 120 {
		// 数据库 120→121：subscriptions_topic_userid 已覆盖官方大群成员游标分页，
		// 无需新增重复索引，仅同步数据库版本。
		if err := bumpVersion(a, 121); err != nil {
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
