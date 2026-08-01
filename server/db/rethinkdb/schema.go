//go:build rethinkdb
// +build rethinkdb

package rethinkdb

import (
	"errors"
	"strconv"

	t "chat/server/store/types"

	rdb "gopkg.in/rethinkdb/rethinkdb-go.v6"
)

// CreateDb 初始化存储。如果 reset 为 true，则先删除数据库，所有数据将丢失。
func (a *adapter) CreateDb(reset bool) error {

	// 如果数据库存在则删除，不存在则忽略错误。
	if reset {
		rdb.DBDrop(a.dbName).RunWrite(a.conn)
	}

	if _, err := rdb.DBCreate(a.dbName).RunWrite(a.conn); err != nil {
		return err
	}

	// RethinkDB 不支持关系型数据库的表/字段 COMMENT；每个 TableCreate 前的
	// 注释记录表用途，字段语义由对应 Go 持久化模型的字段注释说明。

	// 元数据键值对表。
	if _, err := rdb.DB(a.dbName).TableCreate("kvmeta", rdb.TableCreateOpts{PrimaryKey: "key"}).RunWrite(a.conn); err != nil {
		return err
	}

	// 用户
	if _, err := rdb.DB(a.dbName).TableCreate("users", rdb.TableCreateOpts{PrimaryKey: "Id"}).RunWrite(a.conn); err != nil {
		return err
	}
	// 在 State 上创建二级索引，用于查找已暂停和软删除的用户。
	if _, err := rdb.DB(a.dbName).Table("users").IndexCreate("State").RunWrite(a.conn); err != nil {
		return err
	}
	// 在用户.Tags 数组上创建二级索引，以便通过标签查找用户。
	if _, err := rdb.DB(a.dbName).Table("users").IndexCreate("Tags", rdb.IndexCreateOpts{Multi: true}).RunWrite(a.conn); err != nil {
		return err
	}
	// 在用户.Devices.<hash>.DeviceId 上创建二级索引，确保设备 ID 跨用户唯一
	if _, err := rdb.DB(a.dbName).Table("users").IndexCreateFunc("DeviceIds",
		func(row rdb.Term) any {
			devices := row.Field("Devices")
			return devices.Keys().Map(func(key rdb.Term) any {
				return devices.Field(key).Field("DeviceId")
			})
		}, rdb.IndexCreateOpts{Multi: true}).RunWrite(a.conn); err != nil {
		return err
	}

	// 用户认证记录 {unique, userid, secret}
	if _, err := rdb.DB(a.dbName).TableCreate("auth", rdb.TableCreateOpts{PrimaryKey: "unique"}).RunWrite(a.conn); err != nil {
		return err
	}
	// 应能通过用户 ID 访问用户的认证记录
	if _, err := rdb.DB(a.dbName).Table("auth").IndexCreate("userid").RunWrite(a.conn); err != nil {
		return err
	}

	// Topic 订阅。主键为 Topic:用户 字符串
	if _, err := rdb.DB(a.dbName).TableCreate("subscriptions", rdb.TableCreateOpts{PrimaryKey: "Id"}).RunWrite(a.conn); err != nil {
		return err
	}
	if _, err := rdb.DB(a.dbName).Table("subscriptions").IndexCreate("User").RunWrite(a.conn); err != nil {
		return err
	}
	if _, err := rdb.DB(a.dbName).Table("subscriptions").IndexCreate("Topic").RunWrite(a.conn); err != nil {
		return err
	}
	// Topic_User 复合索引用于官方大群成员稳定游标分页，避免全量内存排序。
	if _, err := rdb.DB(a.dbName).Table("subscriptions").IndexCreateFunc("Topic_User",
		func(row rdb.Term) any {
			return []any{row.Field("Topic"), row.Field("User")}
		}).RunWrite(a.conn); err != nil {
		return err
	}

	// 存储在数据库中的 Topic
	if _, err := rdb.DB(a.dbName).TableCreate("topics", rdb.TableCreateOpts{PrimaryKey: "Id"}).RunWrite(a.conn); err != nil {
		return err
	}
	// Owner 字段上的二级索引，用于删除用户。
	if _, err := rdb.DB(a.dbName).Table("topics").IndexCreate("Owner").RunWrite(a.conn); err != nil {
		return err
	}
	// 在 State 上创建二级索引，用于查找已暂停和软删除的 Topic。
	if _, err := rdb.DB(a.dbName).Table("topics").IndexCreate("State").RunWrite(a.conn); err != nil {
		return err
	}
	// Topic.Tags 数组上的二级索引，以便通过标签查找 Topic。
	// 这些标签不像用户.Tags 那样唯一。
	if _, err := rdb.DB(a.dbName).Table("topics").IndexCreate("Tags", rdb.IndexCreateOpts{Multi: true}).RunWrite(a.conn); err != nil {
		return err
	}
	// 创建系统 Topic 'sys'。
	if err := createSystemTopic(a); err != nil {
		return err
	}

	// 存储的消息
	if _, err := rdb.DB(a.dbName).TableCreate("messages", rdb.TableCreateOpts{PrimaryKey: "Id"}).RunWrite(a.conn); err != nil {
		return err
	}
	// Topic-seqID 复合索引，用于选择 Topic 中的消息。
	if _, err := rdb.DB(a.dbName).Table("messages").IndexCreateFunc("Topic_SeqId",
		func(row rdb.Term) any {
			return []any{row.Field("Topic"), row.Field("SeqId")}
		}).RunWrite(a.conn); err != nil {
		return err
	}
	// 客户端消息幂等键。
	if _, err := rdb.DB(a.dbName).Table("messages").IndexCreateFunc("Topic_ClientKey",
		func(row rdb.Term) any {
			return []any{row.Field("Topic"), row.Field("ClientKey")}
		}).RunWrite(a.conn); err != nil {
		return err
	}
	// 硬删除消息的复合索引
	if _, err := rdb.DB(a.dbName).Table("messages").IndexCreateFunc("Topic_DelId",
		func(row rdb.Term) any {
			return []any{row.Field("Topic"), row.Field("DelId")}
		}).RunWrite(a.conn); err != nil {
		return err
	}
	// 软删除消息的复合多索引：每条消息获得多个复合索引条目，如
	// [Topic, User1, DelId1], [Topic, User2, DelId2],...
	if _, err := rdb.DB(a.dbName).Table("messages").IndexCreateFunc("Topic_DeletedFor",
		func(row rdb.Term) any {
			return row.Field("DeletedFor").Map(func(df rdb.Term) any {
				return []any{row.Field("Topic"), df.Field("User"), df.Field("DelId")}
			})
		}, rdb.IndexCreateOpts{Multi: true}).RunWrite(a.conn); err != nil {
		return err
	}

	// scheduledmessages 保存尚未分配 Topic SeqId 的定时消息快照。
	// RethinkDB 不支持表/字段 COMMENT，表用途记录在建表代码和 Go 模型注释中。
	if _, err := rdb.DB(a.dbName).TableCreate("scheduledmessages",
		rdb.TableCreateOpts{PrimaryKey: "Id"}).RunWrite(a.conn); err != nil {
		return err
	}
	if _, err := rdb.DB(a.dbName).Table("scheduledmessages").IndexCreate("PublishAt").RunWrite(a.conn); err != nil {
		return err
	}
	if _, err := rdb.DB(a.dbName).Table("scheduledmessages").IndexCreateFunc("Topic_From_ClientId",
		func(row rdb.Term) any {
			return []any{row.Field("Topic"), row.Field("From"), row.Field("ClientId")}
		}).RunWrite(a.conn); err != nil {
		return err
	}

	// 已删除消息的日志
	if _, err := rdb.DB(a.dbName).TableCreate("dellog", rdb.TableCreateOpts{PrimaryKey: "Id"}).RunWrite(a.conn); err != nil {
		return err
	}
	if _, err := rdb.DB(a.dbName).Table("dellog").IndexCreateFunc("Topic_DelId",
		func(row rdb.Term) any {
			return []any{row.Field("Topic"), row.Field("DelId")}
		}).RunWrite(a.conn); err != nil {
		return err
	}

	// 用户凭据 - 联系信息，如 "email:jdoe@example.com" 或 "tel:+18003287448"：
	// Id: "method:credential" 如 "email:jdoe@example.com"。参见 types.Credential。
	if _, err := rdb.DB(a.dbName).TableCreate("credentials", rdb.TableCreateOpts{PrimaryKey: "Id"}).RunWrite(a.conn); err != nil {
		return err
	}
	// 在 credentials.User 上创建二级索引，以便通过用户 ID 查询凭据。
	if _, err := rdb.DB(a.dbName).Table("credentials").IndexCreate("User").RunWrite(a.conn); err != nil {
		return err
	}

	// 文件上传记录。参见 types.FileDef。
	if _, err := rdb.DB(a.dbName).TableCreate("fileuploads", rdb.TableCreateOpts{PrimaryKey: "Id"}).RunWrite(a.conn); err != nil {
		return err
	}
	// fileuploads.UseCount 上的二级索引，用于批量删除未使用的记录。
	if _, err := rdb.DB(a.dbName).Table("fileuploads").IndexCreate("UseCount").RunWrite(a.conn); err != nil {
		return err
	}

	// 记录当前数据库版本。
	if _, err := rdb.DB(a.dbName).Table("kvmeta").Insert(
		map[string]any{"key": "version", "value": adpVersion}).RunWrite(a.conn); err != nil {
		return err
	}

	return nil
}

// UpgradeDb 将数据库升级到最新版本。
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

	if a.version == 106 || a.version == 107 {
		// 执行从版本 106 或 107 升级到版本 108 的数据库升级。

		// 将默认的 'Auth' 访问模式 JRWPA 替换为 JRWPAS
		filter := map[string]any{"Access": map[string]any{"Auth": t.ModeCP2P}}
		update := map[string]any{"Access": map[string]any{"Auth": t.ModeCAuth}}
		if _, err := rdb.DB(a.dbName).Table("users").Filter(filter).Update(update).RunWrite(a.conn); err != nil {
			return err
		}

		if err := bumpVersion(a, 108); err != nil {
			return err
		}
	}

	if a.version == 108 {
		// 执行从版本 108 升级到版本 109 的数据库升级。

		if err := createSystemTopic(a); err != nil {
			return err
		}

		if err := bumpVersion(a, 109); err != nil {
			return err
		}
	}

	if a.version == 109 {
		// 执行从版本 109 升级到版本 110 的数据库升级。

		// TouchedAt 现在是必填字段，但缺失也可以。
		// 升级版本以保持 RDB 与 MySQL 版本同步。

		if err := bumpVersion(a, 110); err != nil {
			return err
		}
	}

	if a.version == 110 {
		// 执行从版本 110 升级到版本 111 的数据库升级。

		// 用户

		// 将之前未使用的 State 字段重置为 StateOK 值。
		if _, err := rdb.DB(a.dbName).Table("users").
			Update(map[string]any{"State": t.StateOK}).
			RunWrite(a.conn); err != nil {
			return err
		}

		// 为所有已删除的用户（DeletedAt 不为空）添加 StateDeleted 状态。
		if _, err := rdb.DB(a.dbName).Table("users").
			Between(rdb.MinVal, rdb.MaxVal, rdb.BetweenOpts{Index: "DeletedAt"}).
			Update(map[string]any{"State": t.StateDeleted}).
			RunWrite(a.conn); err != nil {
			return err
		}

		// 将 DeletedAt 重命名为 StateAt。仅更新具有已定义 DeletedAt 的行。
		if _, err := rdb.DB(a.dbName).Table("users").
			Between(rdb.MinVal, rdb.MaxVal, rdb.BetweenOpts{Index: "DeletedAt"}).
			Replace(func(row rdb.Term) rdb.Term {
				return row.Without("DeletedAt").
					Merge(map[string]any{"StateAt": row.Field("DeletedAt")})
			}).
			RunWrite(a.conn); err != nil {
			return err
		}

		// 删除二级索引 DeletedAt。
		if _, err := rdb.DB(a.dbName).Table("users").IndexDrop("DeletedAt").RunWrite(a.conn); err != nil {
			return err
		}

		// 在 State 上创建二级索引，用于查找已暂停和软删除的用户。
		if _, err := rdb.DB(a.dbName).Table("users").IndexCreate("State").RunWrite(a.conn); err != nil {
			return err
		}

		//主题

		// 为所有 DeletedAt 不为空的 Topic 添加 StateDeleted 状态。
		if _, err := rdb.DB(a.dbName).Table("topics").
			Filter(rdb.Row.HasFields("DeletedAt")).
			Update(map[string]any{"State": t.StateDeleted}).
			RunWrite(a.conn); err != nil {
			return err
		}

		// 为所有其他 Topic 设置 StateOK。
		if _, err := rdb.DB(a.dbName).Table("topics").
			Filter(rdb.Row.HasFields("State").Not()).
			Update(map[string]any{"State": t.StateOK}).
			RunWrite(a.conn); err != nil {
			return err
		}

		// 将 DeletedAt 重命名为 StateAt。仅更新具有已定义 DeletedAt 的行。
		if _, err := rdb.DB(a.dbName).Table("topics").
			Filter(rdb.Row.HasFields("DeletedAt")).
			Replace(func(row rdb.Term) rdb.Term {
				return row.Without("DeletedAt").
					Merge(map[string]any{"StateAt": row.Field("DeletedAt")})
			}).
			RunWrite(a.conn); err != nil {
			return err
		}

		// 在 State 上创建二级索引，用于查找已暂停和软删除的 Topic。
		if _, err := rdb.DB(a.dbName).Table("topics").IndexCreate("State").RunWrite(a.conn); err != nil {
			return err
		}

		if err := bumpVersion(a, 111); err != nil {
			return err
		}
	}

	if a.version == 111 {
		// 仅升级版本以与 MySQL 保持同步。
		if err := bumpVersion(a, 112); err != nil {
			return err
		}
	}

	if a.version == 112 {
		// 二级索引不能存储 NULL，因此无法创建有用的索引。
		// 仅升级版本。
		if err := bumpVersion(a, 113); err != nil {
			return err
		}
	}

	if a.version < 116 {
		// 版本 114：添加了 Topic.aux 和 fileuploads.etag。
		// 版本 115：添加了 SQL 索引。
		// 版本 116：添加了 Topic.subcnt。

		// 仅升级版本。
		if err := bumpVersion(a, 116); err != nil {
			return err
		}
	}

	if a.version == 116 {
		// RethinkDB 不支持字段 COMMENT；ClientKey 的用途由索引和 Go 模型注释记录。
		if _, err := rdb.DB(a.dbName).Table("messages").IndexCreateFunc("Topic_ClientKey",
			func(row rdb.Term) any {
				return []any{row.Field("Topic"), row.Field("ClientKey")}
			}).RunWrite(a.conn); err != nil {
			return err
		}
		if err := bumpVersion(a, 117); err != nil {
			return err
		}
	}

	if a.version == 117 {
		// 数据库 117→118：创建持久化定时队列表及投递/幂等索引。
		// 创建持久化定时队列；RethinkDB 无原生表 COMMENT。
		if _, err := rdb.DB(a.dbName).TableCreate("scheduledmessages",
			rdb.TableCreateOpts{PrimaryKey: "Id"}).RunWrite(a.conn); err != nil {
			return err
		}
		if _, err := rdb.DB(a.dbName).Table("scheduledmessages").IndexCreate("PublishAt").RunWrite(a.conn); err != nil {
			return err
		}
		if _, err := rdb.DB(a.dbName).Table("scheduledmessages").IndexCreateFunc("Topic_From_ClientId",
			func(row rdb.Term) any {
				return []any{row.Field("Topic"), row.Field("From"), row.Field("ClientId")}
			}).RunWrite(a.conn); err != nil {
			return err
		}
		if err := bumpVersion(a, 118); err != nil {
			return err
		}
	}

	if a.version == 118 {
		// 数据库 118→119：为历史消息回填服务端搜索文本。
		// RethinkDB 不支持字段 COMMENT；SearchText 的用途由 Go 模型注释记录。
		if _, err := rdb.DB(a.dbName).Table("messages").Update(func(row rdb.Term) any {
			content := row.Field("Content").Default("")
			return map[string]any{
				"SearchText": rdb.Branch(
					content.TypeOf().Eq("STRING"),
					content,
					content.Default(map[string]any{}).Field("txt").Default(""),
				),
			}
		}).RunWrite(a.conn); err != nil {
			return err
		}
		if err := bumpVersion(a, 119); err != nil {
			return err
		}
	}

	if a.version == 119 {
		// 数据库 119→120：为 Topic 回填集群 Owner 与 fencing epoch。
		// RethinkDB 不支持字段 COMMENT；字段用途由 types.Topic 的中文注释记录。
		if _, err := rdb.DB(a.dbName).Table("topics").Update(map[string]any{
			"ClusterOwner": "",
			"ClusterEpoch": int64(0),
		}).RunWrite(a.conn); err != nil {
			return err
		}
		if err := bumpVersion(a, 120); err != nil {
			return err
		}
	}

	if a.version == 120 {
		// 数据库 120→121：为官方大群成员游标分页创建 Topic + User 复合索引。
		// RethinkDB 不支持索引 COMMENT；索引用途记录在此处。
		if _, err := rdb.DB(a.dbName).Table("subscriptions").IndexCreateFunc("Topic_User",
			func(row rdb.Term) any {
				return []any{row.Field("Topic"), row.Field("User")}
			}).RunWrite(a.conn); err != nil {
			return err
		}
		if err := bumpVersion(a, 121); err != nil {
			return err
		}
	}

	if a.version == 121 {
		if err := bumpVersion(a, 122); err != nil {
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
	_, err := rdb.DB(a.dbName).Table("topics").Insert(&t.Topic{
		ObjHeader: t.ObjHeader{Id: "sys",
			CreatedAt: now,
			UpdatedAt: now},
		TouchedAt: now,
		Access:    t.DefaultAccess{Auth: t.ModeNone, Anon: t.ModeNone},
		Public:    map[string]any{"fn": "System"},
	}).RunWrite(a.conn)
	return err
}
