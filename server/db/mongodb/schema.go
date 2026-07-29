//go:build mongodb

package mongodb

import (
	"errors"
	"strconv"

	t "chat/server/store/types"

	b "go.mongodb.org/mongo-driver/v2/bson"
	mdb "go.mongodb.org/mongo-driver/v2/mongo"
	mdbopts "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// CreateDb 创建数据库，可选先删除已有数据库。
func (a *adapter) CreateDb(reset bool) error {
	if reset {
		if err := a.db.Drop(a.ctx); err != nil {
			return err
		}
	} else if a.isDbInitialized() {
		return errors.New("Database already initialized")
	}
	// 集合（表）无需显式创建，MongoDB 会在首次写入或创建索引时自动创建。
	// MongoDB 不支持关系型数据库的表/字段 COMMENT；下方每组索引前的注释
	// 同时承担集合用途和关键字段约束的数据库文档职责。

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
		// 客户端消息幂等键；旧消息不带 clientkey，不参与唯一约束。
		{
			Collection: "messages",
			IndexOpts: mdb.IndexModel{
				Keys: b.D{{Key: "topic", Value: 1}, {Key: "clientkey", Value: 1}},
				Options: mdbopts.Index().
					SetUnique(true).
					SetPartialFilterExpression(b.M{"clientkey": b.M{"$type": "string"}}),
			},
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
		{
			Collection: "messages",
			IndexOpts:  mdb.IndexModel{Keys: b.D{{Key: "topic", Value: 1}, {Key: "updatedat", Value: 1}, {Key: "seqid", Value: 1}}},
		},
		// scheduledmessages 保存尚未分配 Topic SeqId 的定时消息快照。
		// MongoDB 没有关系型数据库的表/字段 COMMENT，集合用途记录在此处。
		// 唯一索引保证同一发送者的 cid 重试不会创建第二条队列记录。
		{
			Collection: "scheduledmessages",
			IndexOpts: mdb.IndexModel{
				Keys:    b.D{{Key: "topic", Value: 1}, {Key: "from", Value: 1}, {Key: "clientid", Value: 1}},
				Options: mdbopts.Index().SetUnique(true),
			},
		},
		{
			Collection: "scheduledmessages",
			Field:      "publishat",
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
		if err := a.db.Collection("users").Indexes().DropOne(a.ctx, "deletedat_1"); err != nil {
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

	if a.version == 116 {
		// MongoDB 不支持字段 COMMENT；clientkey 的用途由索引和 Go 模型注释记录。
		if _, err := a.db.Collection("messages").Indexes().CreateOne(a.ctx, mdb.IndexModel{
			Keys: b.D{{Key: "topic", Value: 1}, {Key: "clientkey", Value: 1}},
			Options: mdbopts.Index().
				SetUnique(true).
				SetPartialFilterExpression(b.M{"clientkey": b.M{"$type": "string"}}),
		}); err != nil {
			return err
		}
		if err := bumpVersion(a, 117); err != nil {
			return err
		}
	}

	if a.version == 117 {
		// 数据库 117→118：增加消息修改游标索引和持久化定时队列索引。
		if _, err := a.db.Collection("messages").Indexes().CreateOne(a.ctx, mdb.IndexModel{
			Keys: b.D{{Key: "topic", Value: 1}, {Key: "updatedat", Value: 1}, {Key: "seqid", Value: 1}},
		}); err != nil {
			return err
		}
		// scheduledmessages 是持久化定时队列；MongoDB 无原生集合 COMMENT。
		if _, err := a.db.Collection("scheduledmessages").Indexes().CreateMany(a.ctx, []mdb.IndexModel{
			{
				Keys:    b.D{{Key: "topic", Value: 1}, {Key: "from", Value: 1}, {Key: "clientid", Value: 1}},
				Options: mdbopts.Index().SetUnique(true),
			},
			{Keys: b.D{{Key: "publishat", Value: 1}}},
		}); err != nil {
			return err
		}
		if err := bumpVersion(a, 118); err != nil {
			return err
		}
	}

	if a.version == 118 {
		// 数据库 118→119：为历史消息回填服务端搜索文本。
		// MongoDB 不支持字段 COMMENT；SearchText 的用途由 Go 模型注释记录。
		searchTextExpr := b.D{{Key: "$cond", Value: b.A{
			b.D{{Key: "$eq", Value: b.A{b.D{{Key: "$type", Value: "$content"}}, "string"}}},
			"$content",
			b.D{{Key: "$ifNull", Value: b.A{"$content.txt", ""}}},
		}}}
		if _, err := a.db.Collection("messages").UpdateMany(a.ctx, b.M{},
			mdb.Pipeline{b.D{{Key: "$set", Value: b.D{{Key: "searchtext", Value: searchTextExpr}}}}}); err != nil {
			return err
		}
		if err := bumpVersion(a, 119); err != nil {
			return err
		}
	}

	if a.version == 119 {
		// 数据库 119→120：为 Topic 回填集群 Owner 与 fencing epoch。
		// MongoDB 不支持字段 COMMENT；字段用途由 types.Topic 的中文注释记录。
		if _, err := a.db.Collection("topics").UpdateMany(a.ctx,
			b.M{"clusterepoch": b.M{"$exists": false}},
			b.M{"$set": b.M{"clusterowner": "", "clusterepoch": int64(0)}}); err != nil {
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
