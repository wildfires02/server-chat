//go:build mongodb

// Package mongodb 是 MongoDB 的数据库适配器。
package mongodb

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"chat/server/auth"
	"chat/server/db/common"
	"chat/server/store"
	t "chat/server/store/types"

	b "go.mongodb.org/mongo-driver/bson"
	"go.mongodb.org/mongo-driver/bson/primitive"
	mdb "go.mongodb.org/mongo-driver/mongo"
	mdbopts "go.mongodb.org/mongo-driver/mongo/options"
)

// adapter 保存 MongoDB 连接数据。
type adapter struct {
	conn   *mdb.Client
	db     *mdb.Database
	dbName string
	// 最大返回记录数
	maxResults int
	// 最大返回消息记录数
	maxMessageResults int
	version           int
	ctx               context.Context
	useTransactions   bool
}

const (
	adpVersion  = 116
	adapterName = "mongodb"

	defaultHost     = "localhost:27017"
	defaultDatabase = "im"

	defaultMaxResults = 1024
	// 此值受 Session 发送队列上限 (128) 限制。
	defaultMaxMessageResults = 100

	defaultAuthMechanism = "SCRAM-SHA-256"
	defaultAuthSource    = "admin"
)

// 参见 https://godoc.org/go.mongodb.org/mongo-driver/mongo/options#ClientOptions 了解说明。
type configType struct {
	// 连接字符串 URI https://www.mongodb.com/docs/manual/reference/connection-string/
	Uri            string `json:"uri,omitempty"`
	Addresses      any    `json:"addresses,omitempty"`
	ConnectTimeout int    `json:"timeout,omitempty"`

	// 独立于 ClientOptions 的选项（自定义选项）：
	Database   string `json:"database,omitempty"`
	ReplicaSet string `json:"replica_set,omitempty"`

	AuthMechanism string `json:"auth_mechanism,omitempty"`
	AuthSource    string `json:"auth_source,omitempty"`
	Username      string `json:"username,omitempty"`
	Password      string `json:"password,omitempty"`

	UseTLS             bool   `json:"tls,omitempty"`
	TlsCertFile        string `json:"tls_cert_file,omitempty"`
	TlsPrivateKey      string `json:"tls_private_key,omitempty"`
	InsecureSkipVerify bool   `json:"tls_skip_verify,omitempty"`

	// 目前唯一支持的版本是 "1"。
	APIVersion mdbopts.ServerAPIVersion `json:"api_version,omitempty"`
}

func (a *adapter) maybeStartTransaction(sess mdb.Session) error {
	if a.useTransactions {
		return sess.StartTransaction()
	}
	return nil
}

func (a *adapter) maybeCommitTransaction(ctx context.Context, sess mdb.Session) error {
	if a.useTransactions {
		return sess.CommitTransaction(ctx)
	}
	return nil
}

// Open 初始化 MongoDB Session
func (a *adapter) Open(jsonconfig json.RawMessage) error {
	if a.conn != nil {
		return errors.New("adapter mongodb is already connected")
	}

	if len(jsonconfig) < 2 {
		return errors.New("adapter mongodb missing config")
	}

	var err error
	var config configType
	if err = json.Unmarshal(jsonconfig, &config); err != nil {
		return errors.New("adapter mongodb failed to parse config: " + err.Error())
	}

	var opts mdbopts.ClientOptions

	if config.Addresses == nil {
		opts.SetHosts([]string{defaultHost})
	} else if host, ok := config.Addresses.(string); ok {
		opts.SetHosts([]string{host})
	} else if ihosts, ok := config.Addresses.([]any); ok && len(ihosts) > 0 {
		hosts := make([]string, len(ihosts))
		for i, ih := range ihosts {
			h, ok := ih.(string)
			if !ok || h == "" {
				return errors.New("adapter mongodb invalid config.Addresses value")
			}
			hosts[i] = h
		}
		opts.SetHosts(hosts)
	} else {
		return errors.New("adapter mongodb failed to parse config.Addresses")
	}

	if config.Database == "" {
		a.dbName = defaultDatabase
	} else {
		a.dbName = config.Database
	}

	if config.ReplicaSet != "" {
		opts.SetReplicaSet(config.ReplicaSet)
		a.useTransactions = true
	} else {
		// 独立实例不支持可重试写入。
		opts.SetRetryWrites(false)
	}

	if config.Username != "" {
		if config.AuthMechanism == "" {
			config.AuthMechanism = defaultAuthMechanism
		}
		if config.AuthSource == "" {
			config.AuthSource = defaultAuthSource
		}
		var passwordSet bool
		if config.Password != "" {
			passwordSet = true
		}
		opts.SetAuth(
			mdbopts.Credential{
				AuthMechanism: config.AuthMechanism,
				AuthSource:    config.AuthSource,
				Username:      config.Username,
				Password:      config.Password,
				PasswordSet:   passwordSet,
			})
	}

	if config.UseTLS {
		tlsConfig := tls.Config{
			InsecureSkipVerify: config.InsecureSkipVerify,
		}

		if config.TlsCertFile != "" {
			cert, err := tls.LoadX509KeyPair(config.TlsCertFile, config.TlsPrivateKey)
			if err != nil {
				return err
			}

			tlsConfig.Certificates = append(tlsConfig.Certificates, cert)
		}

		opts.SetTLSConfig(&tlsConfig)
	}

	if a.maxResults <= 0 {
		a.maxResults = defaultMaxResults
	}

	if a.maxMessageResults <= 0 {
		a.maxMessageResults = defaultMaxMessageResults
	}

	// 连接字符串 URI 会覆盖之前配置的所有其他选项。
	if config.Uri != "" {
		opts.ApplyURI(config.Uri)
	}

	if config.APIVersion != "" {
		opts.SetServerAPIOptions(mdbopts.ServerAPI(config.APIVersion))
	}

	// 确保选项合理。
	if err = opts.Validate(); err != nil {
		return err
	}

	a.ctx = context.Background()
	a.conn, err = mdb.Connect(a.ctx, &opts)
	a.db = a.conn.Database(a.dbName)
	if err != nil {
		return err
	}
	a.version = -1

	return nil
}

// Close 关闭适配器
func (a *adapter) Close() error {
	var err error
	if a.conn != nil {
		err = a.conn.Disconnect(a.ctx)
		a.conn = nil
		a.version = -1
	}
	return err
}

// IsOpen 检查适配器是否已准备好使用
func (a *adapter) IsOpen() bool {
	return a.conn != nil
}

// GetDbVersion 返回当前数据库版本。
func (a *adapter) GetDbVersion() (int, error) {
	if a.version > 0 {
		return a.version, nil
	}

	var result struct {
		Key   string `bson:"_id"`
		Value int
	}
	if err := a.db.Collection("kvmeta").FindOne(a.ctx, b.M{"_id": "version"}).Decode(&result); err != nil {
		if err == mdb.ErrNoDocuments {
			err = errors.New("Database not initialized")
		}
		return -1, err
	}

	a.version = result.Value
	return result.Value, nil
}

func (a *adapter) updateDbVersion(v int) error {
	a.version = -1
	_, err := a.db.Collection("kvmeta").UpdateOne(a.ctx,
		b.M{"_id": "version"},
		b.M{"$set": b.M{"value": v}},
	)
	return err
}

// CheckDbVersion 检查实际数据库版本是否与适配器版本匹配。
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

// Version 返回适配器版本
func (a *adapter) Version() int {
	return adpVersion
}

// Stats 返回数据库连接统计对象。
func (a *adapter) Stats() any {
	if a.db == nil {
		return nil
	}

	var result b.M
	if err := a.db.RunCommand(a.ctx, b.D{{Key: "serverStatus", Value: 1}}, nil).Decode(&result); err != nil {
		return nil
	}

	return result["connections"]
}

// GetName 返回适配器名称
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

// CreateDb 创建数据库，可选先删除已有数据库。
func (a *adapter) CreateDb(reset bool) error {
	if reset {
		if err := a.db.Drop(a.ctx); err != nil {
			return err
		}
	} else if a.isDbInitialized() {
		return errors.New("Database already initialized")
	}
	// 集合（表）无需显式创建，MongoDB 会在首次写入时自动创建

	indexes := []struct {
		Collection string
		Field      string
		IndexOpts  mdb.IndexModel
	}{
		// 用户
		// '用户.state' 索引，用于查找已挂起和软删除的用户。
		{
			Collection: "users",
			Field:      "state",
		},
		// '用户.tags' 数组索引，以便通过标签查找用户。
		{
			Collection: "users",
			Field:      "tags",
		},
		// '用户.devices.deviceid' 索引，确保设备 ID 跨用户唯一。
		// 部分过滤表达式避免对 null 值施加唯一约束（当用户对象没有设备时）。
		{
			Collection: "users",
			IndexOpts: mdb.IndexModel{
				Keys: b.M{"devices.deviceid": 1},
				Options: mdbopts.Index().
					SetUnique(true).
					SetPartialFilterExpression(b.M{"devices.deviceid": b.M{"$exists": true}}),
			},
		},
		// lastSeen 和 updatedat 索引，用于删除过期用户账户。
		{
			Collection: "users",
			IndexOpts:  mdb.IndexModel{Keys: b.D{{Key: "lastseen", Value: 1}, {Key: "updatedat", Value: 1}}},
		},

		// 用户认证记录 {_id, userid, secret}
		// 需要能够通过用户 ID 访问用户的认证记录
		{
			Collection: "auth",
			Field:      "userid",
		},

		// Topic 订阅。主键是 Topic:用户 字符串
		{
			Collection: "subscriptions",
			Field:      "user",
		},
		{
			Collection: "subscriptions",
			Field:      "topic",
		},

		// 存储在数据库中的 Topic
		// 'owner' 字段索引，用于删除用户。
		{
			Collection: "topics",
			Field:      "owner",
		},
		// 'state' 索引，用于查找已挂起和软删除的 Topic。
		{
			Collection: "topics",
			Field:      "state",
		},
		// 'Topic.tags' 数组索引，以便通过标签查找 Topic。
		// 这些标签不像 '用户.tags' 那样唯一。
		{
			Collection: "topics",
			Field:      "tags",
		},

		// 存储的消息
		// 'Topic - seqid' 复合索引，用于选择 Topic 中的消息。
		{
			Collection: "messages",
			IndexOpts:  mdb.IndexModel{Keys: b.D{{Key: "topic", Value: 1}, {Key: "seqid", Value: 1}}},
		},
		// 硬删除消息的复合索引
		{
			Collection: "messages",
			IndexOpts:  mdb.IndexModel{Keys: b.D{{Key: "topic", Value: 1}, {Key: "delid", Value: 1}}},
		},
		// 软删除消息的复合多索引：每条消息获得多个复合索引条目，如
		//			 // [Topic, user1, delid1], [Topic, user2, delid2],...
		{
			Collection: "messages",
			IndexOpts:  mdb.IndexModel{Keys: b.D{{Key: "topic", Value: 1}, {Key: "deletedfor.user", Value: 1}, {Key: "deletedfor.delid", Value: 1}}},
		},

		// 已删除消息的日志
		// 'Topic - delid' 复合索引
		{
			Collection: "dellog",
			IndexOpts:  mdb.IndexModel{Keys: b.D{{Key: "topic", Value: 1}, {Key: "delid", Value: 1}}},
		},

		// 用户凭据 - 联系方式，如 "email:jdoe@example.com" 或 "tel:+18003287448"：
		// Id: "method:credential"，如 "email:jdoe@example.com"。参见 types.Credential。
		// 'credentials.用户' 索引，以便通过用户 ID 查询凭据。
		{
			Collection: "credentials",
			Field:      "user",
		},

		// 文件上传记录。参见 types.FileDef。
		// 'fileuploads.usecount' 索引，以便批量删除未使用的记录。
		{
			Collection: "fileuploads",
			Field:      "usecount",
		},
	}

	var err error
	for _, idx := range indexes {
		if idx.Field != "" {
			_, err = a.db.Collection(idx.Collection).Indexes().CreateOne(a.ctx, mdb.IndexModel{Keys: b.M{idx.Field: 1}})
		} else {
			_, err = a.db.Collection(idx.Collection).Indexes().CreateOne(a.ctx, idx.IndexOpts)
		}
		if err != nil {
			return err
		}
	}

	// 元数据键值对集合 "kvmeta"。
	// 键在 "_id" 字段中。
	// 记录当前数据库版本。
	if _, err := a.db.Collection("kvmeta").InsertOne(a.ctx, map[string]any{"_id": "version", "value": adpVersion}); err != nil {
		return err
	}

	// 创建系统 Topic 'sys'。
	return createSystemTopic(a)
}

// UpgradeDb 将数据库升级到当前适配器版本。
func (a *adapter) UpgradeDb() error {
	bumpVersion := func(a *adapter, x int) error {
		if err := a.updateDbVersion(x); err != nil {
			return err
		}
		_, err := a.GetDbVersion()
		return err
	}

	_, err := a.GetDbVersion()
	if err != nil {
		return err
	}

	if a.version == 110 {
		// 执行数据库从版本 110 升级到版本 111。

		// 用户

		// 将之前未使用的 State 字段重置为 StateOK 值。
		if _, err := a.db.Collection("users").UpdateMany(a.ctx,
			b.M{},
			b.M{"$set": b.M{"state": t.StateOK}}); err != nil {
			return err
		}

		// 为所有已删除的用户（DeletedAt 非空）添加 StatusDeleted。
		if _, err := a.db.Collection("users").UpdateMany(a.ctx,
			b.M{"deletedat": b.M{"$ne": nil}},
			b.M{"$set": b.M{"state": t.StateDeleted}}); err != nil {
			return err
		}

		// 将 DeletedAt 重命名为 StateAt。仅更新已定义 DeletedAt 的行。
		if _, err := a.db.Collection("users").UpdateMany(a.ctx,
			b.M{"deletedat": b.M{"$exists": true}},
			b.M{"$rename": b.M{"deletedat": "stateat"}}); err != nil {
			return err
		}

		// 删除二级索引 DeletedAt。
		if _, err := a.db.Collection("users").Indexes().DropOne(a.ctx, "deletedat_1"); err != nil {
			return err
		}

		// 在 State 上创建二级索引，用于查找已挂起和软删除的 Topic。
		if _, err = a.db.Collection("users").Indexes().CreateOne(a.ctx, mdb.IndexModel{Keys: b.M{"state": 1}}); err != nil {
			return err
		}

		// Topic 管理

		// 为所有 DeletedAt 非空的 Topic 添加 StateDeleted。
		if _, err := a.db.Collection("topics").UpdateMany(a.ctx,
			b.M{"deletedat": b.M{"$ne": nil}},
			b.M{"$set": b.M{"state": t.StateDeleted}}); err != nil {
			return err
		}

		// 为所有其他 Topic 设置 StateOK。
		if _, err := a.db.Collection("topics").UpdateMany(a.ctx,
			b.M{"state": b.M{"$exists": false}},
			b.M{"$set": b.M{"state": t.StateOK}}); err != nil {
			return err
		}

		// 将 DeletedAt 重命名为 StateAt。仅更新已定义 DeletedAt 的行。
		if _, err := a.db.Collection("topics").UpdateMany(a.ctx,
			b.M{"deletedat": b.M{"$exists": true}},
			b.M{"$rename": b.M{"deletedat": "stateat"}}); err != nil {
			return err
		}

		// 在 State 上创建二级索引，用于查找已挂起和软删除的 Topic。
		if _, err = a.db.Collection("topics").Indexes().CreateOne(a.ctx, mdb.IndexModel{Keys: b.M{"state": 1}}); err != nil {
			return err
		}

		if err := bumpVersion(a, 111); err != nil {
			return err
		}
	}

	if a.version == 111 {
		// 仅升级版本以与 MySQL 保持一致。
		if err := bumpVersion(a, 112); err != nil {
			return err
		}
	}

	if a.version == 112 {
		// 在 用户(lastseen,updatedat) 上创建二级索引，用于删除过期用户账户。
		if _, err = a.db.Collection("users").Indexes().CreateOne(a.ctx,
			mdb.IndexModel{Keys: b.D{{Key: "lastseen", Value: 1}, {Key: "updatedat", Value: 1}}}); err != nil {
			return err
		}

		if err := bumpVersion(a, 113); err != nil {
			return err
		}
	}

	if a.version < 116 {
		// 版本 114：添加 Topic.aux，添加 fileuploads.etag。
		// 版本 115：添加 SQL 索引。
		// 版本 116：添加 Topic.subcnt。
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
func createSystemTopic(a *adapter) error {
	now := t.TimeNow()
	_, err := a.db.Collection("topics").InsertOne(a.ctx, &t.Topic{
		ObjHeader: t.ObjHeader{
			Id:        "sys",
			CreatedAt: now,
			UpdatedAt: now},
		TouchedAt: now,
		Access:    t.DefaultAccess{Auth: t.ModeNone, Anon: t.ModeNone},
		Public:    map[string]any{"fn": "System"},
	})
	return err
}

// 用户管理

// UserCreate 创建用户记录
func (a *adapter) UserCreate(usr *t.User) error {
	if _, err := a.db.Collection("users").InsertOne(a.ctx, &usr); err != nil {
		return err
	}

	return nil
}

// UserGet 根据用户 ID 获取单个用户。若未找到用户则返回 (nil, nil)
func (a *adapter) UserGet(id t.Uid) (*t.User, error) {
	var user t.User

	filter := b.M{"_id": id.String(), "state": b.M{"$ne": t.StateDeleted}}
	if err := a.db.Collection("users").FindOne(a.ctx, filter).Decode(&user); err != nil {
		if err == mdb.ErrNoDocuments { // 未找到用户
			return nil, nil
		} else {
			return nil, err
		}
	}
	user.Public = unmarshalBsonD(user.Public)
	user.Trusted = unmarshalBsonD(user.Trusted)
	return &user, nil
}

// UserGetAll 根据用户 ID 列表返回用户记录
func (a *adapter) UserGetAll(ids ...t.Uid) ([]t.User, error) {
	uids := make([]any, len(ids))
	for i, id := range ids {
		uids[i] = id.String()
	}

	var users []t.User
	filter := b.M{"_id": b.M{"$in": uids}, "state": b.M{"$ne": t.StateDeleted}}
	cur, err := a.db.Collection("users").Find(a.ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	for cur.Next(a.ctx) {
		var user t.User
		if err := cur.Decode(&user); err != nil {
			return nil, err
		}
		user.Public = unmarshalBsonD(user.Public)
		user.Trusted = unmarshalBsonD(user.Trusted)

		users = append(users, user)
	}

	return users, nil
}

// UserDelete 删除指定用户：完全擦除（硬删除）或标记为已删除。
func (a *adapter) UserDelete(uid t.Uid, hard bool) error {
	ownFilter := b.M{"owner": uid.String()}
	// 硬删除时，删除所有 Topic，包括那些
	// 之前已软删除。
	if !hard {
		ownFilter["state"] = b.M{"$ne": t.StateDeleted}
	}

	forUser := uid.String()
	// 选择用户作为所有者的 Topic。
	ownTopics, err := a.topicNamesForUser("topics", ownFilter, "_id", true)
	if err != nil {
		return err
	}
	ownTopicsFilter := b.M{"topic": b.M{"$in": ownTopics}}

	var sess mdb.Session
	if sess, err = a.conn.StartSession(); err != nil {
		return err
	}
	defer sess.EndSession(a.ctx)

	if err = a.maybeStartTransaction(sess); err != nil {
		return err
	}

	if err = mdb.WithSession(a.ctx, sess, func(sc mdb.SessionContext) error {

		if hard {
			// 无需删除用户的设备：设备存储在用户记录中，会随记录一起删除。

			// 删除用户在所有 Topic 中的订阅并递减 Topic 的 subcnt。
			if err = a.subsDelete(sc, b.M{"user": forUser}, true); err != nil {
				return err
			}

			// 删除用户在所有 Topic 中的 dellog 条目。
			err = a.clearUserDellog(sc, forUser)
			if err != nil {
				return err
			}

			// 无法删除用户在所有 Topic 中的消息，因为无法通知 Topic 此类删除。
			// 仅将消息保留，标记为由“未找到”用户发送。

			// 删除用户作为所有者的 Topic：
			if len(ownTopics) > 0 {

				// 1. 删除 dellog
				// 2. 递减 fileuploads。
				// 3. 删除所有消息。
				// 4. 删除订阅。

				// 删除用户拥有的 Topic 的 dellog。
				_, err = a.db.Collection("dellog").DeleteMany(sc, ownTopicsFilter)
				if err != nil {
					return err
				}

				// 递减 fileuploads 使用计数器
				// 首先获取在 topicIds 的 Topic 消息中使用的附件 ID 数组
				// 然后递减这些文件记录的 usecount 字段
				err = a.decFileUseCounter(sc, "messages", ownTopicsFilter)
				if err != nil {
					return err
				}

				// 递减 Topic 头像的使用计数器。
				err = a.decFileUseCounter(sc, "topics", b.M{"_id": b.M{"$in": ownTopics}})
				if err != nil {
					return err
				}

				// Delete 消息
				_, err = a.db.Collection("messages").DeleteMany(sc, ownTopicsFilter)
				if err != nil {
					return err
				}

				// 删除订阅（所有用户在该用户作为 Topic 所有者的地方）。
				_, err = a.db.Collection("subscriptions").DeleteMany(sc, ownTopicsFilter)
				if err != nil {
					return err
				}

				// 无需删除 Topic 标签：标签存储在 Topic 记录中，会随记录一起删除。

				// 最后删除 Topic。
				if _, err = a.db.Collection("topics").DeleteMany(sc, b.M{"owner": forUser}); err != nil {
					return err
				}
			}

			// 删除用户的认证记录。
			if _, err = a.authDelAllRecords(sc, uid); err != nil {
				return err
			}

			// 删除凭据。
			if err = a.credDel(sc, uid, "", ""); err != nil && err != t.ErrNotFound {
				return err
			}

			// 删除头像（递减使用计数器）。
			if err = a.decFileUseCounter(sc, "users", b.M{"_id": forUser}); err != nil {
				return err
			}

			// 无需删除用户的标签：标签存储在用户记录中，会随记录一起删除。

			// 最后删除用户。
			if _, err = a.db.Collection("users").DeleteOne(sc, b.M{"_id": forUser}); err != nil {
				return err
			}
		} else {
			// 禁用用户的订阅。
			if err = a.subsDelete(sc, b.M{"user": forUser}, false); err != nil {
				return err
			}

			now := t.TimeNow()
			disable := b.M{"$set": b.M{"updatedat": now, "state": t.StateDeleted, "stateat": now}}

			if len(ownTopics) > 0 {
				// 禁用用户作为所有者的 Topic 的订阅。
				if _, err = a.db.Collection("subscriptions").UpdateMany(sc, ownTopicsFilter, disable); err != nil {
					return err
				}

				// 禁用用户作为所有者的群组 Topic。
				if _, err = a.db.Collection("topics").UpdateMany(sc, b.M{"_id": b.M{"$in": ownTopics}},
					b.M{"$set": b.M{
						"updatedat": now, "touchedat": now, "state": t.StateDeleted, "stateat": now,
					}}); err != nil {
					return err
				}
			}

			// 禁用与该用户的 P2P Topic。
			p2pTopics, err := a.p2pTopicsForUser(uid)
			if err != nil {
				return err
			}
			if len(p2pTopics) > 0 {
				if _, err = a.db.Collection("topics").UpdateMany(sc, b.M{"_id": b.M{"$in": p2pTopics}},
					b.M{"$set": b.M{
						"updatedat": now, "touchedat": now, "state": t.StateDeleted, "stateat": now,
					}}); err != nil {
					return err
				}

				// 禁用用户已禁用的 P2P Topic 的订阅。
				if _, err = a.db.Collection("subscriptions").UpdateMany(sc,
					b.M{"topic": b.M{"$in": p2pTopics}}, disable); err != nil {
					return err
				}
			}

			// 最后禁用用户。
			if _, err = a.db.Collection("users").UpdateMany(sc, b.M{"_id": forUser}, disable); err != nil {
				return err
			}
		}

		// 最后提交所有更改
		return a.maybeCommitTransaction(sc, sess)
	}); err != nil {
		return err
	}

	return err
}

// topicStateForUser 由 UserUpdate 在更新包含状态变更时调用。
// 已软删除的 Topic 保持软删除状态。
func (a *adapter) topicStateForUser(uid t.Uid, now time.Time, update any) error {
	state, ok := update.(t.ObjState)
	if !ok {
		return t.ErrMalformed
	}

	if now.IsZero() {
		now = t.TimeNow()
	}

	// 变更用户作为所有者的所有 Topic 的状态。
	if _, err := a.db.Collection("topics").UpdateMany(a.ctx,
		b.M{"owner": uid.String(), "state": b.M{"$ne": t.StateDeleted}},
		b.M{"$set": b.M{"state": state, "stateat": now}}); err != nil {
		return err
	}

	// 变更与该用户的 P2P Topic 的状态（P2P Topic 的 owner 为空）
	// 获取与该用户的 P2P Topic 列表。
	p2pTopics, err := a.p2pTopicsForUser(uid)
	if err != nil {
		return err
	}
	if len(p2pTopics) > 0 {
		if _, err := a.db.Collection("topics").UpdateMany(a.ctx,
			b.M{"_id": b.M{"$in": p2pTopics}, "state": b.M{"$ne": t.StateDeleted}},
			b.M{"$set": b.M{"state": state, "stateat": now}}); err != nil {
			return err
		}
	}

	// 订阅无需更新：
	// 已禁用用户的订阅不会被禁用，仍可操作。
	return nil
}

// UserUpdate 更新用户记录
func (a *adapter) UserUpdate(uid t.Uid, update map[string]any) error {
	// 将字段名从 CamelCase 转换为小写。
	update = normalizeUpdateMap(update)

	_, err := a.db.Collection("users").UpdateOne(a.ctx, b.M{"_id": uid.String()}, b.M{"$set": update})
	if err != nil {
		return err
	}

	if state, ok := update["state"]; ok {
		now, _ := update["stateat"].(time.Time)
		err = a.topicStateForUser(uid, now, state)
	}

	// 标签存储在同一记录中，无需单独更新。

	return err
}

// UserUpdateTags 添加、删除或重置用户的标签。
func (a *adapter) UserUpdateTags(uid t.Uid, add, remove, reset []string) ([]string, error) {
	var newTags t.StringSlice
	// 与 nil 比较而非检查零长度：零长度重置是有效的。
	if reset != nil {
		// 用新值替换标签
		newTags = reset
	} else {
		var user t.User
		err := a.db.Collection("users").FindOne(a.ctx, b.M{"_id": uid.String()}).Decode(&user)
		if err != nil {
			return nil, err
		}

		// 变更标签列表。
		newTags = user.Tags
		if len(add) > 0 {
			newTags = union(newTags, add)
		}
		if len(remove) > 0 {
			newTags = diff(newTags, remove)
		}
	}

	return newTags, a.UserUpdate(uid, map[string]any{"tags": newTags})
}

// UserGetByCred returns 用户 ID for the given validated credential.
func (a *adapter) UserGetByCred(method, value string) (t.Uid, error) {
	var userId map[string]string
	err := a.db.Collection("credentials").FindOne(a.ctx,
		b.M{"_id": method + ":" + value},
		mdbopts.FindOne().SetProjection(b.M{"user": 1, "_id": 0}),
	).Decode(&userId)
	if err != nil {
		if err == mdb.ErrNoDocuments {
			return t.ZeroUid, nil
		}
		return t.ZeroUid, err
	}

	return t.ParseUid(userId["user"]), nil
}

// UserUnreadCount returns the total number of unread 消息 in all Topic with
// the R 权限. If read fails, the counts are still returned with the original
// 用户 IDs but with the unread count undefined and non-nil 错误.
// Does not count unread 消息 in Channel although it probably should.
func (a *adapter) UserUnreadCount(ids ...t.Uid) (map[t.Uid]int, error) {
	uids := make([]string, len(ids))
	counts := make(map[t.Uid]int, len(ids))
	for i, id := range ids {
		uids[i] = id.String()
		// 确保所有原始 uid 始终存在。
		counts[id] = 0
	}
	/*
		Query:
			db.subscriptions.aggregate([
				{ $match: { user: { $in: ["KnElfSSA21U", "0ZcCQmwI2RI"] } } },
				{ $lookup: { from: "topics", localField: "topic", foreignField: "_id", as: "fromTopics"} },
				{ $match: { fromTopics: { $not: {$size: 0}  }}},
				{ $replaceRoot: { newRoot: { $mergeObjects: [ {$arrayElemAt: [ "$fromTopics", 0 ]} , "$$ROOT" ] } } },
				{ $match: {
						deletedat: { $exists: false },
						state:     { $ne: t.StateDeleted },
						modewant:  { $bitsAllSet: [ t.ModeRead ] },
						modegiven: { $bitsAllSet: [ t.ModeRead ] }
					}
				},
				{ $project: { _id: 0, user: 1, readseqid: 1, seqid: 1} },
				{ $group: { _id: "$user", unreadCount: { $sum: { $subtract: [ "$seqid", "$readseqid" ] } } } }
			])

		Result:
			{ "_id" : "KnElfSSA21U", "unreadCount" : 0 }
			{ "_id" : "0ZcCQmwI2RI", "unreadCount" : 7 }
	*/

	pipeline := b.A{
		b.M{"$match": b.M{"user": b.M{"$in": uids}}},
		// 将 Topic 名称映射为真实的 Topic ID (如将 chn... 映射为对应的 grp... 主键) 从而支持 Channel
		b.M{"$addFields": b.M{
			"realTopicId": b.M{
				"$cond": b.A{
					b.M{"$eq": b.A{b.M{"$substrCP": b.A{"$topic", 0, 3}}, "chn"}},
					b.M{"$concat": b.A{"grp", b.M{"$substrCP": b.A{"$topic", 3, b.M{"$strLenCP": "$topic"}}}}},
					"$topic",
				},
			},
		}},
		// 从两个集合中连接文档
		b.M{"$lookup": b.M{
			"from":         "topics",
			"localField":   "realTopicId",
			"foreignField": "_id",
			"as":           "fromTopics"},
		},
		// 移除没有订阅的用户。
		b.M{"$match": b.M{"fromTopics": b.M{"$not": b.M{"$size": 0}}}},
		// 合并两个文档为一个
		b.M{"$replaceRoot": b.M{"newRoot": b.M{"$mergeObjects": b.A{b.M{"$arrayElemAt": b.A{"$fromTopics", 0}}, "$$ROOT"}}}},

		// 只保留影响结果的记录。
		b.M{"$match": b.M{
			"deletedat": b.M{"$exists": false},
			"state":     b.M{"$ne": t.StateDeleted},
			// 按访问权限过滤
			"modewant":  b.M{"$bitsAllSet": b.A{t.ModeRead}},
			"modegiven": b.M{"$bitsAllSet": b.A{t.ModeRead}}}},

		// 移除未使用的字段。
		b.M{"$project": b.M{"_id": 0, "user": 1, "readseqid": 1, "seqid": 1}},
		// 按用户分组。
		b.M{"$group": b.M{"_id": "$user", "unreadCount": b.M{"$sum": b.M{"$subtract": b.A{"$seqid", "$readseqid"}}}}},
	}
	cur, err := a.db.Collection("subscriptions").Aggregate(a.ctx, pipeline)
	if err != nil {
		return counts, err
	}
	defer cur.Close(a.ctx)

	for cur.Next(a.ctx) {
		var oneCount struct {
			Id          string `bson:"_id"`
			UnreadCount int    `bson:"unreadCount"`
		}
		cur.Decode(&oneCount)
		counts[t.ParseUid(oneCount.Id)] = oneCount.UnreadCount
	}

	return counts, nil
}

// UserGetUnvalidated 返回从未登录、没有已验证凭据且自 lastUpdatedBefore 以来未更新过的用户 ID 列表。
func (a *adapter) UserGetUnvalidated(lastUpdatedBefore time.Time, limit int) ([]t.Uid, error) {
	/*
		Query:
		[
			// .. WHERE lastseen 为空 AND updatedat<?
			{$match: {
				$and: [
					{ lastseen: null },
					{ updatedat: {$lt: new ISODate("2022-12-09T01:26:15.819Z")} },
				],
			}},
			// JOIN credentials ON id=用户
			{$lookup: {
				from: "credentials",
				localField: "_id",
				foreignField: "user",
				as: "fcred",
			}},
			// {x: 1, y: [{a: 1}, {a: 2}]} -> [{x: 1, a: 1}, {x: 1, a: 2}]（展开数组）
		  {$unwind: {path: "$fcred"}},
			// SELECT _id, 当 done 时为 1 否则为 0
		  {$project: {
				_id: 1,
		    completed: { $cond: { if: "$fcred.done", then: 1, else: 0 } },
		  }},
			// 按 _id 分组
		  {$group: { _id: "$_id", completed: { $sum: "$completed" } } },
			// 筛选 completed=0
		  {$match: { completed: 0 }},
			// 投影 _id
		  {$project: { _id: "$_id" }},
			{$limit: 10}
		]
	*/
	pipeline := b.A{
		b.M{"$match": b.M{
			"$and": b.A{
				b.M{"lastseen": primitive.Null{}},
				b.M{"updatedat": b.M{"$lt": lastUpdatedBefore}},
			},
		}},
		b.M{"$lookup": b.D{
			{Key: "from", Value: "credentials"},
			{Key: "localField", Value: "_id"},
			{Key: "foreignField", Value: "user"},
			{Key: "as", Value: "fcred"}},
		},
		b.M{"$unwind": b.M{"path": "$fcred"}},
		b.M{"$project": b.D{
			{Key: "_id", Value: 1},
			{Key: "completed", Value: b.M{
				"$cond": b.D{{Key: "if", Value: "$fcred.done"}, {Key: "then", Value: 1}, {Key: "else", Value: 0}}},
			}}},
		b.M{"$group": b.D{{Key: "_id", Value: "$_id"}, {Key: "completed", Value: b.M{"$sum": "$completed"}}}},
		b.M{"$match": b.M{"completed": 0}},
		b.M{"$project": b.M{"_id": "$_id"}},
		b.M{"$limit": limit},
	}

	cur, err := a.db.Collection("users").Aggregate(a.ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var uids []t.Uid
	for cur.Next(a.ctx) {
		var oneUser struct {
			Id string `bson:"_id"`
		}
		if err := cur.Decode(&oneUser); err != nil {
			return nil, err
		}
		uid := t.ParseUid(oneUser.Id)
		if uid.IsZero() {
			return nil, errors.New("failed to decode user id")
		}
		uids = append(uids, uid)
	}

	return uids, err
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
	credCollection := a.db.Collection("credentials")

	cred.Id = cred.Method + ":" + cred.Value

	if !cred.Done {
		// 检查是否 the same credential is already validated.
		var result1 t.Credential
		err := credCollection.FindOne(a.ctx, b.M{"_id": cred.Id}).Decode(&result1)
		if result1 != (t.Credential{}) {
			// 已有人验证了此凭据。
			return false, t.ErrDuplicate
		}
		if err != nil && err != mdb.ErrNoDocuments { // if no result -> continue
			return false, err
		}

		// 软删除此用户和方法的所有未验证记录。
		_, err = credCollection.UpdateMany(a.ctx,
			b.M{"user": cred.User, "method": cred.Method, "done": false},
			b.M{"$set": b.M{"deletedat": t.TimeNow()}})
		if err != nil {
			return false, err
		}

		// 如果凭据未确认，不应阻止其他人尝试验证：
		// 使索引为用户唯一而非全局唯一。
		cred.Id = cred.User + ":" + cred.Id

		// 检查此凭据是否已被用户添加。
		var result2 t.Credential
		err = credCollection.FindOne(a.ctx, b.M{"_id": cred.Id}).Decode(&result2)
		if result2 != (t.Credential{}) {
			_, err = credCollection.UpdateOne(a.ctx,
				b.M{"_id": cred.Id},
				b.M{
					"$unset": b.M{"deletedat": ""},
					"$set":   b.M{"updatedat": cred.UpdatedAt, "resp": cred.Resp}})
			if err != nil {
				return false, err
			}

			// 记录已更新，一切正常。
			return false, nil
		}
		if err != nil && err != mdb.ErrNoDocuments {
			return false, err
		}
	} else {
		// 硬删除可能存在的未验证凭据。
		_, err := credCollection.DeleteOne(a.ctx, b.M{"_id": cred.User + ":" + cred.Id})
		if err != nil {
			return false, err
		}
	}

	// 插入新记录。
	_, err := credCollection.InsertOne(a.ctx, cred)
	if isDuplicateErr(err) {
		return true, t.ErrDuplicate
	}

	return true, err
}

// CredGetActive 返回指定方法当前活跃的凭据记录。
func (a *adapter) CredGetActive(uid t.Uid, method string) (*t.Credential, error) {
	var cred t.Credential

	filter := b.M{
		"user":      uid.String(),
		"deletedat": b.M{"$exists": false},
		"method":    method,
		"done":      false}

	if err := a.db.Collection("credentials").FindOne(a.ctx, filter).Decode(&cred); err != nil {
		if err == mdb.ErrNoDocuments { // 未找到凭据
			err = nil
		}
		return nil, err
	}

	return &cred, nil
}

// CredGetAll 返回指定用户和方法的凭据记录，可选仅已验证或全部。
func (a *adapter) CredGetAll(uid t.Uid, method string, validatedOnly bool) ([]t.Credential, error) {
	filter := b.M{"user": uid.String()}
	if method != "" {
		filter["method"] = method
	}
	if validatedOnly {
		filter["done"] = true
	} else {
		filter["deletedat"] = b.M{"$exists": false}
	}

	cur, err := a.db.Collection("credentials").Find(a.ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var credentials []t.Credential
	if err := cur.All(a.ctx, &credentials); err != nil {
		return nil, err
	}
	return credentials, nil
}

// CredDel 删除指定方法/值的凭据。若方法为空，则删除用户的所有凭据。
func (a *adapter) credDel(ctx context.Context, uid t.Uid, method, value string) error {
	credCollection := a.db.Collection("credentials")
	filter := b.M{"user": uid.String()}
	if method != "" {
		filter["method"] = method
		if value != "" {
			filter["value"] = value
		}
	} else {
		res, err := credCollection.DeleteMany(ctx, filter)
		if err == nil {
			if res.DeletedCount == 0 {
				err = t.ErrNotFound
			}
		}
		return err
	}

	// 硬删除所有已确认的值或未尝试确认的值。
	hardDeleteFilter := copyBsonMap(filter)
	hardDeleteFilter["$or"] = b.A{
		b.M{"done": true},
		b.M{"retries": 0}}
	if res, err := credCollection.DeleteMany(ctx, hardDeleteFilter); err != nil {
		return err
	} else if res.DeletedCount > 0 {
		return nil
	}

	// 软删除所有其他值。
	res, err := credCollection.UpdateMany(ctx, filter, b.M{"$set": b.M{"deletedat": t.TimeNow()}})
	if err == nil {
		if res.ModifiedCount == 0 {
			err = t.ErrNotFound
		}
	}
	return err
}

func (a *adapter) CredDel(uid t.Uid, method, value string) error {
	return a.credDel(a.ctx, uid, method, value)
}

// CredConfirm 将指定凭据标记为已验证。
func (a *adapter) CredConfirm(uid t.Uid, method string) error {
	cred, err := a.CredGetActive(uid, method)
	if err != nil {
		return err
	}

	cred.Done = true
	cred.UpdatedAt = t.TimeNow()
	if _, err = a.CredUpsert(cred); err != nil {
		return err
	}

	_, _ = a.db.Collection("credentials").DeleteOne(a.ctx, b.M{"_id": uid.String() + ":" + cred.Method + ":" + cred.Value})
	return nil
}

// CredFail 增加指定凭据验证失败次数。
func (a *adapter) CredFail(uid t.Uid, method string) error {
	filter := b.M{
		"user":      uid.String(),
		"deletedat": b.M{"$exists": false},
		"method":    method,
		"done":      false}

	update := b.M{
		"$inc": b.M{"retries": 1},
		"$set": b.M{"updatedat": t.TimeNow()}}
	_, err := a.db.Collection("credentials").UpdateOne(a.ctx, filter, update)
	return err
}

// 基础认证方案的认证管理

// AuthGetUniqueRecord 根据唯一值（如登录名）返回认证记录。
func (a *adapter) AuthGetUniqueRecord(unique string) (t.Uid, auth.Level, []byte, time.Time, error) {
	var record struct {
		UserId  string
		AuthLvl auth.Level
		Secret  []byte
		Expires time.Time
	}

	filter := b.M{"_id": unique}
	findOpts := mdbopts.FindOne().SetProjection(b.M{
		"userid":  1,
		"authlvl": 1,
		"secret":  1,
		"expires": 1,
	})
	if err := a.db.Collection("auth").FindOne(a.ctx, filter, findOpts).Decode(&record); err != nil {
		if err == mdb.ErrNoDocuments {
			return t.ZeroUid, 0, nil, time.Time{}, nil
		}
		return t.ZeroUid, 0, nil, time.Time{}, err
	}

	return t.ParseUid(record.UserId), record.AuthLvl, record.Secret, record.Expires, nil
}

// AuthGetRecord returns authentication record given 用户 ID and method.
func (a *adapter) AuthGetRecord(uid t.Uid, scheme string) (string, auth.Level, []byte, time.Time, error) {
	var record struct {
		Id      string `bson:"_id"`
		AuthLvl auth.Level
		Secret  []byte
		Expires time.Time
	}

	filter := b.M{"userid": uid.String(), "scheme": scheme}
	findOpts := mdbopts.FindOne().SetProjection(b.M{
		"authlvl": 1,
		"secret":  1,
		"expires": 1,
	})
	err := a.db.Collection("auth").FindOne(a.ctx, filter, findOpts).Decode(&record)
	if err != nil {
		if err == mdb.ErrNoDocuments {
			err = t.ErrNotFound
		}
		return "", 0, nil, time.Time{}, err
	}

	return record.Id, record.AuthLvl, record.Secret, record.Expires, nil
}

// AuthAddRecord 创建新的认证记录
func (a *adapter) AuthAddRecord(uid t.Uid, scheme, unique string, authLvl auth.Level, secret []byte, expires time.Time) error {
	authRecord := b.M{
		"_id":     unique,
		"userid":  uid.String(),
		"scheme":  scheme,
		"authlvl": authLvl,
		"secret":  secret,
		"expires": expires}
	if _, err := a.db.Collection("auth").InsertOne(a.ctx, authRecord); err != nil {
		if isDuplicateErr(err) {
			return t.ErrDuplicate
		}
		return err
	}
	return nil
}

// AuthDelScheme 删除用户的指定认证方案。
func (a *adapter) AuthDelScheme(uid t.Uid, scheme string) error {
	_, err := a.db.Collection("auth").DeleteOne(a.ctx,
		b.M{
			"userid": uid.String(),
			"scheme": scheme})
	return err
}

func (a *adapter) authDelAllRecords(ctx context.Context, uid t.Uid) (int, error) {
	res, err := a.db.Collection("auth").DeleteMany(ctx, b.M{"userid": uid.String()})
	return int(res.DeletedCount), err
}

// AuthDelAllRecords 删除指定用户的所有认证记录。
func (a *adapter) AuthDelAllRecords(uid t.Uid) (int, error) {
	return a.authDelAllRecords(a.ctx, uid)
}

// AuthUpdRecord 修改认证记录。
func (a *adapter) AuthUpdRecord(uid t.Uid, scheme, unique string,
	authLvl auth.Level, secret []byte, expires time.Time) error {
	// 主键不可变。如果 '_id' 已变更，需要用新记录替换旧记录：
	// 1. 检查 '_id' 是否已变更。
	// 2. 若未变更，按 '_id' 执行更新。
	// 3. 若已变更，先插入新记录（可能因 '_id' 重复而失败），然后删除旧记录。

	var err error
	var record common.AuthRecord
	findOpts := mdbopts.FindOne().SetProjection(b.M{"_id": 1})
	filter := b.M{"userid": uid.String(), "scheme": scheme}
	if err = a.db.Collection("auth").FindOne(a.ctx, filter, findOpts).Decode(&record); err != nil {
		if err == mdb.ErrNoDocuments {
			err = t.ErrNotFound
		}
		return err
	}

	if record.Unique == unique {
		upd := b.M{
			"authlvl": authLvl,
		}
		if len(secret) > 0 {
			upd["secret"] = secret
		}
		if !expires.IsZero() {
			upd["expires"] = expires
		}
		_, err = a.db.Collection("auth").UpdateOne(a.ctx,
			b.M{"_id": unique},
			b.M{"$set": upd})
	} else {
		// 唯一值已变更。采用原子增加+尝试删除模式（若删除失败则原子撤销补偿，兼顾单机与副本集集群）
		if len(secret) == 0 {
			secret = record.Secret
		}
		if expires.IsZero() {
			expires = record.Expires
		}
		err = a.AuthAddRecord(uid, scheme, unique, authLvl, secret, expires)
		if err == nil {
			if _, delErr := a.db.Collection("auth").DeleteOne(a.ctx, b.M{"_id": record.Unique}); delErr != nil {
				// 删除旧记录失败：发起补偿性回滚，删除刚创建的新记录
				a.db.Collection("auth").DeleteOne(a.ctx, b.M{"_id": unique})
				err = delErr
			}
		}
	}

	return err
}

// Topic 管理

func (a *adapter) undeleteSubscription(sub *t.Subscription) error {
	_, err := a.db.Collection("subscriptions").UpdateOne(a.ctx,
		b.M{"_id": sub.Id},
		b.M{
			"$unset": b.M{"deletedat": ""},
			"$set": b.M{
				"updatedat": sub.UpdatedAt,
				"createdat": sub.CreatedAt,
				"modegiven": sub.ModeGiven,
				"modewant":  sub.ModeWant,
				"delid":     0,
				"readseqid": 0,
				"recvseqid": 0}})
	return err
}

// TopicCreate 创建 Topic
func (a *adapter) TopicCreate(topic *t.Topic) error {
	_, err := a.db.Collection("topics").InsertOne(a.ctx, &topic)
	return err
}

// TopicCreateP2P 创建 P2P Topic。
func (a *adapter) TopicCreateP2P(initiator, invited *t.Subscription) error {
	initiator.Id = initiator.Topic + ":" + initiator.User
	// Don't care if the initiator changes own 订阅
	replOpts := mdbopts.Replace().SetUpsert(true)
	_, err := a.db.Collection("subscriptions").ReplaceOne(a.ctx, b.M{"_id": initiator.Id}, initiator, replOpts)
	if err != nil {
		return err
	}

	// If the second 订阅 exists, don't overwrite it. Just make sure it's not deleted.
	invited.Id = invited.Topic + ":" + invited.User
	_, err = a.db.Collection("subscriptions").InsertOne(a.ctx, invited)
	if err != nil {
		// Is this a duplicate 订阅?
		if !isDuplicateErr(err) {
			// It's a genuine DB 错误
			return err
		}
		// 恢复第二个订阅（如果存在）：移除 DeletedAt，更新 CreatedAt 和 UpdatedAt，
		// 更新 ModeGiven。
		err = a.undeleteSubscription(invited)
		if err != nil {
			return err
		}
	}

	topic := &t.Topic{
		ObjHeader: t.ObjHeader{Id: initiator.Topic},
		TouchedAt: initiator.GetTouchedAt(),
	}
	topic.ObjHeader.MergeTimes(&initiator.ObjHeader)
	return a.TopicCreate(topic)
}

// TopicGet 按名称加载单个 Topic（如果存在）。若 Topic 不存在则返回 (nil, nil)
func (a *adapter) TopicGet(topic string) (*t.Topic, error) {
	var tt = new(t.Topic)
	if err := a.db.Collection("topics").FindOne(a.ctx, b.M{"_id": topic}).Decode(tt); err != nil {
		if err == mdb.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}

	if t.GetTopicCat(topic) == t.TopicCatGrp {
		// 已找到 Topic，获取订阅计数。
		subCnt, err := a.subscriptionCount(topic)
		if err != nil {
			return nil, err
		}

		if int(subCnt) != tt.SubCnt {
			// Update the Topic with the correct 订阅 count.
			tt.SubCnt = int(subCnt)
			err = a.topicUpdate(topic, b.M{"subcnt": tt.SubCnt})
			if err != nil {
				return nil, err
			}
		}
	}

	tt.Public = unmarshalBsonD(tt.Public)
	tt.Trusted = unmarshalBsonD(tt.Trusted)

	return tt, nil
}

// TopicsForUser 加载用户的联系人列表：P2P 和群组 Topic，不包括 'me' 和 'fnd' 订阅。
// 读取并反规范化 Public 和 Trusted 值。
func (a *adapter) TopicsForUser(uid t.Uid, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	// 获取用户的所有订阅。
	filter := b.M{"user": uid.String()}
	if !keepDeleted {
		// 过滤掉已定义 deletedat 的行
		filter["deletedat"] = b.M{"$exists": false}
	}

	limit := 0
	ims := time.Time{}
	if opts != nil {
		if opts.Topic != "" {
			filter["topic"] = opts.Topic
		}

		// 仅在客户端不管理缓存（或冷启动）时应用限制。
		// 否则需要获取所有订阅并与用户/Topic 手动连接。
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

	var findOpts *mdbopts.FindOptions
	if limit > 0 {
		findOpts = mdbopts.Find().SetLimit(int64(limit))
	}

	cur, err := a.db.Collection("subscriptions").Find(a.ctx, filter, findOpts)
	if err != nil {
		return nil, err
	}
	// 必须手动关闭游标，因为我们将重用它们。

	// Fetch 订阅. Two queries are needed: 用户 table (me & p2p) and Topic table (p2p and grp).
	// Prepare a list of Separate 订阅 to 用户 vs Topic
	join := make(map[string]t.Subscription) // Keeping these to make a join with table for .private and .access
	topq := make([]string, 0, 16)
	usrq := make([]string, 0, 16)
	for cur.Next(a.ctx) {
		var sub t.Subscription
		if err = cur.Decode(&sub); err != nil {
			break
		}
		tname := sub.Topic
		sub.User = uid.String()
		tcat := t.GetTopicCat(tname)

		if tcat == t.TopicCatMe || tcat == t.TopicCatFnd {
			// Skip 'me' or 'fnd' 订阅. Don't skip 'sys'.
			continue
		} else if tcat == t.TopicCatP2P {
			// P2P 订阅, find the other 用户 to get 用户.Public
			uid1, uid2, _ := t.ParseP2P(sub.Topic)
			if uid1 == uid {
				usrq = append(usrq, uid2.String())
				sub.SetWith(uid2.UserId())
			} else {
				usrq = append(usrq, uid1.String())
				sub.SetWith(uid1.UserId())
			}
			topq = append(topq, tname)
		} else if tcat == t.TopicCatGrp {
			// 可能将 Channel 名称转换为 Topic 名称。
			tname = t.ChnToGrp(tname)
		}
		// 'slf'、'sys' 订阅无需特殊处理。

		topq = append(topq, tname)
		sub.Private = unmarshalBsonD(sub.Private)
		join[tname] = sub
	}
	cur.Close(a.ctx)
	if err != nil {
		return nil, err
	}

	var subs []t.Subscription
	if len(join) == 0 {
		return subs, nil
	}

	if len(topq) > 0 {
		// 获取群组和 P2P Topic
		filter = b.M{"_id": b.M{"$in": topq}}

		if !keepDeleted {
			filter["state"] = b.M{"$ne": t.StateDeleted}
		}

		if !ims.IsZero() {
			// 如果提供了缓存时间戳：仅获取较新的条目。
			filter["touchedat"] = b.M{"$gt": ims}

			findOpts = nil
			if limit > 0 && limit < len(topq) {
				// 没有意义获取超过请求限制的数量。
				findOpts = mdbopts.Find().SetSort(b.D{{Key: "touchedat", Value: 1}}).SetLimit(int64(limit))
			}
		}

		cur, err = a.db.Collection("topics").Find(a.ctx, filter, findOpts)
		if err != nil {
			return nil, err
		}

		for cur.Next(a.ctx) {
			var top t.Topic
			if err = cur.Decode(&top); err != nil {
				break
			}
			sub := join[top.Id]
			// 检查 sub.UpdatedAt 是否需要调整为更早或更晚的时间。
			sub.UpdatedAt = common.SelectLatestTime(sub.UpdatedAt, top.UpdatedAt)
			sub.SetState(top.State)
			sub.SetTouchedAt(top.TouchedAt)
			sub.SetSeqId(top.SeqId)
			if t.GetTopicCat(sub.Topic) == t.TopicCatGrp {
				sub.SetSubCnt(top.SubCnt)
				sub.SetPublic(unmarshalBsonD(top.Public))
				sub.SetTrusted(unmarshalBsonD(top.Trusted))
			}
			// 放回 P2P 订阅的更新值，将在下面进一步处理
			join[top.Id] = sub
		}
		cur.Close(a.ctx)

		if err != nil {
			return nil, err
		}
	}

	// 获取 P2P 用户并连接到 P2P 表
	if len(usrq) > 0 {
		filter = b.M{"_id": b.M{"$in": usrq}}
		if !keepDeleted {
			filter["state"] = b.M{"$ne": t.StateDeleted}
		}

		// 忽略 ims：我们需要所有用户来获取 LastSeen 和 UserAgent。

		cur, err = a.db.Collection("users").Find(a.ctx, filter, findOpts)
		if err != nil {
			return nil, err
		}

		for cur.Next(a.ctx) {
			var usr2 t.User
			if err = cur.Decode(&usr2); err != nil {
				break
			}

			joinOn := uid.P2PName(t.ParseUid(usr2.Id))
			if sub, ok := join[joinOn]; ok {
				sub.UpdatedAt = common.SelectLatestTime(sub.UpdatedAt, usr2.UpdatedAt)
				sub.SetState(usr2.State)
				sub.SetPublic(unmarshalBsonD(usr2.Public))
				sub.SetTrusted(unmarshalBsonD(usr2.Trusted))
				sub.SetDefaultAccess(usr2.Access.Auth, usr2.Access.Anon)
				sub.SetLastSeenAndUA(usr2.LastSeen, usr2.UserAgent)
				join[joinOn] = sub
			}
		}
		cur.Close(a.ctx)

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

// UsersForTopic 加载指定 Topic 的用户订阅（不包括 Channel 读者）。
// Public 和 Trusted 已加载。
func (a *adapter) UsersForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	tcat := t.GetTopicCat(topic)

	// 获取所有已订阅用户。用户数量不大。
	filter := b.M{"topic": topic}
	if !keepDeleted && tcat != t.TopicCatP2P {
		// 过滤掉 DeletedAt 非空的行。
		// P2P Topic 必须加载所有订阅，否则无法交换 Public 值。
		filter["deletedat"] = b.M{"$exists": false}
	}

	limit := a.maxResults
	var oneUser t.Uid
	if opts != nil {
		// 忽略 IfModifiedSince - 我们必须返回所有条目
		// 未修改的条目将被去除 Public、Trusted 和 Private。

		if !opts.User.IsZero() {
			if tcat != t.TopicCatP2P {
				filter["user"] = opts.User.String()
			}
			oneUser = opts.User
		}
		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}

	cur, err := a.db.Collection("subscriptions").Find(a.ctx, filter, mdbopts.Find().SetLimit(int64(limit)))
	if err != nil {
		return nil, err
	}

	// Fetch 订阅.
	var subs []t.Subscription
	join := make(map[string]t.Subscription)
	usrq := make([]any, 0, 16)
	for cur.Next(a.ctx) {
		var sub t.Subscription
		if err = cur.Decode(&sub); err != nil {
			break
		}
		join[sub.User] = sub
		usrq = append(usrq, sub.User)
	}
	cur.Close(a.ctx)
	if err != nil {
		return nil, err
	}

	// Fetch 用户 by a list of 订阅.
	if len(usrq) > 0 {
		subs = make([]t.Subscription, 0, len(usrq))
		cur, err = a.db.Collection("users").Find(a.ctx, b.M{
			"_id":   b.M{"$in": usrq},
			"state": b.M{"$ne": t.StateDeleted}})
		if err != nil {
			return nil, err
		}

		for cur.Next(a.ctx) {
			var usr2 t.User
			if err = cur.Decode(&usr2); err != nil {
				break
			}
			if sub, ok := join[usr2.Id]; ok {
				sub.ObjHeader.MergeTimes(&usr2.ObjHeader)
				sub.Private = unmarshalBsonD(sub.Private)
				sub.SetPublic(unmarshalBsonD(usr2.Public))
				sub.SetTrusted(unmarshalBsonD(usr2.Trusted))
				sub.SetLastSeenAndUA(usr2.LastSeen, usr2.UserAgent)
				subs = append(subs, sub)
			}
		}
		cur.Close(a.ctx)
		if err != nil {
			return nil, err
		}
	}

	if t.GetTopicCat(topic) == t.TopicCatP2P && len(subs) > 0 {
		// 按预期交换 P2P Topic 的 public 值和 lastSeen。
		if len(subs) == 1 {
			// 用户已删除。无法处理。
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
			userAgent := subs[0].GetUserAgent()
			subs[0].SetLastSeenAndUA(subs[1].GetLastSeen(), subs[1].GetUserAgent())
			subs[1].SetLastSeenAndUA(lastSeen, userAgent)
		}

		// 移除已删除和不需要的订阅
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

	return subs, nil
}

// topicNamesForUser 从 'collection' 的 'field' 中使用 'filter' 读取 Topic 名称。
// 如果 includeChan 为 true，对于群组 Topic 还会添加相应的 Channel 名称。
func (a *adapter) topicNamesForUser(collection string, filter b.M, field string, includeChan bool) ([]string, error) {
	cur, err := a.db.Collection(collection).Find(a.ctx, filter,
		mdbopts.Find().SetProjection(b.M{field: 1}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var names []string
	for cur.Next(a.ctx) {
		var res map[string]string
		if err = cur.Decode(&res); err != nil {
			break
		}
		names = append(names, res[field])
		// 如果名称是群组 Topic，且请求时还添加 Channel 名称。
		if includeChan {
			if channel := t.GrpToChn(res[field]); channel != "" {
				names = append(names, channel)
			}
		}
	}

	return names, err
}

func (a *adapter) p2pTopicsForUser(uid t.Uid) ([]string, error) {
	return a.topicNamesForUser("subscriptions",
		b.M{
			"user":      uid.String(),
			"deletedat": b.M{"$exists": false},
			"topic":     b.M{"$regex": primitive.Regex{Pattern: "^p2p"}}},
		"topic", false)
}

// OwnTopics loads a slice of Topic names where the 用户 is the owner.
func (a *adapter) OwnTopics(uid t.Uid) ([]string, error) {
	return a.topicNamesForUser("topics",
		b.M{"owner": uid.String(), "state": b.M{"$ne": t.StateDeleted}},
		"_id", false)
}

// ChannelsForUser loads a slice of Topic names where the 用户 is a Channel reader and notifications (P) are enabled.
func (a *adapter) ChannelsForUser(uid t.Uid) ([]string, error) {
	return a.topicNamesForUser("subscriptions",
		b.M{
			"user":      uid.String(),
			"deletedat": b.M{"$exists": false},
			"topic":     b.M{"$regex": primitive.Regex{Pattern: "^chn"}},
			"modewant":  b.M{"$bitsAllSet": b.A{t.ModePres}},
			"modegiven": b.M{"$bitsAllSet": b.A{t.ModePres}}},
		"topic", false)
}

// TopicShare creates Topic 订阅.
func (a *adapter) TopicShare(topic string, shares []*t.Subscription) error {
	// 分配 Id。
	for _, sub := range shares {
		sub.Id = sub.Topic + ":" + sub.User
	}

	// 订阅 could have been marked as deleted (DeletedAt != nil). If it's marked
	// as deleted, unmark by clearing the DeletedAt field of the old 订阅 and
	// 更新时间和 ModeGiven。
	for _, sub := range shares {
		_, err := a.db.Collection("subscriptions").InsertOne(a.ctx, sub)
		if err != nil {
			if isDuplicateErr(err) {
				if err = a.undeleteSubscription(sub); err != nil {
					return err
				}
			} else {
				return err
			}
		}
	}

	if topic != "" {
		// Update Topic's 订阅 count.
		// The 错误 is ignored because the 订阅 have been created already.
		a.db.Collection("topics").UpdateOne(a.ctx,
			b.M{"_id": topic},
			b.M{"$inc": b.M{"subcnt": len(shares)}})
	}

	return nil
}

// TopicDelete deletes Topic, 订阅, 消息.
func (a *adapter) TopicDelete(topic string, isChan, hard bool) error {
	filter := b.M{}
	if isChan {
		// If the Topic is a Channel, must try to delete 订阅 under both grpXXX and chnXXX names.
		filter["$or"] = b.A{
			b.M{"topic": topic},
			b.M{"topic": t.GrpToChn(topic)},
		}
	} else {
		filter["topic"] = topic
	}
	err := a.subsDelete(a.ctx, filter, hard)
	if err != nil {
		return err
	}

	filter = b.M{"_id": topic}
	if hard {
		if err = a.decFileUseCounter(a.ctx, "topics", filter); err != nil {
			return err
		}
		if err = a.MessageDeleteList(topic, nil); err != nil {
			return err
		}
		_, err = a.db.Collection("topics").DeleteOne(a.ctx, filter)
	} else {
		_, err = a.db.Collection("topics").UpdateOne(a.ctx, filter, b.M{"$set": b.M{
			"state":   t.StateDeleted,
			"stateat": t.TimeNow(),
		}})
	}

	return err
}

// TopicUpdateOnMessage increments Topic's or 用户's SeqId value and updates TouchedAt timestamp.
func (a *adapter) TopicUpdateOnMessage(topic string, msg *t.Message) error {
	return a.topicUpdate(topic, b.M{"seqid": msg.SeqId, "touchedat": msg.CreatedAt})
}

func (a *adapter) subscriptionCount(topic string) (int64, error) {
	// 获取 Topic 的非已删除订阅数。
	return a.db.Collection("subscriptions").CountDocuments(a.ctx, b.M{
		"topic":     b.M{"$in": b.A{topic, t.GrpToChn(topic)}},
		"deletedat": b.M{"$exists": false},
	})
}

// TopicUpdateSubCnt 更新 Topic 中反规范化的订阅者计数。
func (a *adapter) TopicUpdateSubCnt(topic string) error {
	// 获取 Topic 的非已删除订阅数。
	// UPDATE ... SET=(SELECT ...) 在 MongoDB 中不支持，所以必须分两个查询完成。
	count, err := a.subscriptionCount(topic)
	if err != nil {
		return err
	}
	return a.topicUpdate(topic, b.M{"subcnt": count})
}

// TopicUpdate 更新 Topic 记录。
func (a *adapter) TopicUpdate(topic string, update map[string]any) error {
	if t, u := update["TouchedAt"], update["UpdatedAt"]; t == nil && u != nil {
		update["TouchedAt"] = u
	}
	return a.topicUpdate(topic, normalizeUpdateMap(update))
}

// TopicOwnerChange 更新 Topic 的所有者
func (a *adapter) TopicOwnerChange(topic string, newOwner t.Uid) error {
	return a.topicUpdate(topic, map[string]any{"owner": newOwner.String()})
}

func (a *adapter) topicUpdate(topic string, update map[string]any) error {
	_, err := a.db.Collection("topics").UpdateOne(a.ctx,
		b.M{"_id": topic},
		b.M{"$set": update})

	return err
}

// Topic 订阅

// SubscriptionGet 读取用户对 Topic 的订阅。
func (a *adapter) SubscriptionGet(topic string, user t.Uid, keepDeleted bool) (*t.Subscription, error) {
	sub := new(t.Subscription)
	filter := b.M{"_id": topic + ":" + user.String()}
	if !keepDeleted {
		filter["deletedat"] = b.M{"$exists": false}
	}
	err := a.db.Collection("subscriptions").FindOne(a.ctx, filter).Decode(sub)
	if err != nil {
		if err == mdb.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}

	return sub, nil
}

// SubsForUser loads all 订阅 of a given 用户. It does NOT load Public, Trusted or Private values,
// 不加载已删除的订阅。
func (a *adapter) SubsForUser(user t.Uid) ([]t.Subscription, error) {
	filter := b.M{"user": user.String(), "deletedat": b.M{"$exists": false}}

	cur, err := a.db.Collection("subscriptions").Find(a.ctx, filter)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var subs []t.Subscription
	for cur.Next(a.ctx) {
		var ss t.Subscription
		if err := cur.Decode(&ss); err != nil {
			return nil, err
		}
		ss.Private = nil
		subs = append(subs, ss)
	}

	return subs, cur.Err()
}

// SubsForTopic 获取 Topic 的所有订阅。不加载 Public 值，不加载 Channel 读者。
// UsersForTopic 与 SubsForTopic 的区别在于前者加载 用户.public+trusted，后者不加载。
func (a *adapter) SubsForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	filter := b.M{"topic": topic}
	if !keepDeleted {
		filter["deletedat"] = b.M{"$exists": false}
	}

	limit := a.maxResults
	if opts != nil {
		// 忽略 IfModifiedSince - 我们必须返回所有条目
		// 未修改的条目将被去除 Public、Trusted 和 Private。

		if !opts.User.IsZero() {
			filter["user"] = opts.User.String()
		}
		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}
	findOpts := new(mdbopts.FindOptions).SetLimit(int64(limit))

	cur, err := a.db.Collection("subscriptions").Find(a.ctx, filter, findOpts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var subs []t.Subscription
	for cur.Next(a.ctx) {
		var ss t.Subscription
		if err := cur.Decode(&ss); err != nil {
			return nil, err
		}
		ss.Private = unmarshalBsonD(ss.Private)
		subs = append(subs, ss)
	}

	return subs, cur.Err()
}

// SubsUpdate 更新订阅对象的部分字段。不需要更新的字段传 nil
func (a *adapter) SubsUpdate(topic string, user t.Uid, update map[string]any) error {
	// 将 CamelCase 字段名转换为小写。
	update = normalizeUpdateMap(update)

	filter := b.M{}
	if !user.IsZero() {
		// 更新单个 Topic 订阅
		filter["_id"] = topic + ":" + user.String()
	} else {
		// 更新所有 Topic 订阅
		filter["topic"] = topic
	}
	_, err := a.db.Collection("subscriptions").UpdateOne(a.ctx, filter, b.M{"$set": update})
	return err
}

// SubsDelete marks at most one 订阅 as deleted (soft-deleting).
func (a *adapter) SubsDelete(topic string, user t.Uid) error {
	var sess mdb.Session
	var err error

	if sess, err = a.conn.StartSession(); err != nil {
		return err
	}
	defer sess.EndSession(a.ctx)

	if err = a.maybeStartTransaction(sess); err != nil {
		return err
	}

	forUser := user.String()

	return mdb.WithSession(a.ctx, sess, func(sc mdb.SessionContext) error {
		if err := a.subsDelete(sc, b.M{"_id": topic + ":" + forUser}, false); err != nil {
			return err
		}

		// Channel readers cannot delete 消息.
		if !t.IsChannel(topic) {

			// 删除用户的 dellog 条目。
			if _, err := a.db.Collection("dellog").DeleteMany(sc, b.M{"topic": topic, "deletedfor": forUser}); err != nil {
				return err
			}

			// Delete 用户's markings of soft-deleted 消息
			filter := b.M{"topic": topic, "deletedfor.user": forUser}
			if _, err := a.db.Collection("messages").
				UpdateMany(sc, filter, b.M{"$pull": b.M{"deletedfor": b.M{"user": forUser}}}); err != nil {
				return err
			}
		}

		if t.GetTopicCat(topic) == t.TopicCatGrp {
			// Decrement Topic 订阅 count (only one 订阅 is	deleted).
			if err := a.topicUpdate(topic, b.M{"subcnt": -1}); err != nil {
				return err
			}
		}

		// 提交更改。
		return a.maybeCommitTransaction(sc, sess)
	})
}

// clearUserDellog 删除指定用户的所有 dellog 条目和 deletedfor 标记。
func (a *adapter) clearUserDellog(sc mdb.SessionContext, forUser string) error {
	topics, err := a.db.Collection("subscriptions").Distinct(sc, "topic",
		b.M{"user": forUser, "deletedat": b.M{"$exists": false}})
	if err != nil {
		return err
	}

	// 无需将 Channel 名称转换为群组名称：
	// Channel 读者无法删除消息。

	if len(topics) > 0 {
		// 删除用户的 dellog 条目。
		if _, err = a.db.Collection("dellog").DeleteMany(sc,
			b.M{"topic": b.M{"$in": topics}, "deletedfor": forUser}); err != nil {
			return err
		}

		// Delete 用户's markings of soft-deleted 消息
		filter := b.M{"topic": b.M{"$in": topics}, "deletedfor.user": forUser}
		if _, err = a.db.Collection("messages").
			UpdateMany(sc, filter, b.M{"$pull": b.M{"deletedfor": b.M{"user": forUser}}}); err != nil {
			return err
		}
	}

	return nil
}

// 删除/标记删除订阅并递减 Topic 中的 subcnt。
func (a *adapter) subsDelete(ctx context.Context, filter b.M, hard bool) error {
	// 首先，递减所有受影响 Topic 的订阅计数。
	// 分两步完成，因为 MongoDB 不支持等效的 'UPDATE .. LEFT JOIN ...'。
	filterWithDeletedAt := copyBsonMap(filter)
	filterWithDeletedAt["deletedat"] = b.M{"$exists": false}
	cur, err := a.db.Collection("subscriptions").Find(ctx, filterWithDeletedAt,
		mdbopts.Find().SetProjection(b.D{{Key: "topic", Value: 1}, {Key: "_id", Value: 0}}))
	if err != nil {
		return err
	}
	defer cur.Close(ctx)
	var topics []string
	for cur.Next(ctx) {
		var result struct {
			Topic string `bson:"topic"`
		}
		if err = cur.Decode(&result); err != nil {
			return err
		}
		if t.IsChannel(result.Topic) {
			// 将 Channel 名称转换为群组名称。
			topics = append(topics, t.ChnToGrp(result.Topic))
		}
		topics = append(topics, result.Topic)
	}

	if err = cur.Err(); err != nil {
		return err
	}

	if len(topics) > 0 {
		// Decrement 订阅 count in affected Topic.
		a.db.Collection("topics").UpdateMany(ctx,
			b.M{"_id": b.M{"$in": topics}},
			b.M{"$inc": b.M{"subcnt": -1}})
	}

	// Now delete or mark deleted the 订阅.
	if hard {
		_, err = a.db.Collection("subscriptions").DeleteMany(ctx, filter)
	} else {
		now := t.TimeNow()
		_, err = a.db.Collection("subscriptions").UpdateMany(ctx, filterWithDeletedAt,
			b.M{"$set": b.M{"updatedat": now, "deletedat": now}})
	}
	return err
}

// Find 根据标签列表搜索联系人和 Topic。
func (a *adapter) Find(caller, prefPrefix string, req [][]string, opt []string, activeOnly bool) ([]t.Subscription, error) {
	/*
		// 使用 unionWith 的 MongoDB 聚合管道。
		[
			{ $match: { tags: { $in: ["basic:alice", "travel"] } } },
			{ $unionWith: {
					coll: "topics",
					pipeline: [ { $match: { tags: { $in: ["basic:alice", "travel"] } } } ]
				}
			},
			{ $project: { _id: 1, access: 1, createdat: 1, updatedat: 1, usebt: 1, public: 1, trusted: 1, tags: 1, _source: 1 } },
			{ $addFields: { matchedCount: { $sum: { $map: {
				input: { $setIntersection: [ "$tags", [ "alias:aliassa", "basic:alice", "travel" ] ] },
				as: "tag",
				in: { $cond: { if: { $regexMatch: { input: "$$tag", regex: "^alias:"} }, then: 20, else: 1 } }
			} }}}},
			{ $match: { $expr: { $ne: [ { $size: { $setIntersection: [ "$tags", ["basic:alice", "travel"] ] } }, 0 ] } } },
			{ $sort: { matchedCount: -1 } },
			{ $limit: 20 }
		]

		// 使用 $facet 的替代方法（据说）性能更好：
		[ { $facet: {
					users: [
						{ $match: { tags: { $in: [ "alias:alice", "basic:alice", "travel" ] } } },
						{ $project: { _id: 1, access: 1, createdat: 1, updatedat: 1, usebt: 1, public: 1, trusted: 1, tags: 1 } }
					],
					topics: [
						{ $lookup: {
							from: "topics",
							pipeline: [
								{ $match: { tags: { $in: [ "alias:alice", "basic:alice", "travel" ] } } },
								{ $project: { _id: 1, access: 1, createdat: 1, updatedat: 1, usebt: 1, public: 1, trusted: 1, tags: 1 } } }
							],
							as: "topicDocs"
						}},
						{ $unwind: "$topicDocs" },
						{ $replaceRoot: { newRoot: "$topicDocs" } }
					]
				}
			},
			{ $project: { combined: { $concatArrays: ["$users", "$topics"] } } },
			{ $unwind: "$combined" },
			{ $replaceRoot: { newRoot: "$combined" } },
			{ $group: { _id: "$_id", doc: { $first: "$$ROOT" } } },
			{ $replaceRoot: { newRoot: "$doc" } },
			{ $addFields: { matchedCount:
				{ $sum: { $map: { input:
					{ $setIntersection: [ "$tags", [ "alias:alice", "basic:alice", "travel" ] ] },
					as: "tag",
					in: {
					$cond: {
						if: { $regexMatch: { input: "$$tag", regex: "^alias:" } }, then: 20, else: 1 }
					}
				} }
			} } },
			{ $match: { $expr: { $ne: [
				{ $size: { $setIntersection: [ "$tags", [ "alias:alice", "basic:alice", "travel" ] ] } },
				0
			] } } },
			{ $sort: { matchedCount: -1 } },
			{ $limit: 20 }
		]
	*/

	index := make(map[string]struct{})
	allReq := t.FlattenDoubleSlice(req)
	var allTags []any
	for _, tag := range append(allReq, opt...) {
		allTags = append(allTags, tag)
		index[tag] = struct{}{}
	}

	matchOn := b.M{"tags": b.M{"$in": allTags}}
	if activeOnly {
		matchOn["state"] = b.M{"$eq": t.StateOK}
	}

	projectFields := b.M{"_id": 1, "createdat": 1, "updatedat": 1, "usebt": 1,
		"access": 1, "subcnt": 1, "public": 1, "trusted": 1, "tags": 1}

	pipeline := b.A{
		// 阶段 1：$facet
		b.M{
			"$facet": b.D{
				{Key: "users", Value: b.A{
					b.M{"$match": matchOn},
					b.M{"$project": projectFields},
				}},
				{Key: "topics", Value: b.A{
					b.M{"$lookup": b.D{
						{Key: "from", Value: "topics"},
						{Key: "pipeline", Value: b.A{
							b.M{"$match": matchOn},
							b.M{"$project": projectFields},
						}},
						{Key: "as", Value: "topicDocs"},
					}},
					b.M{"$unwind": "$topicDocs"},
					b.M{"$replaceRoot": b.M{"newRoot": "$topicDocs"}},
				}},
			},
		},
		// 阶段 2：$project
		b.M{"$project": b.M{"combined": b.M{"$concatArrays": b.A{"$users", "$topics"}}}},
		// 阶段 3：$unwind
		b.M{"$unwind": "$combined"},
		// 阶段 4：$replaceRoot
		b.M{"$replaceRoot": b.M{"newRoot": "$combined"}},
		// 阶段 5：$group
		b.M{"$group": b.D{{Key: "_id", Value: "$_id"}, {Key: "doc", Value: b.M{"$first": "$$ROOT"}}}},
		// 阶段 6：$replaceRoot
		b.M{"$replaceRoot": b.M{"newRoot": "$doc"}},
		// 阶段 7：$addFields
		b.M{"$addFields": b.M{"matchedCount": b.M{"$sum": b.M{"$map": b.D{
			{Key: "input", Value: b.M{"$setIntersection": b.A{"$tags", allTags}}},
			{Key: "as", Value: "tag"},
			{Key: "in", Value: b.D{
				{Key: "$cond", Value: b.D{
					{Key: "if", Value: b.M{"$regexMatch": b.D{
						{Key: "input", Value: "$$tag"},
						{Key: "regex", Value: "^alias:"},
					},
					}},
					{Key: "then", Value: 20},
					{Key: "else", Value: 1},
				}}}}},
		}}}},
	}

	// 确保必需标签存在。
	for _, reqDisjunction := range req {
		if len(reqDisjunction) == 0 {
			continue
		}
		var reqTags []any
		for _, tag := range reqDisjunction {
			reqTags = append(reqTags, tag)
		}
		// 过滤掉 'tags' 与 'reqTags' 交集为空数组的文档。
		pipeline = append(pipeline,
			b.M{"$match": b.M{"$expr": b.M{"$ne": b.A{b.M{"$size": b.M{"$setIntersection": b.A{"$tags", reqTags}}}, 0}}}})
	}

	pipeline = append(pipeline,
		// 阶段 9：$sort
		b.M{"$sort": b.D{{Key: "matchedCount", Value: -1}, {Key: "subcnt", Value: -1}}},
		// 阶段 10：$limit
		b.M{"$limit": a.maxResults},
	)

	cur, err := a.db.Collection("users").Aggregate(a.ctx, pipeline)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var subs []t.Subscription
	for cur.Next(a.ctx) {
		var topic t.Topic
		var sub t.Subscription
		if err = cur.Decode(&topic); err != nil {
			break
		}

		if topic.UseBt {
			// 这是一个 Channel，将 grp 转换为 chn 名称：所有支持 Channel 的
			// Topic 在搜索结果中应以 Channel 形式出现。
			sub.Topic = t.GrpToChn(topic.Id)
		} else {
			if uid := t.ParseUid(topic.Id); !uid.IsZero() {
				topic.Id = uid.UserId()
				if topic.Id == caller {
					// 跳过调用者自身。
					continue
				}
			}
			sub.Topic = topic.Id
		}

		sub.CreatedAt = topic.CreatedAt
		sub.UpdatedAt = topic.UpdatedAt
		sub.SetSubCnt(topic.SubCnt)
		sub.SetPublic(unmarshalBsonD(topic.Public))
		sub.SetTrusted(unmarshalBsonD(topic.Trusted))
		sub.SetDefaultAccess(topic.Access.Auth, topic.Access.Anon)
		// 表示模式未设置，不是 'N'。
		sub.ModeGiven = t.ModeUnset
		sub.ModeWant = t.ModeUnset
		sub.Private = common.FilterFoundTags(topic.Tags, index)
		subs = append(subs, sub)
	}
	if err == nil {
		err = cur.Err()
	}

	return subs, err
}

// FindOne returns the first Topic or 用户 which matches the given tag.
func (a *adapter) FindOne(tag string) (string, error) {
	// Part of the pipeline identical for 用户 and Topic collections.
	commonPipe := b.A{b.M{"$match": b.M{"tags": tag}}, b.M{"$project": b.M{"_id": 1}}}

	// 必须创建 commonPipe 的副本，以便原始 commonPipe 可在 $unionWith 中不受修改地使用。
	pipeline := append(slices.Clone(commonPipe),
		b.M{"$unionWith": b.M{"coll": "topics", "pipeline": commonPipe}},
		b.M{"$limit": 1})
	cur, err := a.db.Collection("users").Aggregate(a.ctx, pipeline)
	if err != nil {
		return "", err
	}
	defer cur.Close(a.ctx)

	var found string
	if cur.Next(a.ctx) {
		entry := map[string]any{}
		if err = cur.Decode(&entry); err != nil {
			return "", err
		}

		if id, ok := entry["_id"].(string); ok {
			if user := t.ParseUid(id); !user.IsZero() {
				found = user.UserId()
			} else {
				found = id
			}
		}
	}

	return found, cur.Err()
}

// 消息

// MessageSave saves 消息 to 数据库
func (a *adapter) MessageSave(msg *t.Message) error {
	_, err := a.db.Collection("messages").InsertOne(a.ctx, msg)
	return err
}

// MessageGetAll returns 消息 matching the query.
func (a *adapter) MessageGetAll(topic string, forUser t.Uid, opts *t.QueryOpt) ([]t.Message, error) {
	var limit = a.maxMessageResults
	var lower, upper int
	requester := forUser.String()
	if opts != nil {
		if opts.Since > 0 {
			lower = opts.Since
		}
		if opts.Before > 0 {
			upper = opts.Before
		}

		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}
	filter := b.M{
		"topic":           topic,
		"delid":           b.M{"$exists": false},
		"deletedfor.user": b.M{"$ne": requester},
	}
	if upper == 0 {
		filter["seqid"] = b.M{"$gte": lower}
	} else {
		filter["seqid"] = b.M{"$gte": lower, "$lt": upper}
	}
	findOpts := mdbopts.Find().SetSort(b.D{{Key: "topic", Value: -1}, {Key: "seqid", Value: -1}})
	findOpts.SetLimit(int64(limit))

	cur, err := a.db.Collection("messages").Find(a.ctx, filter, findOpts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var msgs []t.Message
	for cur.Next(a.ctx) {
		var msg t.Message
		if err = cur.Decode(&msg); err != nil {
			return nil, err
		}
		msg.Content = unmarshalBsonD(msg.Content)
		msgs = append(msgs, msg)
	}

	return msgs, nil
}

func (a *adapter) messagesHardDelete(topic string) error {
	var err error

	// 扣减关联文件附件的使用计数 (decFileUseCounter 在下文执行)
	filter := b.M{"topic": topic}
	if _, err = a.db.Collection("dellog").DeleteMany(a.ctx, filter); err != nil {
		return err
	}

	if err = a.decFileUseCounter(a.ctx, "messages", filter); err != nil {
		return err
	}

	if _, err = a.db.Collection("messages").DeleteMany(a.ctx, filter); err != nil {
		return err
	}

	return err
}

// rangeToFilter 是 Mongo 中等效于 common.RangeToSql 的实现。
func rangeToFilter(delRanges []t.Range, filter b.M) b.M {
	if len(delRanges) > 1 || delRanges[0].Hi == 0 {
		rangeFilter := b.A{}
		for _, rng := range delRanges {
			if rng.Hi == 0 {
				rangeFilter = append(rangeFilter, b.M{"seqid": rng.Low})
			} else {
				rangeFilter = append(rangeFilter, b.M{"seqid": b.M{"$gte": rng.Low, "$lt": rng.Hi}})
			}
		}
		filter["$or"] = rangeFilter
	} else {
		filter["seqid"] = b.M{"$gte": delRanges[0].Low, "$lt": delRanges[0].Hi}
	}
	return filter
}

// MessageDeleteList marks 消息 as deleted.
// 软删除还是硬删除取决于 forUser 值：forUser.IsZero() == true 为硬删除。
func (a *adapter) MessageDeleteList(topic string, toDel *t.DelMessage) error {
	var err error

	if toDel == nil {
		// No filter: delete all 消息.
		return a.messagesHardDelete(topic)
	}

	// Only some 消息 are being deleted

	delRanges := toDel.SeqIdRanges
	filter := b.M{
		"topic": topic,
		// Skip already hard-deleted 消息.
		"delid": b.M{"$exists": false},
	}
	// Mongo 中等效于 common.RangeToSql
	rangeToFilter(delRanges, filter)

	if toDel.DeletedFor == "" {
		// Hard-deleting 消息 requires updates to the 消息 table.

		// We are asked to delete 消息 no older than newerThan.
		if newerThan := toDel.GetNewerThan(); newerThan != nil {
			filter["createdat"] = b.M{"$gt": newerThan}
		}

		pipeline := b.A{
			b.M{"$match": filter},
			b.M{"$project": b.M{"seqid": 1}},
		}

		// Find the actual IDs still present in the 数据库.

		cur, err := a.db.Collection("messages").Aggregate(a.ctx, pipeline)
		if err != nil {
			return err
		}
		defer cur.Close(a.ctx)

		var seqIDs []int
		for cur.Next(a.ctx) {
			var result struct {
				SeqID int `bson:"seqid"`
			}
			if err = cur.Decode(&result); err != nil {
				return err
			}
			seqIDs = append(seqIDs, result.SeqID)
		}

		if len(seqIDs) == 0 {
			// 无需删除。无需记录日志。完成。
			return nil
		}

		// 重新计算实际要删除的范围。
		sort.Ints(seqIDs)
		delRanges = t.SliceToRanges(seqIDs)

		// 用新范围组成新查询。
		filter = b.M{
			"topic": topic,
		}
		rangeToFilter(delRanges, filter)

		if err = a.decFileUseCounter(a.ctx, "messages", filter); err != nil {
			return err
		}
		// Hard-delete individual 消息. 消息 is not deleted but all fields with content
		// 被替换为 null。
		_, err = a.db.Collection("messages").UpdateMany(a.ctx, filter, b.M{"$set": b.M{
			"deletedat":   t.TimeNow(),
			"delid":       toDel.DelId,
			"from":        "",
			"head":        nil,
			"content":     nil,
			"attachments": nil}})
	} else {
		// 软删除：将 DelId 添加到 DeletedFor

		// Skip 消息 already soft-deleted for the current 用户
		filter["deletedfor.user"] = b.M{"$ne": toDel.DeletedFor}

		_, err = a.db.Collection("messages").UpdateMany(a.ctx, filter,
			b.M{"$addToSet": b.M{
				"deletedfor": &t.SoftDelete{
					User:  toDel.DeletedFor,
					DelId: toDel.DelId,
				}}})
	}

	// 记录日志。硬删除和软删除都需要。
	if _, err = a.db.Collection("dellog").InsertOne(a.ctx, toDel); err != nil {
		return err
	}

	if toDel.DelId > 0 {
		if err = a.TopicUpdate(topic, map[string]any{"DelId": toDel.DelId}); err != nil {
			return err
		}
		forUser := t.ParseUserId(toDel.DeletedFor)
		if err = a.SubsUpdate(topic, forUser, map[string]any{"DelId": toDel.DelId}); err != nil {
			return err
		}
	}

	return nil
}

// MessageGetDeleted returns a list of deleted 消息 Ids.
func (a *adapter) MessageGetDeleted(topic string, forUser t.Uid, opts *t.QueryOpt) ([]t.DelMessage, error) {
	var limit = a.maxResults
	var lower, upper int
	if opts != nil {
		if opts.Since > 0 {
			lower = opts.Since
		}
		if opts.Before > 0 {
			upper = opts.Before
		}
		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}
	filter := b.M{
		"topic": topic,
		"$or": b.A{
			b.M{"deletedfor": forUser.String()},
			b.M{"deletedfor": ""},
		}}
	if upper == 0 {
		filter["delid"] = b.M{"$gte": lower}
	} else {
		filter["delid"] = b.M{"$gte": lower, "$lt": upper}
	}
	findOpts := mdbopts.Find().
		SetSort(b.D{{Key: "topic", Value: 1}, {Key: "delid", Value: 1}}).
		SetLimit(int64(limit))

	cur, err := a.db.Collection("dellog").Find(a.ctx, filter, findOpts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var dmsgs []t.DelMessage
	if err = cur.All(a.ctx, &dmsgs); err != nil {
		return nil, err
	}

	return dmsgs, nil
}

// 设备管理（用于推送通知）。

// DeviceUpsert 创建或更新设备记录。
func (a *adapter) DeviceUpsert(uid t.Uid, dev *t.DeviceDef) error {
	userId := uid.String()
	var user t.User
	err := a.db.Collection("users").FindOne(a.ctx, b.M{
		"_id":              userId,
		"devices.deviceid": dev.DeviceId}).Decode(&user)

	if err == nil && user.Id != "" { // current 用户 owns this device
		// 使用 ArrayFilter 避免添加重复设备对象，而是更新该设备数据
		updOpts := mdbopts.Update().SetArrayFilters(mdbopts.ArrayFilters{
			Filters: []any{b.M{"dev.deviceid": dev.DeviceId}}})
		_, err = a.db.Collection("users").UpdateOne(a.ctx,
			b.M{"_id": userId},
			b.M{"$set": b.M{
				"devices.$[dev].platform": dev.Platform,
				"devices.$[dev].lastseen": dev.LastSeen,
				"devices.$[dev].lang":     dev.Lang}},
			updOpts)
		return err
	} else if err == mdb.ErrNoDocuments { // device is free or owned by other 用户
		err = a.deviceInsert(userId, dev)

		if isDuplicateErr(err) {
			// Other 用户 owns this device.
			// We need to delete this device from that 用户 and then insert again
			if _, err = a.db.Collection("users").UpdateOne(a.ctx,
				b.M{"devices.deviceid": dev.DeviceId},
				b.M{"$pull": b.M{"devices": b.M{"deviceid": dev.DeviceId}}}); err != nil {

				return err
			}
			return a.deviceInsert(userId, dev)
		}
		if err != nil {
			return err
		}
		return nil
	}

	return err
}

// deviceInsert adds device object to 用户.devices array
func (a *adapter) deviceInsert(userId string, dev *t.DeviceDef) error {
	filter := b.M{"_id": userId}
	_, err := a.db.Collection("users").UpdateOne(a.ctx, filter,
		b.M{"$push": b.M{"devices": dev}})

	if err != nil && strings.Contains(err.Error(), "must be an array") {
		// 'devices' 字段不是数组。将其转为以 'dev' 为首个元素的数组
		_, err = a.db.Collection("users").UpdateOne(a.ctx, filter,
			b.M{"$set": b.M{"devices": []any{dev}}})
	}

	return err
}

// DeviceGetAll returns all devices for a given set of 用户.
func (a *adapter) DeviceGetAll(uids ...t.Uid) (map[t.Uid][]t.DeviceDef, int, error) {
	ids := make([]any, len(uids))
	for i, id := range uids {
		ids[i] = id.String()
	}

	filter := b.M{"_id": b.M{"$in": ids}}
	findOpts := mdbopts.Find().SetProjection(b.M{"_id": 1, "devices": 1})
	cur, err := a.db.Collection("users").Find(a.ctx, filter, findOpts)
	if err != nil {
		return nil, 0, err
	}
	defer cur.Close(a.ctx)

	result := make(map[t.Uid][]t.DeviceDef)
	count := 0
	var uid t.Uid
	for cur.Next(a.ctx) {
		var row struct {
			Id      string `bson:"_id"`
			Devices []t.DeviceDef
		}
		if err = cur.Decode(&row); err != nil {
			return nil, 0, err
		}
		if len(row.Devices) > 0 {
			if err := uid.UnmarshalText([]byte(row.Id)); err != nil {
				continue
			}

			result[uid] = row.Devices
			count++
		}
	}
	return result, count, cur.Err()
}

// DeviceDelete 删除设备记录（推送令牌）。
func (a *adapter) DeviceDelete(uid t.Uid, deviceID string) error {
	var err error
	filter := b.M{"_id": uid.String()}
	update := b.M{}
	if deviceID == "" {
		update["$set"] = b.M{"devices": []any{}}
	} else {
		update["$pull"] = b.M{"devices": b.M{"deviceid": deviceID}}
	}
	_, err = a.db.Collection("users").UpdateOne(a.ctx, filter, update)
	return err
}

// 文件上传记录。文件存储在数据库之外。

// FileStartUpload 初始化文件上传
func (a *adapter) FileStartUpload(fd *t.FileDef) error {
	_, err := a.db.Collection("fileuploads").InsertOne(a.ctx, fd)
	return err
}

// FileFinishUpload 标记文件上传完成，无论成功与否。
func (a *adapter) FileFinishUpload(fd *t.FileDef, success bool, size int64) (*t.FileDef, error) {
	now := t.TimeNow()
	if success {
		// 标记上传完成。
		if _, err := a.db.Collection("fileuploads").UpdateOne(a.ctx,
			b.M{"_id": fd.Id},
			b.M{"$set": b.M{
				"updatedat": now,
				"status":    t.UploadCompleted,
				"size":      size,
				"etag":      fd.ETag,
				"location":  fd.Location,
			}}); err != nil {

			return nil, err
		}
		fd.Status = t.UploadCompleted
		fd.Size = size
	} else {
		// 删除记录：已无用。
		if _, err := a.db.Collection("fileuploads").DeleteOne(a.ctx, b.M{"_id": fd.Id}); err != nil {
			return nil, err
		}
		fd.Status = t.UploadFailed
		fd.Size = 0
	}

	fd.UpdatedAt = now

	return fd, nil
}

// FileGet 获取指定文件的记录
func (a *adapter) FileGet(fid string) (*t.FileDef, error) {
	var fd t.FileDef
	err := a.db.Collection("fileuploads").FindOne(a.ctx, b.M{"_id": fid}).Decode(&fd)
	if err != nil {
		if err == mdb.ErrNoDocuments {
			return nil, nil
		}
		return nil, err
	}

	return &fd, nil
}

// FileDeleteUnused 删除 UseCount 为零的记录。若 olderThan 非零，则删除
// UpdatedAt 早于 olderThan 的未使用记录。
// 返回已删除文件记录的 FileDef.Location 数组，以便同时删除实际文件。
func (a *adapter) FileDeleteUnused(olderThan time.Time, limit int) ([]string, error) {
	findOpts := mdbopts.Find()
	filter := b.M{"$or": b.A{
		b.M{"usecount": 0},
		b.M{"usecount": b.M{"$exists": false}}}}
	if !olderThan.IsZero() {
		filter["updatedat"] = b.M{"$lt": olderThan}
	}
	if limit > 0 {
		findOpts.SetLimit(int64(limit))
	}

	findOpts.SetProjection(b.M{"location": 1, "_id": 0})
	cur, err := a.db.Collection("fileuploads").Find(a.ctx, filter, findOpts)
	if err != nil {
		return nil, err
	}
	defer cur.Close(a.ctx)

	var locations []string
	for cur.Next(a.ctx) {
		var result map[string]string
		if err := cur.Decode(&result); err != nil {
			return nil, err
		}
		locations = append(locations, result["location"])
	}

	_, err = a.db.Collection("fileuploads").DeleteMany(a.ctx, filter)
	return locations, err
}

// 给定针对 '消息' 集合的过滤查询，递减 'fileuploads' 表中对应的使用计数器。
func (a *adapter) decFileUseCounter(ctx context.Context, collection string, msgFilter b.M) error {
	// 复制 msgFilter
	filter := b.M{}
	for k, v := range msgFilter {
		filter[k] = v
	}
	filter["attachments"] = b.M{"$exists": true}
	fileIds, err := a.db.Collection(collection).Distinct(ctx, "attachments", filter)
	if err != nil {
		return err
	}

	if len(fileIds) > 0 {
		_, err = a.db.Collection("fileuploads").UpdateMany(ctx,
			b.M{"_id": b.M{"$in": fileIds}},
			b.M{"$inc": b.M{"usecount": -1}})
	}

	return err
}

// FileLinkAttachments connects given Topic or 消息 to the file record IDs from the list.
func (a *adapter) FileLinkAttachments(topic string, userId, msgId t.Uid, fids []string) error {
	if len(fids) == 0 || (topic == "" && userId.IsZero() && msgId.IsZero()) {
		return t.ErrMalformed
	}

	now := t.TimeNow()
	var err error

	if msgId.IsZero() {
		// Only one link per 用户 or Topic is permitted.
		fids = fids[0:1]

		// Topic and 用户 and mutable. Must unlink the previous attachments first.
		var table string
		var linkId string
		if topic != "" {
			table = "topics"
			linkId = topic
		} else {
			table = "users"
			linkId = userId.String()
		}

		// 查找旧附件。
		var attachments map[string][]string
		findOpts := mdbopts.FindOne().SetProjection(b.M{"attachments": 1, "_id": 0})
		err = a.db.Collection(table).FindOne(a.ctx, b.M{"_id": linkId}, findOpts).Decode(&attachments)
		if err != nil {
			return err
		}

		if len(attachments["attachments"]) > 0 {
			// 递减旧附件的使用计数。
			if _, err = a.db.Collection("fileuploads").UpdateOne(a.ctx,
				b.M{"_id": attachments["attachments"][0]},
				b.M{
					"$set": b.M{"updatedat": now},
					"$inc": b.M{"usecount": -1},
				},
			); err != nil {
				return err
			}
		}

		_, err = a.db.Collection(table).UpdateOne(a.ctx,
			b.M{"_id": linkId},
			b.M{"$set": b.M{"updatedat": now, "attachments": fids}})
		if err != nil {
			return err
		}
	} else {
		_, err = a.db.Collection("messages").UpdateOne(a.ctx,
			b.M{"_id": msgId.String()},
			b.M{"$set": b.M{"updatedat": now, "attachments": fids}})
		if err != nil {
			return err
		}
	}

	ids := make([]any, len(fids))
	for i, id := range fids {
		ids[i] = id
	}
	_, err = a.db.Collection("fileuploads").UpdateMany(a.ctx,
		b.M{"_id": b.M{"$in": ids}},
		b.M{
			"$set": b.M{"updatedat": now},
			"$inc": b.M{"usecount": 1},
		},
	)

	return err
}

// PCacheGet 读取持久缓存条目。
func (a *adapter) PCacheGet(key string) (string, error) {
	var value map[string]string
	findOpts := mdbopts.FindOneOptions{Projection: b.M{"value": 1, "_id": 0}}
	if err := a.db.Collection("kvmeta").FindOne(a.ctx, b.M{"_id": key}, &findOpts).Decode(&value); err != nil {
		if err == mdb.ErrNoDocuments {
			err = t.ErrNotFound
		}
		return "", err
	}
	return value["value"], nil
}

// PCacheUpsert 创建或更新持久缓存条目。
func (a *adapter) PCacheUpsert(key string, value string, failOnDuplicate bool) error {
	if strings.Contains(key, "^") {
		// 不允许键中包含 ^：它会干扰 $match 查询。
		return t.ErrMalformed
	}

	collection := a.db.Collection("kvmeta")
	doc := b.M{
		"value": value,
	}

	if failOnDuplicate {
		doc["_id"] = key
		doc["createdat"] = t.TimeNow()
		_, err := collection.InsertOne(a.ctx, doc)
		if mdb.IsDuplicateKeyError(err) {
			err = t.ErrDuplicate
		}
		return err
	}

	res := collection.FindOneAndUpdate(a.ctx, b.M{"_id": key}, b.M{"$set": doc},
		mdbopts.FindOneAndUpdate().SetUpsert(true).SetReturnDocument(mdbopts.After))
	return res.Err()
}

// PCacheDelete 删除一条持久缓存条目。
func (a *adapter) PCacheDelete(key string) error {
	_, err := a.db.Collection("kvmeta").DeleteOne(a.ctx, b.M{"_id": key})
	return err
}

// PCacheExpire 使具有给定键前缀的旧条目过期。
func (a *adapter) PCacheExpire(keyPrefix string, olderThan time.Time) error {
	if keyPrefix == "" {
		return t.ErrMalformed
	}

	_, err := a.db.Collection("kvmeta").DeleteMany(a.ctx, b.M{"createdat": b.M{"$lt": olderThan},
		"_id": primitive.Regex{Pattern: "^" + keyPrefix}})
	return err
}

// GetTestDB returns a currently open 数据库 connection.
func (a *adapter) GetTestDB() any {
	return a.db
}

func (a *adapter) isDbInitialized() bool {
	var result map[string]int

	findOpts := mdbopts.FindOneOptions{Projection: b.M{"value": 1, "_id": 0}}
	if err := a.db.Collection("kvmeta").FindOne(a.ctx, b.M{"_id": "version"}, &findOpts).Decode(&result); err != nil {
		return false
	}
	return true
}

// GetTestAdapter 返回适配器对象。用于运行测试。
func GetTestAdapter() *adapter {
	return &adapter{}
}

func init() {
	store.RegisterAdapter(&adapter{})
}

func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

func union(userTags, addTags []string) []string {
	for _, tag := range addTags {
		if !contains(userTags, tag) {
			userTags = append(userTags, tag)
		}
	}
	return userTags
}

func diff(userTags, removeTags []string) []string {
	var result []string
	for _, tag := range userTags {
		if !contains(removeTags, tag) {
			result = append(result, tag)
		}
	}
	return result
}

// normalizeUpdateMap 将硬编码为 CamelCase 的键转为小写（MongoDB 默认使用小写）
func normalizeUpdateMap(update map[string]any) map[string]any {
	result := make(map[string]any, len(update))
	for key, value := range update {
		result[strings.ToLower(key)] = value
	}

	return result
}

// 递归反序列化 bson.D 类型。
// Mongo 驱动反序列化为 'any' 时，对 map 创建 bson.D 对象，对 slice 创建 bson.A 对象。
// 需要手动将它们反序列化为正确的类型：分别对应 map[string]any 和 []any。
func unmarshalBsonD(bsonObj any) any {
	if obj, ok := bsonObj.(b.D); ok && len(obj) != 0 {
		result := make(map[string]any)
		for key, val := range obj.Map() {
			result[key] = unmarshalBsonD(val)
		}
		return result
	} else if obj, ok := bsonObj.(primitive.Binary); ok {
		// primitive.Binary 是包含 Subtype 和 Data 字段的结构体类型。我们只需要 Data（[]byte）
		return obj.Data
	} else if obj, ok := bsonObj.(b.A); ok {
		// 针对 bson.D 对象数组的情况
		var result []any
		for _, elem := range obj {
			result = append(result, unmarshalBsonD(elem))
		}
		return result
	}
	// 直接原样返回值
	return bsonObj
}

func copyBsonMap(mp b.M) b.M {
	result := b.M{}
	for k, v := range mp {
		result[k] = v
	}
	return result
}
func isDuplicateErr(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	return strings.Contains(msg, "duplicate key error")
}
