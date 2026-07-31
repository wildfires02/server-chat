# THIS SCHEMA FILE IS FOR REFERENCE/DOCUMENTATION ONLY!
# DO NOT USE IT TO INITIALIZE THE DATABASE.
# Read installation instructions first.

# The following line will produce an intentional error.

'READ INSTALLATION INSTRUCTIONS!';

# The actual schema is below.

DROP DATABASE IF EXISTS im;

CREATE DATABASE im CHARACTER SET utf8mb4 COLLATE utf8mb4_0900_ai_ci;

USE im;


CREATE TABLE kvmeta(
	`key` VARCHAR(64) COMMENT '元数据键',
	createdat DATETIME(3) COMMENT '元数据创建时间',
	`value` TEXT COMMENT '元数据字符串值',
	PRIMARY KEY(`key`),
	INDEX kvmeta_createdat_key(createdat, `key`)
) COMMENT='数据库版本及全局键值元数据';

INSERT INTO kvmeta(`key`, `value`) VALUES("version", "122");

CREATE TABLE users(
	id 			BIGINT NOT NULL COMMENT '用户唯一ID',
	createdat 	DATETIME(3) NOT NULL COMMENT '用户创建时间',
	updatedat 	DATETIME(3) NOT NULL COMMENT '用户最近更新时间',
	state 		SMALLINT NOT NULL DEFAULT 0 COMMENT '用户生命周期状态',
	stateat 	DATETIME(3) COMMENT '状态最近变更时间',
	access 		JSON COMMENT '用户默认访问权限',
	lastseen 	DATETIME COMMENT '用户最近在线时间',
	useragent 	VARCHAR(255) DEFAULT '' COMMENT '最近在线客户端User-Agent',
	public 		JSON COMMENT '对其他用户可见的公开资料',
	tags		JSON COMMENT '用于发现和搜索的反规范化标签',

	PRIMARY KEY(id),
	INDEX users_state_stateat(state, stateat),
	INDEX users_lastseen_updatedat(lastseen, updatedat)
) COMMENT='用户账号及公开资料';

# Indexed user tags.
CREATE TABLE usertags(
	id 		INT NOT NULL AUTO_INCREMENT COMMENT '标签关联记录ID',
	userid 	BIGINT NOT NULL COMMENT '关联用户ID',
	tag 	VARCHAR(96) NOT NULL COMMENT '标准化用户搜索标签',

	PRIMARY KEY(id),
	FOREIGN KEY(userid) REFERENCES users(id),
	INDEX usertags_tag(tag),
	UNIQUE INDEX usertags_userid_tag(userid, tag)
) COMMENT='用户与可搜索标签的索引关联';

# Indexed devices. Normalized into a separate table.
CREATE TABLE devices(
	id 			INT NOT NULL AUTO_INCREMENT COMMENT '设备记录ID',
	userid 		BIGINT NOT NULL COMMENT '设备所属用户ID',
	hash 		CHAR(16) NOT NULL COMMENT '设备标识的短哈希索引',
	deviceid 	TEXT NOT NULL COMMENT '推送服务使用的完整设备标识',
	platform	VARCHAR(32) COMMENT '客户端平台名称',
	lastseen 	DATETIME NOT NULL COMMENT '设备最近活跃时间',
	lang 		VARCHAR(8) COMMENT '设备首选语言',

	PRIMARY KEY(id),
	FOREIGN KEY(userid) REFERENCES users(id),
	UNIQUE INDEX devices_hash(hash)
) COMMENT='用户登录设备与推送目标';

# Authentication records for the basic authentication scheme.
CREATE TABLE auth(
	id 		INT NOT NULL AUTO_INCREMENT COMMENT '认证记录ID',
	uname	VARCHAR(32) NOT NULL COMMENT '认证方案内唯一登录名',
	userid 	BIGINT NOT NULL COMMENT '认证记录所属用户ID',
	scheme	VARCHAR(16) NOT NULL COMMENT '认证方案名称',
	authlvl	SMALLINT NOT NULL COMMENT '认证等级',
	secret 	VARCHAR(255) NOT NULL COMMENT '认证方案保存的凭据摘要或密文',
	expires DATETIME COMMENT '认证记录过期时间',

	PRIMARY KEY(id),
	FOREIGN KEY(userid) REFERENCES users(id),
	UNIQUE INDEX auth_userid_scheme(userid, scheme),
	UNIQUE INDEX auth_uname (uname)
) COMMENT='用户认证方案与登录凭据';


# Topics
CREATE TABLE topics(
	id			INT NOT NULL AUTO_INCREMENT COMMENT 'Topic内部记录ID',
	createdat 	DATETIME(3) NOT NULL COMMENT 'Topic创建时间',
	updatedat 	DATETIME(3) NOT NULL COMMENT 'Topic元数据最近更新时间',
	touchedat 	DATETIME(3) COMMENT '最近一条消息通过Topic的时间',
	state		SMALLINT NOT NULL DEFAULT 0 COMMENT 'Topic生命周期状态',
	stateat		DATETIME(3) COMMENT '状态最近变更时间',
	name		CHAR(25) NOT NULL COMMENT 'Topic全局唯一名称',
	usebt		TINYINT DEFAULT 0 COMMENT '是否使用广播频道语义',
	owner		BIGINT NOT NULL DEFAULT 0 COMMENT 'Topic所有者用户ID',
	access		JSON COMMENT '匿名和认证用户的默认访问权限',
	seqid		INT NOT NULL DEFAULT 0 COMMENT '最新服务端消息序列号',
	clusterowner VARCHAR(64) NOT NULL DEFAULT '' COMMENT '最后持有Topic写入权的集群节点名称',
	clusterepoch BIGINT NOT NULL DEFAULT 0 COMMENT '最后一次Topic写入使用的Cluster View Revision',
	delid		INT DEFAULT 0 COMMENT '最新删除操作序列号',
	subcnt  INT DEFAULT 0 COMMENT '当前有效订阅者数量',
	public	JSON COMMENT '订阅者可见的公共资料',
	trusted	JSON COMMENT '可信客户端可见的资料',
	tags		JSON COMMENT '用于发现和搜索的反规范化标签',
	aux			JSON COMMENT '服务端扩展元数据',

	PRIMARY KEY(id),
	UNIQUE INDEX topics_name (name),
	INDEX topics_owner(owner),
	INDEX topics_state_stateat(state, stateat),
	INDEX topics_name_state_seqid ON topics(name, state, seqid)
) COMMENT='会话、群组和频道的核心状态';

# Indexed topic tags.
CREATE TABLE topictags(
	id 		INT NOT NULL AUTO_INCREMENT COMMENT '标签关联记录ID',
	topic 	CHAR(25) NOT NULL COMMENT '关联Topic名称',
	tag 	VARCHAR(96) NOT NULL COMMENT '标准化Topic搜索标签',

	PRIMARY KEY(id),
	FOREIGN KEY(topic) REFERENCES topics(name),
	INDEX topictags_tag (tag),
	UNIQUE INDEX topictags_topic_tag(topic, tag)
) COMMENT='Topic与可搜索标签的索引关联';

# Subscriptions
CREATE TABLE subscriptions(
	id			INT NOT NULL AUTO_INCREMENT COMMENT '订阅记录ID',
	createdat	DATETIME(3) NOT NULL COMMENT '订阅创建时间',
	updatedat	DATETIME(3) NOT NULL COMMENT '订阅最近更新时间',
	deletedat	DATETIME(3) COMMENT '订阅软删除时间',
	userid		BIGINT NOT NULL COMMENT '订阅用户ID',
	topic		CHAR(25) NOT NULL COMMENT '订阅Topic名称',
	delid		INT DEFAULT 0 COMMENT '用户已同步的最新删除操作序列号',
	recvseqid	INT DEFAULT 0 COMMENT '用户已送达的最新消息序列号',
	readseqid	INT DEFAULT 0 COMMENT '用户已读的最新消息序列号',
	readhistory JSON COMMENT '最近七天逐消息已读时间检查点',
	modewant	CHAR(8) COMMENT '用户请求的访问模式',
	modegiven	CHAR(8) COMMENT 'Topic授予的访问模式',
	private		JSON COMMENT '仅该订阅用户可见的私有资料',

	PRIMARY KEY(id)	,
	FOREIGN KEY(userid) REFERENCES users(id),
	UNIQUE INDEX subscriptions_topic_userid(topic, userid),
	INDEX subscriptions_topic(topic),
	INDEX subscriptions_deletedat(deletedat),
	INDEX subscriptions_user_topic_deletedat ON subscriptions(userid, topic, deletedat)
) COMMENT='用户与Topic之间的订阅、权限及同步游标';

# Messages
CREATE TABLE messages(
	id 			INT NOT NULL AUTO_INCREMENT COMMENT '消息内部记录ID',
	createdat 	DATETIME(3) NOT NULL COMMENT '消息创建时间',
	updatedat 	DATETIME(3) NOT NULL COMMENT '消息最近编辑时间',
	deletedat 	DATETIME(3) COMMENT '消息删除时间',
	delid 		INT DEFAULT 0 COMMENT '硬删除操作序列号；0表示未硬删除',
	seqid 		INT NOT NULL COMMENT '消息在Topic内的服务端序列号',
	topic 		CHAR(25) NOT NULL COMMENT '消息所属Topic名称',
	`from` 		BIGINT NOT NULL COMMENT '发送者用户ID',
	clientid	VARCHAR(64) COMMENT '客户端生成的发布幂等键',
	clientkey	CHAR(43) COMMENT '发送者与clientid生成的唯一索引键',
	head 		JSON COMMENT '客户端扩展头和服务端消息元数据',
	content 	JSON COMMENT '纯文本或Drafty消息正文',
	searchtext MEDIUMTEXT COMMENT '从消息正文提取并经NFKC归一化的全文搜索文本',

	PRIMARY KEY(id),
	FOREIGN KEY(topic) REFERENCES topics(name),
	UNIQUE INDEX messages_topic_seqid (topic, seqid),
	UNIQUE INDEX messages_topic_clientkey (topic, clientkey),
	INDEX messages_topic_updatedat_seqid(topic, updatedat, seqid)
) COMMENT='已进入Topic序列的持久化消息';

# 定时消息在真正投递时才获取 Topic seqid。
CREATE TABLE scheduledmessages(
	id			BIGINT NOT NULL COMMENT '服务端生成的定时消息唯一ID',
	createdat	DATETIME(3) NOT NULL COMMENT '定时消息创建时间',
	updatedat	DATETIME(3) NOT NULL COMMENT '定时消息最近更新时间',
	publishat	DATETIME(3) NOT NULL COMMENT '计划投递时间',
	topic		CHAR(25) NOT NULL COMMENT '目标Topic名称',
	`from`		BIGINT NOT NULL COMMENT '发送者用户ID',
	clientid	VARCHAR(64) NOT NULL COMMENT '发送者范围内的客户端幂等键',
	noecho		TINYINT NOT NULL DEFAULT 0 COMMENT '投递时是否跳过发起会话',
	head		JSON COMMENT '已校验的消息头快照',
	content		JSON COMMENT '已校验的消息正文快照',
	attachmenturls JSON COMMENT '投递普通消息时使用的附件URL列表',
	attachments JSON COMMENT '垃圾回收保护使用的文件ID列表',

	PRIMARY KEY(id),
	FOREIGN KEY(topic) REFERENCES topics(name) ON DELETE CASCADE,
	FOREIGN KEY(`from`) REFERENCES users(id) ON DELETE CASCADE,
	UNIQUE INDEX scheduled_topic_from_clientid(topic, `from`, clientid),
	INDEX scheduled_publishat(publishat)
) COMMENT='尚未进入Topic序列的持久化定时消息队列';

# Deletion log
CREATE TABLE dellog(
	id			INT NOT NULL AUTO_INCREMENT COMMENT '删除日志记录ID',
	topic		CHAR(25) NOT NULL COMMENT '删除操作所属Topic',
	deletedfor	BIGINT NOT NULL DEFAULT 0 COMMENT '软删除目标用户ID；0表示所有用户',
	delid		INT NOT NULL COMMENT '删除操作序列号',
	low			INT NOT NULL COMMENT '被删除消息范围下界（包含）',
	hi			INT NOT NULL COMMENT '被删除消息范围上界（不包含）',

	PRIMARY KEY(id),
	FOREIGN KEY(topic) REFERENCES topics(name),
	# For getting the list of deleted message ranges
	INDEX dellog_topic_delid_deletedfor(topic,delid,deletedfor),
	# Used when getting not-yet-deleted messages(messages LEFT JOIN dellog)
	INDEX dellog_topic_deletedfor_low_hi(topic,deletedfor,low,hi),
	# Used when deleting a user
	INDEX dellog_deletedfor(deletedfor)
) COMMENT='消息软删除与硬删除的同步日志';

# User credentials
CREATE TABLE credentials(
	id			INT NOT NULL AUTO_INCREMENT COMMENT '凭据记录ID',
	createdat	DATETIME(3) NOT NULL COMMENT '凭据创建时间',
	updatedat	DATETIME(3) NOT NULL COMMENT '凭据最近更新时间',
	deletedat	DATETIME(3) COMMENT '凭据软删除时间',
	method 		VARCHAR(16) NOT NULL COMMENT '凭据类型，如email或tel',
	value		VARCHAR(128) NOT NULL COMMENT '规范化后的凭据值',
	synthetic	VARCHAR(192) NOT NULL COMMENT '用于全局唯一约束的合成键',
	userid 		BIGINT NOT NULL COMMENT '凭据所属用户ID',
	resp		VARCHAR(255) NOT NULL COMMENT '验证挑战的期望响应摘要',
	done		TINYINT NOT NULL DEFAULT 0 COMMENT '凭据是否已完成验证',
	retries		INT NOT NULL DEFAULT 0 COMMENT '验证失败重试次数',

	PRIMARY KEY(id),
	UNIQUE credentials_uniqueness(synthetic),
	FOREIGN KEY(userid) REFERENCES users(id),
) COMMENT='用户邮箱、手机号等可验证身份凭据';

# Records of uploaded files. Files themselves are stored elsewhere.
CREATE TABLE fileuploads(
	id				BIGINT NOT NULL COMMENT '上传文件唯一ID',
	createdat	DATETIME(3) NOT NULL COMMENT '上传记录创建时间',
	updatedat	DATETIME(3) NOT NULL COMMENT '上传状态最近更新时间',
	userid		BIGINT COMMENT '上传文件的用户ID',
	status		INT NOT NULL COMMENT '上传和垃圾回收状态',
	mimetype	VARCHAR(255) NOT NULL COMMENT '文件MIME类型',
	size			BIGINT NOT NULL COMMENT '文件字节大小',
	location	VARCHAR(2048) NOT NULL COMMENT '媒体后端中的文件位置',
	etag			VARCHAR(128) COMMENT '对象存储内容校验标签',

	PRIMARY KEY(id),
	INDEX fileuploads_status(status)
) COMMENT='媒体后端中的上传文件元数据';

# Links between uploaded files and messages or topics.
CREATE TABLE filemsglinks(
	id			INT NOT NULL AUTO_INCREMENT COMMENT '文件关联记录ID',
	createdat	DATETIME(3) NOT NULL COMMENT '关联创建时间',
	fileid		BIGINT NOT NULL COMMENT '关联文件ID',
	msgid		INT COMMENT '关联消息内部记录ID',
	topic		CHAR(25) COMMENT '关联Topic名称',
	userid		BIGINT COMMENT '关联用户ID',

	PRIMARY KEY(id),
	FOREIGN KEY(fileid) REFERENCES fileuploads(id) ON DELETE CASCADE,
	FOREIGN KEY(msgid) REFERENCES messages(id) ON DELETE CASCADE,
	FOREIGN KEY(topicid) REFERENCES topics(id) ON DELETE CASCADE,
	FOREIGN KEY(userid) REFERENCES users(id) ON DELETE CASCADE
) COMMENT='文件与消息、Topic或用户资料的引用关联';

CREATE TABLE scheduledfilelinks(
	id			BIGINT NOT NULL AUTO_INCREMENT COMMENT '关联记录自增ID',
	scheduledid BIGINT NOT NULL COMMENT '定时消息ID',
	fileid		BIGINT NOT NULL COMMENT '待投递文件ID',

	PRIMARY KEY(id),
	FOREIGN KEY(scheduledid) REFERENCES scheduledmessages(id) ON DELETE CASCADE,
	FOREIGN KEY(fileid) REFERENCES fileuploads(id) ON DELETE CASCADE,
	UNIQUE INDEX scheduledfilelinks_pair(scheduledid, fileid)
) COMMENT='定时消息与待投递文件的垃圾回收保护关联';
