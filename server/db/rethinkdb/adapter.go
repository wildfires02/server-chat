//go:build rethinkdb

// Package rethinkdb is a 数据库 adapter for RethinkDB.
package rethinkdb

import (
	"encoding/json"
	"errors"
	"hash/fnv"
	"sort"
	"strconv"
	"strings"
	"time"

	"chat/server/auth"
	"chat/server/db/common"
	"chat/server/logs"
	"chat/server/store"
	t "chat/server/store/types"

	rdb "gopkg.in/rethinkdb/rethinkdb-go.v6"
)

// adapter 保存 RethinkDB 连接数据。
type adapter struct {
	conn   *rdb.Session
	dbName string
	// 最大返回记录数
	maxResults int
	// 最大返回消息记录数
	maxMessageResults int
	version           int
}

const (
	adpVersion  = 116
	adapterName = "rethinkdb"

	defaultHost     = "localhost:28015"
	defaultDatabase = "im"

	defaultMaxResults = 1024
	// 此值受 Session 发送队列上限 (128) 限制。
	defaultMaxMessageResults = 100
)

// 配置字段说明参见 https://godoc.org/github.com/rethinkdb/rethinkdb-go#ConnectOpts
type configType struct {
	Database          string `json:"database,omitempty"`
	Addresses         any    `json:"addresses,omitempty"`
	Username          string `json:"username,omitempty"`
	Password          string `json:"password,omitempty"`
	AuthKey           string `json:"authkey,omitempty"`
	Timeout           int    `json:"timeout,omitempty"`
	WriteTimeout      int    `json:"write_timeout,omitempty"`
	ReadTimeout       int    `json:"read_timeout,omitempty"`
	KeepAlivePeriod   int    `json:"keep_alive_timeout,omitempty"`
	UseJSONNumber     bool   `json:"use_json_number,omitempty"`
	NumRetries        int    `json:"num_retries,omitempty"`
	InitialCap        int    `json:"initial_cap,omitempty"`
	MaxOpen           int    `json:"max_open,omitempty"`
	DiscoverHosts     bool   `json:"discover_hosts,omitempty"`
	HostDecayDuration int    `json:"host_decay_duration,omitempty"`
}

// Open 初始化 RethinkDB Session
func (a *adapter) Open(jsonconfig json.RawMessage) error {
	if a.conn != nil {
		return errors.New("adapter rethinkdb is already connected")
	}

	if len(jsonconfig) < 2 {
		return errors.New("adapter rethinkdb missing config")
	}

	var err error
	var config configType
	if err = json.Unmarshal(jsonconfig, &config); err != nil {
		return errors.New("adapter rethinkdb failed to parse config: " + err.Error())
	}

	var opts rdb.ConnectOpts

	if config.Addresses == nil {
		opts.Address = defaultHost
	} else if host, ok := config.Addresses.(string); ok {
		opts.Address = host
	} else if ihosts, ok := config.Addresses.([]any); ok && len(ihosts) > 0 {
		hosts := make([]string, len(ihosts))
		for i, ih := range ihosts {
			h, ok := ih.(string)
			if !ok || h == "" {
				return errors.New("adapter rethinkdb invalid config.Addresses value")
			}
			hosts[i] = h
		}
		opts.Addresses = hosts
	} else {
		return errors.New("adapter rethinkdb failed to parse config.Addresses")
	}

	if config.Database == "" {
		a.dbName = defaultDatabase
	} else {
		a.dbName = config.Database
	}

	if a.maxResults <= 0 {
		a.maxResults = defaultMaxResults
	}

	if a.maxMessageResults <= 0 {
		a.maxMessageResults = defaultMaxMessageResults
	}

	opts.Database = a.dbName
	opts.Username = config.Username
	opts.Password = config.Password
	opts.AuthKey = config.AuthKey
	opts.Timeout = time.Duration(config.Timeout) * time.Second
	opts.WriteTimeout = time.Duration(config.WriteTimeout) * time.Second
	opts.ReadTimeout = time.Duration(config.ReadTimeout) * time.Second
	opts.KeepAlivePeriod = time.Duration(config.KeepAlivePeriod) * time.Second
	opts.UseJSONNumber = config.UseJSONNumber
	opts.NumRetries = config.NumRetries
	opts.InitialCap = config.InitialCap
	opts.MaxOpen = config.MaxOpen
	opts.DiscoverHosts = config.DiscoverHosts
	opts.HostDecayDuration = time.Duration(config.HostDecayDuration) * time.Second

	a.conn, err = rdb.Connect(opts)
	if err != nil {
		return err
	}

	rdb.SetTags("json")
	a.version = -1

	return nil
}

// Close 关闭底层数据库连接
func (a *adapter) Close() error {
	var err error
	if a.conn != nil {
		// Close 会等待所有未完成的请求完成
		err = a.conn.Close()
		a.conn = nil
		a.version = -1
	}
	return err
}

// IsOpen 如果与数据库的连接已建立则返回 true。
// 不检查连接是否实际存活。
func (a *adapter) IsOpen() bool {
	return a.conn != nil
}

// GetDbVersion 返回当前数据库版本。
func (a *adapter) GetDbVersion() (int, error) {
	if a.version > 0 {
		return a.version, nil
	}

	cursor, err := rdb.DB(a.dbName).Table("kvmeta").Get("version").Field("value").Run(a.conn)
	if err != nil {
		if isMissingDb(err) {
			err = errors.New("Database not initialized")
		}
		return -1, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return -1, errors.New("Database not initialized")
	}

	var vers int
	if err = cursor.One(&vers); err != nil {
		return -1, err
	}

	a.version = vers

	return vers, nil
}

func (a *adapter) updateDbVersion(v int) error {
	a.version = -1
	if _, err := rdb.DB(a.dbName).Table("kvmeta").Get("version").
		Update(map[string]any{"value": v}).RunWrite(a.conn); err != nil {
		return err
	}
	return nil
}

// CheckDbVersion 检查实际数据库版本是否与此适配器的预期版本匹配。
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

// Stats 返回数据库连接统计对象。
func (a *adapter) Stats() any {
	if a.conn == nil {
		return nil
	}

	cursor, err := rdb.DB("rethinkdb").Table("stats").Get([]string{"cluster"}).Field("query_engine").Run(a.conn)
	if err != nil {
		return nil
	}
	defer cursor.Close()

	var stats []any
	if err = cursor.All(&stats); err != nil || len(stats) < 1 {
		return nil
	}

	return stats[0]
}

// GetName 返回适配器向存储注册时使用的名称。
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

// CreateDb 初始化存储。如果 reset 为 true，则先删除数据库，所有数据将丢失。
func (a *adapter) CreateDb(reset bool) error {

	// 如果数据库存在则删除，不存在则忽略错误。
	if reset {
		rdb.DBDrop(a.dbName).RunWrite(a.conn)
	}

	if _, err := rdb.DBCreate(a.dbName).RunWrite(a.conn); err != nil {
		return err
	}

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
		
		// Topic

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

// UserCreate 创建新用户。返回错误，如果是重复用户名则错误为 true，
// 其他错误为 false
func (a *adapter) UserCreate(user *t.User) error {
	_, err := rdb.DB(a.dbName).Table("users").Insert(&user).RunWrite(a.conn)
	return err
}

// AuthAddRecord 添加用户的认证记录
func (a *adapter) AuthAddRecord(uid t.Uid, scheme, unique string, authLvl auth.Level,
	secret []byte, expires time.Time) error {

	_, err := rdb.DB(a.dbName).Table("auth").Insert(
		&common.AuthRecord{
			Unique:  unique,
			UserId:  uid.String(),
			Scheme:  scheme,
			AuthLvl: authLvl,
			Secret:  secret,
			Expires: expires}).RunWrite(a.conn)
	if err != nil {
		if rdb.IsConflictErr(err) {
			return t.ErrDuplicate
		}
		return err
	}
	return nil
}

// AuthDelScheme 删除用户的现有认证方案。
func (a *adapter) AuthDelScheme(uid t.Uid, scheme string) error {
	_, err := rdb.DB(a.dbName).Table("auth").
		GetAllByIndex("userid", uid.String()).
		Filter(map[string]any{"scheme": scheme}).
		Delete().RunWrite(a.conn)
	return err
}

// AuthDelAllRecords 删除用户的所有认证记录
func (a *adapter) AuthDelAllRecords(uid t.Uid) (int, error) {
	res, err := rdb.DB(a.dbName).Table("auth").GetAllByIndex("userid", uid.String()).Delete().RunWrite(a.conn)
	return res.Deleted, err
}

// AuthUpdRecord 更新用户的认证密钥。
func (a *adapter) AuthUpdRecord(uid t.Uid, scheme, unique string, authLvl auth.Level,
	secret []byte, expires time.Time) error {
	// 'unique' 用作主键（RethinkDB 中确保唯一性的唯一方式）。
	// 主键不可变。如果 'unique' 已更改，必须用新记录替换旧记录：
	// 1. 检查 'unique' 是否已更改。
	// 2. 如果没有，通过 'unique' 执行更新
	// 3. 如果是，先插入新记录（可能因 'unique' 重复而失败），然后删除旧记录。

	// 获取旧的 'unique'
	cursor, err := rdb.DB(a.dbName).Table("auth").GetAllByIndex("userid", uid.String()).
		Filter(map[string]any{"scheme": scheme}).
		Pluck("unique").Default(nil).Run(a.conn)
	if err != nil {
		if isNoResults(err) {
			return t.ErrNotFound
		}
		return err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		// 如果记录未找到，不更新
		return t.ErrNotFound
	}

	var record common.AuthRecord
	if err = cursor.One(&record); err != nil {
		return err
	}
	if record.Unique == unique {
		// Unique 未更改
		upd := map[string]any{
			"authLvl": authLvl,
		}
		if len(secret) > 0 {
			upd["secret"] = secret
		}
		if !expires.IsZero() {
			upd["expires"] = expires
		}
		_, err = rdb.DB(a.dbName).Table("auth").Get(unique).Update(upd).RunWrite(a.conn)
	} else {
		// Unique 已更改。插入-删除。
		// 不支持事务 :(
		if len(secret) == 0 {
			secret = record.Secret
		}
		if expires.IsZero() {
			expires = record.Expires
		}
		err = a.AuthAddRecord(uid, scheme, unique, authLvl, secret, expires)
		if err == nil {
			// 这里对错误无能为力。
			rdb.DB(a.dbName).Table("auth").Get(record.Unique).Delete().RunWrite(a.conn)
		}
	}
	return err
}

// AuthGetRecord 通过用户 ID 和方案检索用户的认证记录。
func (a *adapter) AuthGetRecord(uid t.Uid, scheme string) (string, auth.Level, []byte, time.Time, error) {
	// Default() 用于防止 Pluck 返回错误
	cursor, err := rdb.DB(a.dbName).Table("auth").GetAllByIndex("userid", uid.String()).
		Filter(map[string]any{"scheme": scheme}).
		Pluck("unique", "secret", "expires", "authLvl").Default(nil).Run(a.conn)
	if err != nil {
		return "", 0, nil, time.Time{}, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return "", 0, nil, time.Time{}, t.ErrNotFound
	}

	var record struct {
		Unique  string     `json:"unique"`
		AuthLvl auth.Level `json:"authLvl"`
		Secret  []byte     `json:"secret"`
		Expires time.Time  `json:"expires"`
	}

	if err = cursor.One(&record); err != nil {
		return "", 0, nil, time.Time{}, err
	}
	// 转换为 UTC（gorethink 的 bug？）
	record.Expires = record.Expires.UTC()
	return record.Unique, record.AuthLvl, record.Secret, record.Expires, nil
}

// AuthGetUniqueRecord 通过唯一值（如登录名）检索用户的认证记录。
func (a *adapter) AuthGetUniqueRecord(unique string) (t.Uid, auth.Level, []byte, time.Time, error) {
	// Default() 用于防止 Pluck 返回错误
	cursor, err := rdb.DB(a.dbName).Table("auth").Get(unique).Pluck(
		"userid", "secret", "expires", "authLvl").Default(nil).Run(a.conn)
	if err != nil {
		return t.ZeroUid, 0, nil, time.Time{}, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return t.ZeroUid, 0, nil, time.Time{}, nil
	}

	var record struct {
		Userid  string     `json:"userid"`
		AuthLvl auth.Level `json:"authLvl"`
		Secret  []byte     `json:"secret"`
		Expires time.Time  `json:"expires"`
	}

	if err = cursor.One(&record); err != nil {
		return t.ZeroUid, 0, nil, time.Time{}, err
	}

	return t.ParseUid(record.Userid), record.AuthLvl, record.Secret, record.Expires.UTC(), nil
}

// UserGet 通过用户 ID 获取单个用户。如果用户不存在则返回 (nil, nil)
func (a *adapter) UserGet(uid t.Uid) (*t.User, error) {
	cursor, err := rdb.DB(a.dbName).Table("users").GetAll(uid.String()).
		Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not()).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return nil, nil
	}

	var user t.User
	if err = cursor.One(&user); err != nil {
		return nil, err
	}
	return &user, nil
}

// UserGetAll 通过 UID 获取多条用户记录。
func (a *adapter) UserGetAll(ids ...t.Uid) ([]t.User, error) {
	uids := make([]any, len(ids))
	for i, id := range ids {
		uids[i] = id.String()
	}

	users := []t.User{}
	cursor, err := rdb.DB(a.dbName).Table("users").GetAll(uids...).
		Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not()).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var user t.User
	for cursor.Next(&user) {
		// 将时间戳转换为 UTC（gorethink 返回的是 +0000 格式）
		user.CreatedAt = user.CreatedAt.UTC()
		user.UpdatedAt = user.UpdatedAt.UTC()
		if user.StateAt != nil {
			stateAt := user.StateAt.UTC()
			user.StateAt = &stateAt
		}
		users = append(users, user)
	}

	return users, cursor.Err()
}

// UserDelete 删除用户记录。
func (a *adapter) UserDelete(uid t.Uid, hard bool) error {
	// 获取用户拥有的 Topic 名称列表（'grp' 和 'chn'）。
	ownTopics, err := a.topicNamesForUser(rdb.DB(a.dbName).Table("topics").
		GetAllByIndex("Owner", uid.String()).Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not()).
		Field("Id"), true)
	if err != nil {
		logs.Err.Println("UserDelete: cannot get user's own topics:", err)
		return err
	}

	if hard {
		// 用户的设备存储在用户记录中，没有单独的表。

		// 删除用户在所有 Topic 中的订阅。
		if err = a.subsDelForUser(uid, true); err != nil {
			return err
		}

		// 删除用户在所有 Topic 中软删除的消息记录
		// 以及 dellog 条目。
		if err = a.clearUserDellog(uid, nil); err != nil {
			return err
		}

		// 不能删除用户在所有 Topic 中的消息，因为无法通知 Topic 此类删除。
		// 只保留消息标记为由“未找到”用户发送。

		// 删除用户作为所有者的 Topic：

		if len(ownTopics) > 0 {
			// 1. 删除 dellog
			// 2. 减少 fileuploads 的使用计数：Topic 本身和消息。
			// 3. 删除所有消息。
			// 4. 删除订阅。
			if _, err = rdb.DB(a.dbName).Table("topics").GetAll(ownTopics...).ForEach(
				func(topic rdb.Term) rdb.Term {
					return rdb.Expr([]any{
						// 删除 dellog
						rdb.DB(a.dbName).Table("dellog").Between(
							[]any{topic.Field("Id"), rdb.MinVal},
							[]any{topic.Field("Id"), rdb.MaxVal},
							rdb.BetweenOpts{Index: "Topic_DelId"}).Delete(),
						// 减少 Topic 附件的 UseCounter
						rdb.DB(a.dbName).Table("fileuploads").GetAll(topic.Field("Attachments")).
							Update(func(fu rdb.Term) any {
								return map[string]any{"UseCount": fu.Field("UseCount").Default(1).Sub(1)}
							}),
						// 减少消息附件的 UseCounter
						rdb.DB(a.dbName).Table("fileuploads").GetAll(
							rdb.Args(
								rdb.DB(a.dbName).Table("messages").Between(
									[]any{topic.Field("Id"), rdb.MinVal},
									[]any{topic.Field("Id"), rdb.MaxVal},
									rdb.BetweenOpts{Index: "Topic_SeqId"}).
									// 仅获取有附件的消息
									Filter(func(msg rdb.Term) rdb.Term {
										return msg.HasFields("Attachments")
									}).
									// 扁平化数组
									ConcatMap(func(row rdb.Term) any { return row.Field("Attachments") }).
									CoerceTo("array"))).
							Update(func(fu rdb.Term) any {
								return map[string]any{"UseCount": fu.Field("UseCount").Default(1).Sub(1)}
							}),
						// 删除消息
						rdb.DB(a.dbName).Table("messages").Between(
							[]any{topic.Field("Id"), rdb.MinVal},
							[]any{topic.Field("Id"), rdb.MaxVal},
							rdb.BetweenOpts{Index: "Topic_SeqId"}).Delete(),
						// 删除订阅
						rdb.DB(a.dbName).Table("subscriptions").
							GetAllByIndex("Topic", topic.Field("Id")).Delete(),
					})
				}).RunWrite(a.conn); err != nil {
				return err
			}

			// 最后删除 Topic。
			if _, err = rdb.DB(a.dbName).Table("topics").GetAllByIndex("Owner", uid.String()).
				Delete().RunWrite(a.conn); err != nil {
				return err
			}
		}

		// 删除用户的认证记录。
		if _, err = a.AuthDelAllRecords(uid); err != nil {
			return err
		}

		// 删除凭据。
		if err = a.CredDel(uid, "", ""); err != nil && err != t.ErrNotFound {
			return err
		}

		// 必须使用 GetAll 以产生 decFileUseCounter 期望的数组结果。
		q := rdb.DB(a.dbName).Table("users").GetAll(uid.String())

		// 取消关联用户的附件。
		if err = a.decFileUseCounter(q); err != nil {
			return err
		}

		// 最后删除用户。
		_, err = q.Delete().RunWrite(a.conn)
	} else {
		// 禁用用户的订阅。
		if err = a.subsDelForUser(uid, false); err != nil {
			logs.Err.Println("UserDelete: subsDelForUser:", err)
			return err
		}

		now := t.TimeNow()
		disable := map[string]any{
			"UpdatedAt": now,
			"State":     t.StateDeleted,
			"StateAt":   now,
		}
		disableSub := map[string]any{
			"UpdatedAt": now,
			"DeletedAt": now,
		}
		if len(ownTopics) > 0 {
			// 禁用用户作为所有者的 Topic 中的所有订阅。
			if _, err = rdb.DB(a.dbName).Table("subscriptions").
				GetAllByIndex("Topic", ownTopics...).
				Update(disableSub).
				RunWrite(a.conn); err != nil {
				return err
			}

			// 禁用用户作为所有者的 Topic。
			if _, err = rdb.DB(a.dbName).Table("topics").
				GetAll(ownTopics...).
				Update(disable).
				RunWrite(a.conn); err != nil {
				return err
			}
		}

		// 禁用与该用户的 p2p Topic。
		p2pTopics, err := a.p2pTopicsForUser(uid)
		if err != nil {
			logs.Err.Println("UserDelete: p2pTopics:", err)
			return err
		}
		if len(p2pTopics) > 0 {
			// 禁用与该用户的 p2p Topic 中的所有订阅。
			if _, err = rdb.DB(a.dbName).Table("subscriptions").
				GetAllByIndex("Topic", p2pTopics...).
				Update(disableSub).
				RunWrite(a.conn); err != nil {
				return err
			}
			// 禁用与该用户的 p2p Topic。
			if _, err = rdb.DB(a.dbName).Table("topics").
				GetAll(p2pTopics...).
				Update(disable).
				RunWrite(a.conn); err != nil {
				return err
			}
		}

		// 禁用用户（与 Topic 相同的字段）。
		_, err = rdb.DB(a.dbName).Table("users").Get(uid.String()).
			Update(disable).RunWrite(a.conn)
	}
	return err
}

// 删除用户在所有 Topic 中软删除的消息记录。
func (a *adapter) clearUserDellog(uid t.Uid, topics []any) error {
	var err error
	forUser := uid.String()
	if topics == nil {
		// 获取用户有订阅的所有 Topic 列表。
		topics, err = a.topicNamesForUser(rdb.DB(a.dbName).
			Table("subscriptions").
			GetAllByIndex("User", forUser).
			Field("Topic"), false)
		if err != nil {
			return err
		}
	}

	// 无需转换 Channel 名称为 group 名称：
	// Channel 读者不能删除消息。

	// 从消息的软删除列表中移除当前用户
	// （在用户有订阅的所有 Topic 中）。
	_, err = rdb.DB(a.dbName).Table("topics").GetAll(topics...).
		ForEach(func(topic rdb.Term) rdb.Term {
			return rdb.DB(a.dbName).Table("messages").Between(
				[]any{topic.Field("Id"), forUser, rdb.MinVal},
				[]any{topic.Field("Id"), forUser, rdb.MaxVal},
				rdb.BetweenOpts{Index: "Topic_DeletedFor"}).
				Update(map[string]any{
					// 取 DeletedFor 数组，减去所有包含当前用户 ID 的值。
					"DeletedFor": func(msg rdb.Term) rdb.Term {
						return msg.Field("DeletedFor").
							SetDifference(msg.Field("DeletedFor").Filter(map[string]any{"User": forUser}))
					},
				})
		}).RunWrite(a.conn)
	if err != nil {
		return err
	}

	// 删除 dellog 中该用户在所有有订阅的 Topic 中的条目。
	_, err = rdb.DB(a.dbName).Table("topics").GetAll(topics...).
		ForEach(func(topic rdb.Term) rdb.Term {
			return rdb.DB(a.dbName).Table("dellog").
				// 选择给定表的所有日志条目。
				Between(
					[]any{topic.Field("Id"), rdb.MinVal},
					[]any{topic.Field("Id"), rdb.MaxVal},
					rdb.BetweenOpts{Index: "Topic_DelId"}).
				// 仅保留为当前用户软删除的条目以待删除。
				Filter(func(dle rdb.Term) rdb.Term { return dle.Field("DeletedFor").Eq(forUser) }).
				// 删除它们。
				Delete()
		}).RunWrite(a.conn)

	return err
}

// topicNamesForUser 通过查询返回 Topic 名称列表。
func (a *adapter) topicNamesForUser(query rdb.Term, includeChan bool) ([]any, error) {
	cursor, err := query.Run(a.conn)
	if err != nil {
		if isNoResults(err) {
			return nil, nil
		}
		return nil, err
	}
	defer cursor.Close()

	var result []string
	if err = cursor.All(&result); err != nil {
		return nil, err
	}

	var args []any
	for _, name := range result {
		args = append(args, name)
		if includeChan {
			// 为每个 'grp' 名称追加 'chn' Topic 名称。
			if channel := t.GrpToChn(name); channel != "" {
				args = append(args, channel)
			}
		}
	}
	return args, nil
}

func (a *adapter) p2pTopicsForUser(uid t.Uid) ([]any, error) {
	return a.topicNamesForUser(rdb.DB(a.dbName).Table("subscriptions").
		GetAllByIndex("User", uid.String()).
		Field("Topic").
		Filter(rdb.Row.Field("Topic").Match("^p2p")), false)
}

// topicStateForUser 由 UserUpdate 在更新包含状态更改时调用。
func (a *adapter) topicStateForUser(uid t.Uid, now time.Time, update any) error {
	state, ok := update.(t.ObjState)
	if !ok {
		return t.ErrMalformed
	}

	if now.IsZero() {
		now = t.TimeNow()
	}

	// 更改用户作为所有者的所有 Topic 的状态。
	if _, err := rdb.DB(a.dbName).Table("topics").
		GetAllByIndex("Owner", uid.String()).
		Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not()).
		Update(map[string]any{
			"State":   state,
			"StateAt": now,
		}).RunWrite(a.conn); err != nil {
		return err
	}

	// 更改与该用户的 p2p Topic 的状态（p2p Topic 的 owner 为空）
	/*
		r.db('im').table('topics').getAll(
			r.args(
				r.db("im").table("subscriptions").getAll('S8VFqRpXw5M', {index: 'User'})('Topic').coerceTo('array')
			)
		).update(...)
	*/
	if _, err := rdb.DB(a.dbName).Table("topics").
		GetAll(rdb.Args(
			rdb.DB(a.dbName).Table("subscriptions").GetAllByIndex("User", uid.String()).
				Field("Topic").CoerceTo("array"))).
		Filter(rdb.Row.Field("Owner").Eq("").And(rdb.Row.Field("State").Eq(t.StateDeleted).Not())).
		Update(map[string]any{
			"State":   state,
			"StateAt": now,
		}).RunWrite(a.conn); err != nil {
		return err
	}

	// 订阅不需要更新：
	// 已禁用用户的订阅不会被禁用，仍然可以操作。

	return nil
}

// UserUpdate 更新用户对象。
func (a *adapter) UserUpdate(uid t.Uid, update map[string]any) error {
	_, err := rdb.DB(a.dbName).Table("users").Get(uid.String()).Update(update).RunWrite(a.conn)
	if err != nil {
		return err
	}

	if state, ok := update["State"]; ok {
		now, _ := update["StateAt"].(time.Time)
		err = a.topicStateForUser(uid, now, state)
	}

	return err
}

// UserUpdateTags 追加或重置用户的标签
func (a *adapter) UserUpdateTags(uid t.Uid, add, remove, reset []string) ([]string, error) {
	// 与 nil 比较而不是检查零长度：零长度重置是有效的。
	if reset != nil {
		// 用新值替换 Tags
		return reset, a.UserUpdate(uid, map[string]any{"Tags": reset})
	}

	// 变更标签列表。

	newTags := rdb.Row.Field("Tags")
	if len(add) > 0 {
		newTags = newTags.SetUnion(add)
	}
	if len(remove) > 0 {
		newTags = newTags.SetDifference(remove)
	}

	q := rdb.DB(a.dbName).Table("users").Get(uid.String())
	_, err := q.Update(map[string]any{"Tags": newTags}).RunWrite(a.conn)
	if err != nil {
		return nil, err
	}

	// 获取新标签。
	// 使用 Pluck 而不是 Field，因为 https://github.com/rethinkdb/rethinkdb-go/issues/486
	cursor, err := q.Pluck("Tags").Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var tagsField struct{ Tags []string }
	err = cursor.One(&tagsField)
	if err != nil {
		return nil, err
	}
	if len(tagsField.Tags) == 0 {
		tagsField.Tags = nil
	}
	return tagsField.Tags, nil
}

// UserGetByCred 返回给定已验证凭据的用户 ID。
func (a *adapter) UserGetByCred(method, value string) (t.Uid, error) {
	cursor, err := rdb.DB(a.dbName).Table("credentials").Get(method + ":" + value).Field("User").Default(nil).Run(a.conn)
	if err != nil {
		return t.ZeroUid, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return t.ZeroUid, nil
	}

	var userId string
	if err = cursor.One(&userId); err != nil {
		return t.ZeroUid, err
	}

	return t.ParseUid(userId), nil
}

// UserUnreadCount 返回所有具有 R 权限的 Topic 中未读消息的总数。
// 如果读取失败，仍会返回带有原始用户 ID 的计数，
// 但未读计数未定义且错误非 nil。
// UserUnreadCount 不统计 Channel 中的未读消息（尽管应该统计）。
func (a *adapter) UserUnreadCount(ids ...t.Uid) (map[t.Uid]int, error) {
	// 调用期望用户 ID 为纯字符串，如 "356zaYaumiU"。
	uids := make([]any, len(ids))
	counts := make(map[t.Uid]int, len(ids))
	for i, id := range ids {
		uids[i] = id.String()
		// 确保所有原始 uid 始终存在。
		counts[id] = 0
	}

	/*
		Query:
			r.db("im").table("subscriptions").getAll("356zaYaumiU", "k4cvfaq8zCQ", {index: "User"})
			  .eqJoin("Topic", r.db("im").table("topics"), {index: "Id"})
			  .filter(
			    r.not(r.row.hasFields({"left": "DeletedAt"}).or(r.row("right")("State").eq(20)))
			  )
			  .zip()
			  .pluck("User", "ReadSeqId", "ModeWant", "ModeGiven", "SeqId")
			  .filter(r.js('(function(row) {return row.ModeWant&row.ModeGiven&1 > 0;})'))
			  .group("User")
			  .sum(function(x) {return x.getField("SeqId").sub(x.getField("ReadSeqId"));})

		Result:
				[{group: "356zaYaumiU", reduction: 1}, {group: "k4cvfaq8zCQ", reduction: 0}]
	*/
	cursor, err := rdb.DB(a.dbName).Table("subscriptions").GetAllByIndex("User", uids...).
		EqJoin("Topic", rdb.DB(a.dbName).Table("topics"), rdb.EqJoinOpts{Index: "Id"}).
		// left: 订阅; right: Topic。
		Filter(
			rdb.Not(rdb.Row.HasFields(map[string]any{"left": "DeletedAt"}).
				Or(rdb.Row.Field("right").Field("State").Eq(t.StateDeleted)))).
		Zip().
		Pluck("User", "ReadSeqId", "ModeWant", "ModeGiven", "SeqId").
		Filter(rdb.JS("(function(row) {return (row.ModeWant & row.ModeGiven & " + strconv.Itoa(int(t.ModeRead)) + ") > 0;})")).
		Group("User").
		Sum(func(row rdb.Term) rdb.Term { return row.Field("SeqId").Sub(row.Field("ReadSeqId")) }).
		Run(a.conn)
	if err != nil {
		return counts, err
	}
	defer cursor.Close()

	var oneCount struct {
		Group     string
		Reduction int
	}
	for cursor.Next(&oneCount) {
		counts[t.ParseUid(oneCount.Group)] = oneCount.Reduction
	}
	err = cursor.Err()

	return counts, err
}

// UserGetUnvalidated 返回从未登录、没有已验证凭据
// 且自 lastUpdatedBefore 以来未更新过的 uid 列表。
func (a *adapter) UserGetUnvalidated(lastUpdatedBefore time.Time, limit int) ([]t.Uid, error) {
	/*
		Query:
			r.db('im').table('users')
				.filter(r.row('LastSeen').eq(null).and(r.row('UpdatedAt').lt('Mar 31 2022 01:03:38')))
				.eqJoin('Id', r.db('im').table('credentials'), {index: 'User'}).zip()
				.pluck('User', 'Done')
				.group('User')
				.sum(function(row) {return r.branch(row('Done'), 1, 0)})
				.ungroup()
				.filter({reduction: 0})
				.pluck('group').limit(10)

		Result: [{"group": "3W1hPuHjobg"}, {"group": "Fh_skXNRhVg"}, {"group": "NqMZzq0ajWk"}]
	*/
	cursor, err := rdb.DB(a.dbName).Table("users").
		Filter(rdb.Row.Field("LastSeen").Eq(nil).And(rdb.Row.Field("UpdatedAt").Lt(lastUpdatedBefore))).
		EqJoin("Id", rdb.DB(a.dbName).Table("credentials"), rdb.EqJoinOpts{Index: "User"}).Zip().
		Pluck("User", "Done").
		Group("User").
		Sum(func(row rdb.Term) rdb.Term { return rdb.Branch(row.Field("Done"), 1, 0) }).
		Ungroup().
		Filter(rdb.Row.Field("reduction").Eq(0)).
		Pluck("group").
		Limit(limit).
		Run(a.conn)

	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var rec struct {
		Group string
	}

	var uids []t.Uid
	for cursor.Next(&rec) {
		uid := t.ParseUid(rec.Group)
		if !uid.IsZero() {
			uids = append(uids, uid)
		} else {
			return nil, errors.New("bad uid field")
		}
	}

	err = cursor.Err()

	return uids, err
}

// TopicCreate 从模板创建 Topic
func (a *adapter) TopicCreate(topic *t.Topic) error {
	_, err := rdb.DB(a.dbName).Table("topics").Insert(&topic).RunWrite(a.conn)
	return err
}

// TopicCreateP2P 通过两个用户创建 p2p Topic
func (a *adapter) TopicCreateP2P(initiator, invited *t.Subscription) error {
	initiator.Id = initiator.Topic + ":" + initiator.User
	// 不关心发起者是否更改了自己的订阅
	_, err := rdb.DB(a.dbName).Table("subscriptions").Insert(initiator, rdb.InsertOpts{Conflict: "replace"}).
		RunWrite(a.conn)
	if err != nil {
		return err
	}

	// 如果第二个订阅已存在，不覆盖。确保它未被删除。
	invited.Id = invited.Topic + ":" + invited.User
	_, err = rdb.DB(a.dbName).Table("subscriptions").Insert(invited, rdb.InsertOpts{Conflict: "error"}).
		RunWrite(a.conn)
	if err != nil {
		// 这是重复的订阅吗？
		if !rdb.IsConflictErr(err) {
			// 这是真正的数据库错误
			return err
		}
		// 如果存在则恢复第二个订阅：删除 DeletedAt，更新 CreatedAt 和 UpdatedAt，
		// 更新 ModeGiven。
		_, err = rdb.DB(a.dbName).Table("subscriptions").
			Get(invited.Id).Replace(
			rdb.Row.Without("DeletedAt").
				Merge(map[string]any{
					"CreatedAt": invited.CreatedAt,
					"UpdatedAt": invited.UpdatedAt,
					"ModeGiven": invited.ModeGiven})).
			RunWrite(a.conn)
		if err != nil {
			return err
		}
	}

	topic := &t.Topic{ObjHeader: t.ObjHeader{Id: initiator.Topic}}
	topic.ObjHeader.MergeTimes(&initiator.ObjHeader)
	topic.TouchedAt = initiator.GetTouchedAt()
	return a.TopicCreate(topic)
}

// TopicGet 按名称加载单个 Topic（如果存在）。如果 Topic 不存在则返回 (nil, nil)
func (a *adapter) TopicGet(topic string) (*t.Topic, error) {
	// 按名称获取 Topic
	cursor, err := rdb.DB(a.dbName).Table("topics").Get(topic).Run(a.conn)
	if err != nil {
		return nil, err
	}

	var tt = new(t.Topic)
	if err = cursor.One(tt); err != nil {
		if err == rdb.ErrEmptyResult {
			err = nil // Topic 未找到时无错误。
		}
		return nil, err
	}

	// cursor.One 执行时会自动关闭游标。

	if t.GetTopicCat(topic) == t.TopicCatGrp {
		// Topic 已找到，获取订阅数。尝试 Topic 和 Channel 名称。
		if cursor, err = rdb.DB(a.dbName).Table("subscriptions").
			GetAllByIndex("Topic", topic, t.GrpToChn(topic)).
			Filter(rdb.Row.HasFields("DeletedAt").Not()).
			Count().Run(a.conn); err != nil {
			return nil, err
		}
		subCnt := 0
		if err = cursor.One(&subCnt); err != nil {
			return nil, err
		}
		// 无需关闭游标。

		if subCnt != tt.SubCnt {
			// 用正确的订阅数更新 Topic。
			tt.SubCnt = subCnt
			if _, err = rdb.DB(a.dbName).Table("topics").Get(topic).
				Update(map[string]any{"SubCnt": subCnt}).RunWrite(a.conn); err != nil {
				return nil, err
			}
		}
	}
	// RethinkDB Go 驱动错误地将 UTC 时区转换为 +0000
	tt.CreatedAt = tt.CreatedAt.UTC()
	tt.UpdatedAt = tt.UpdatedAt.UTC()
	tt.TouchedAt = tt.TouchedAt.UTC()
	if tt.StateAt != nil {
		stateAt := tt.StateAt.UTC()
		tt.StateAt = &stateAt
	}

	return tt, nil
}

// TopicsForUser 加载用户的联系人列表：p2p 和 grp Topic，不包括 'me' 和 'fnd' 订阅。
// 读取并反规范化 Public 值。
func (a *adapter) TopicsForUser(uid t.Uid, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	// 获取用户的所有订阅，即使最近未修改的。
	// 我们将使用这些订阅来获取可能最近修改过的 Topic 和用户。
	q := rdb.DB(a.dbName).Table("subscriptions").GetAllByIndex("User", uid.String())
	if !keepDeleted {
		// 过滤出已定义 DeletedAt 的行
		q = q.Filter(rdb.Row.HasFields("DeletedAt").Not())
	}

	limit := 0
	ims := time.Time{}
	if opts != nil {
		if opts.Topic != "" {
			q = q.Filter(rdb.Row.Field("Topic").Eq(opts.Topic))
		}

		// 仅在客户端不管理缓存（或冷启动）时应用限制。
		// 否则必须获取所有订阅并与用户/Topic 手动连接。
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
		q = q.Limit(limit)
	}

	cursor, err := q.Run(a.conn)
	if err != nil {
		return nil, err
	}

	// 获取订阅。需要两个查询：用户表（me 和 p2p）和 Topic 表（p2p 和 grp）。
	// 准备单独订阅列表以区分用户 vs Topic
	var sub t.Subscription
	join := make(map[string]t.Subscription) // 保留这些以便与表连接获取 .private 和 .access
	topq := make([]any, 0, 16)
	usrq := make([]any, 0, 16)
	for cursor.Next(&sub) {
		tname := sub.Topic
		sub.User = uid.String()
		tcat := t.GetTopicCat(tname)

		if tcat == t.TopicCatMe || tcat == t.TopicCatFnd {
			// 'me' 或 'fnd' 订阅，跳过。不跳过 'sys'。
			continue
		} else if tcat == t.TopicCatP2P {
			// P2P 订阅，找到另一个用户以获取用户.Public
			uid1, uid2, _ := t.ParseP2P(sub.Topic)
			if uid1 == uid {
				usrq = append(usrq, uid2.String())
				sub.SetWith(uid2.UserId())
			} else {
				usrq = append(usrq, uid1.String())
				sub.SetWith(uid1.UserId())
			}
		} else if tcat == t.TopicCatGrp {
			// 可能将 Channel 名称转换为 Topic 名称。
			tname = t.ChnToGrp(tname)
		}
		// 'slf'、'sys' 订阅无需特殊处理。

		topq = append(topq, tname)
		join[tname] = sub
	}
	err = cursor.Err()
	cursor.Close()

	if err != nil {
		return nil, err
	}

	var subs []t.Subscription
	if len(join) == 0 {
		return subs, nil
	}

	if len(topq) > 0 {
		// 获取 grp 和 p2p Topic
		q = rdb.DB(a.dbName).Table("topics").GetAll(topq...)
		if !keepDeleted {
			q = q.Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not())
		}

		if !ims.IsZero() {
			// 如果提供了缓存时间戳：仅获取更新的条目。
			q = q.Filter(rdb.Row.Field("TouchedAt").Gt(ims))

			if limit > 0 && limit < len(topq) {
				// 获取超过请求限制的数量没有意义。
				q = q.OrderBy("TouchedAt").Limit(limit)
			}
		}

		cursor, err = q.Run(a.conn)
		if err != nil {
			return nil, err
		}

		var top t.Topic
		for cursor.Next(&top) {
			sub = join[top.Id]
			// 检查是否需要调整 sub.UpdatedAt 到更早或更晚的时间。
			// 如果 IMS 非零，top.UpdatedAt 保证在 IMS 之后。
			sub.UpdatedAt = common.SelectLatestTime(sub.UpdatedAt, top.UpdatedAt)
			sub.SetState(top.State)
			sub.SetTouchedAt(top.TouchedAt)
			sub.SetSeqId(top.SeqId)
			if t.GetTopicCat(sub.Topic) == t.TopicCatGrp {
				sub.SetSubCnt(top.SubCnt)
				sub.SetPublic(top.Public)
				sub.SetTrusted(top.Trusted)
			}
			// 放回更新后的订阅值，将在下方继续处理。
			join[top.Id] = sub
		}
		err = cursor.Err()
		cursor.Close()

		if err != nil {
			return nil, err
		}
	}

	// 获取 p2p 用户并连接到 p2p 订阅。
	if len(usrq) > 0 {
		q = rdb.DB(a.dbName).Table("users").GetAll(usrq...)
		if !keepDeleted {
			// 可选地跳过已删除的用户。
			q = q.Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not())
		}

		// 忽略 ims：我们需要所有用户以获取 LastSeen 和 UserAgent。

		cursor, err = q.Run(a.conn)
		if err != nil {
			return nil, err
		}

		var usr2 t.User
		for cursor.Next(&usr2) {
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
		err = cursor.Err()
		cursor.Close()

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

// UsersForTopic 加载订阅给定 Topic 的用户（非 Channel 读者）。
// UsersForTopic 与 SubsForTopic 的区别在于前者加载用户.Public，
// 后者不加载。
func (a *adapter) UsersForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {
	tcat := t.GetTopicCat(topic)

	// 获取 Topic 订阅者
	// 获取所有已订阅用户。用户数量不大
	q := rdb.DB(a.dbName).Table("subscriptions").GetAllByIndex("Topic", topic)
	if !keepDeleted && tcat != t.TopicCatP2P {
		// 过滤出 DeletedAt 不为空的行。
		// P2P Topic 必须加载所有订阅，否则无法交换 Public 值。
		q = q.Filter(rdb.Row.HasFields("DeletedAt").Not())
	}

	limit := a.maxResults
	var oneUser t.Uid
	if opts != nil {
		// 忽略 IfModifiedSince - 必须返回所有条目
		// 未修改的将去除 Public 和 Private。

		if !opts.User.IsZero() {
			if tcat != t.TopicCatP2P {
				q = q.Filter(rdb.Row.Field("User").Eq(opts.User.String()))
			}
			oneUser = opts.User
		}
		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}
	q = q.Limit(limit)

	cursor, err := q.Run(a.conn)
	if err != nil {
		return nil, err
	}

	// 获取订阅
	var sub t.Subscription
	var subs []t.Subscription
	join := make(map[string]t.Subscription)
	usrq := make([]any, 0, 16)
	for cursor.Next(&sub) {
		join[sub.User] = sub
		usrq = append(usrq, sub.User)
	}
	cursor.Close()

	if len(usrq) > 0 {
		subs = make([]t.Subscription, 0, len(usrq))

		// 通过订阅列表获取用户
		cursor, err = rdb.DB(a.dbName).Table("users").GetAll(usrq...).
			Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not()).Run(a.conn)
		if err != nil {
			return nil, err
		}

		var usr t.User
		for cursor.Next(&usr) {
			if sub, ok := join[usr.Id]; ok {
				sub.ObjHeader.MergeTimes(&usr.ObjHeader)
				sub.SetPublic(usr.Public)
				sub.SetTrusted(usr.Trusted)
				sub.SetLastSeenAndUA(usr.LastSeen, usr.UserAgent)
				subs = append(subs, sub)
			}
		}
		cursor.Close()
	}

	if t.GetTopicCat(topic) == t.TopicCatP2P && len(subs) > 0 {
		// 按预期交换 P2P Topic 的 public 值和 lastSeen。
		if len(subs) == 1 {
			// 用户已删除。无能为力。
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

// OwnTopics 加载用户作为所有者的 Topic 名称切片。
func (a *adapter) OwnTopics(uid t.Uid) ([]string, error) {
	cursor, err := rdb.DB(a.dbName).Table("topics").GetAllByIndex("Owner", uid.String()).
		Filter(rdb.Row.Field("State").Eq(t.StateDeleted).Not()).Field("Id").Run(a.conn)
	if err != nil {
		return nil, err
	}
	var names []string
	var name string
	for cursor.Next(&name) {
		names = append(names, name)
	}
	cursor.Close()
	return names, nil
}

// ChannelsForUser 加载用户作为 Channel 读者且启用了通知 (P) 的 Topic 名称切片。
func (a *adapter) ChannelsForUser(uid t.Uid) ([]string, error) {
	cursor, err := rdb.DB(a.dbName).Table("subscriptions").
		GetAllByIndex("User", uid.String()).
		Filter(rdb.Row.HasFields("DeletedAt").Not()).
		Filter(rdb.Row.Field("Topic").Match("^chn")).
		Filter(rdb.JS("(function(row) {return (row.ModeWant & row.ModeGiven & " + strconv.Itoa(int(t.ModePres)) + ") > 0;})")).
		Field("Topic").Run(a.conn)

	if err != nil {
		return nil, err
	}
	var names []string
	var name string
	for cursor.Next(&name) {
		names = append(names, name)
	}
	cursor.Close()
	return names, nil
}

// TopicShare 向 Topic 添加订阅并增加 Topic 的 subcnt。
func (a *adapter) TopicShare(topic string, shares []*t.Subscription) error {
	// 分配 Id。
	for _, sub := range shares {
		sub.Id = sub.Topic + ":" + sub.User
	}

	// 订阅可能已被标记为已删除（DeletedAt != nil）。如果已标记为已删除，
	// 通过清除旧订阅的 DeletedAt 字段并更新时间和 ModeGiven 来取消标记。
	_, err := rdb.DB(a.dbName).Table("subscriptions").
		Insert(shares, rdb.InsertOpts{Conflict: func(id, oldsub, newsub rdb.Term) any {
			return oldsub.Without("DeletedAt").Merge(map[string]any{
				"CreatedAt": newsub.Field("CreatedAt"),
				"UpdatedAt": newsub.Field("UpdatedAt"),
				"ModeGiven": newsub.Field("ModeGiven"),
				"ModeWant":  newsub.Field("ModeWant"),
				"DelId":     0,
				"ReadSeqId": 0,
				"RecvSeqId": 0})
		}}).RunWrite(a.conn)

	if err == nil && topic != "" {
		_, err = rdb.DB(a.dbName).Table("topics").
			Get(topic).
			Update(map[string]any{"SubCnt": rdb.Row.Field("SubCnt").Default(0).Add(len(shares))}).
			RunWrite(a.conn)
	}
	return err
}

// TopicDelete 删除 Topic、订阅、消息。
func (a *adapter) TopicDelete(topic string, isChan, hard bool) error {
	var err error
	if err = a.subsDelForTopic(topic, isChan, hard); err != nil {
		return err
	}

	if hard {
		if err = a.MessageDeleteList(topic, nil); err != nil {
			return err
		}
	}

	// 必须使用 GetAll 以产生 decFileUseCounter 期望的数组结果。
	q := rdb.DB(a.dbName).Table("topics").GetAll(topic)
	if hard {
		if err = a.decFileUseCounter(q); err == nil {
			_, err = q.Delete().RunWrite(a.conn)
		}
	} else {
		now := t.TimeNow()
		_, err = q.Update(map[string]any{
			"UpdatedAt": now,
			"TouchedAt": now,
			"State":     t.StateDeleted,
			"StatedAt":  now,
		}).RunWrite(a.conn)
	}
	return err
}

// TopicUpdateOnMessage 反序列化消息相关值到 Topic。
func (a *adapter) TopicUpdateOnMessage(topic string, msg *t.Message) error {
	update := struct {
		SeqId     int
		TouchedAt time.Time
	}{msg.SeqId, msg.CreatedAt}

	_, err := rdb.DB(a.dbName).Table("topics").Get(topic).
		Update(update, rdb.UpdateOpts{Durability: "soft"}).RunWrite(a.conn)

	return err
}

// TopicUpdateSubCnt 更新 Topic 中反规范化的订阅者计数。
func (a *adapter) TopicUpdateSubCnt(topic string) error {
	cursor, err := rdb.DB(a.dbName).Table("subscriptions").
		GetAllByIndex("Topic", topic, t.GrpToChn(topic)).
		Filter(rdb.Row.HasFields("DeletedAt").Not()).
		Count().Run(a.conn)
	if err != nil {
		return err
	}
	defer cursor.Close()

	subCnt := 0
	if !cursor.IsNil() {
		if err = cursor.One(&subCnt); err != nil {
			return err
		}
	}
	_, err = rdb.DB(a.dbName).Table("topics").
		Get(topic).
		Update(map[string]any{
			"SubCnt": subCnt,
		}).RunWrite(a.conn)
	return err
}

// TopicUpdate 执行通用 Topic 更新。
func (a *adapter) TopicUpdate(topic string, update map[string]any) error {
	if t, u := update["TouchedAt"], update["UpdatedAt"]; t == nil && u != nil {
		update["TouchedAt"] = u
	}
	_, err := rdb.DB(a.dbName).Table("topics").Get(topic).Update(update).RunWrite(a.conn)
	return err
}

// TopicOwnerChange 更改 Topic 的所有者。
func (a *adapter) TopicOwnerChange(topic string, newOwner t.Uid) error {
	_, err := rdb.DB(a.dbName).Table("topics").Get(topic).
		Update(map[string]any{"Owner": newOwner.String()}).RunWrite(a.conn)
	return err
}

// SubscriptionGet 返回用户对 Topic 的订阅
func (a *adapter) SubscriptionGet(topic string, user t.Uid, keepDeleted bool) (*t.Subscription, error) {

	cursor, err := rdb.DB(a.dbName).Table("subscriptions").Get(topic + ":" + user.String()).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return nil, nil
	}

	var sub t.Subscription
	if err = cursor.One(&sub); err != nil {
		return nil, err
	}

	if !keepDeleted && sub.DeletedAt != nil {
		return nil, nil
	}

	return &sub, nil
}

// SubsForUser 加载用户的所有订阅。不加载 Public 或 Private 值，
// 也不加载已删除的订阅。
func (a *adapter) SubsForUser(forUser t.Uid) ([]t.Subscription, error) {
	q := rdb.DB(a.dbName).
		Table("subscriptions").
		GetAllByIndex("User", forUser.String()).
		Filter(rdb.Row.HasFields("DeletedAt").Not()).
		Without("Private")

	cursor, err := q.Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var subs []t.Subscription
	var ss t.Subscription
	for cursor.Next(&ss) {
		subs = append(subs, ss)
	}

	return subs, cursor.Err()
}

// SubsForTopic 获取 Topic 的所有订阅。不加载 Public 值。
func (a *adapter) SubsForTopic(topic string, keepDeleted bool, opts *t.QueryOpt) ([]t.Subscription, error) {

	q := rdb.DB(a.dbName).Table("subscriptions").GetAllByIndex("Topic", topic)
	if !keepDeleted {
		// 过滤出已定义 DeletedAt 的行
		q = q.Filter(rdb.Row.HasFields("DeletedAt").Not())
	}

	limit := a.maxResults
	if opts != nil {
		// 忽略 IfModifiedSince - 必须返回所有条目
		// 未修改的将去除 Public 和 Private。

		if !opts.User.IsZero() {
			q = q.Filter(rdb.Row.Field("User").Eq(opts.User.String()))
		}
		if opts.Limit > 0 && opts.Limit < limit {
			limit = opts.Limit
		}
	}
	q = q.Limit(limit)

	cursor, err := q.Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var subs []t.Subscription
	var ss t.Subscription
	for cursor.Next(&ss) {
		subs = append(subs, ss)
	}

	return subs, cursor.Err()
}

// SubsUpdate 更新单个订阅。
func (a *adapter) SubsUpdate(topic string, user t.Uid, update map[string]any) error {
	q := rdb.DB(a.dbName).Table("subscriptions")
	if !user.IsZero() {
		// 更新单个 Topic 订阅
		q = q.Get(topic + ":" + user.String())
	} else {
		// 更新所有 Topic 订阅
		q = q.GetAllByIndex("Topic", topic)
	}
	_, err := q.Update(update).RunWrite(a.conn)
	return err
}

// SubsDelete 最多将一个订阅标记为已删除。
func (a *adapter) SubsDelete(topic string, user t.Uid) error {
	now := t.TimeNow()
	forUser := user.String()

	// 将订阅标记为已删除。
	res, err := rdb.DB(a.dbName).Table("subscriptions").
		Get(topic + ":" + forUser).Update(map[string]any{
		"UpdatedAt": now,
		"DeletedAt": now,
	}).RunWrite(a.conn)
	if err != nil {
		return err
	}

	if res.Replaced == 0 {
		// 未更新任何内容，无事可做。
		return t.ErrNotFound
	}

	// 减少 Topic 的 SubCnt。
	_, err = rdb.DB(a.dbName).Table("topics").Get(topic).
		Update(map[string]any{"SubCnt": rdb.Row.Field("SubCnt").Default(1).Sub(1)}).
		RunWrite(a.conn)
	if err != nil {
		return err
	}

	if t.IsChannel(topic) {
		// Channel 读者不能删除消息，全部完成。
		return nil
	}

	// 删除已删除消息的记录。

	// 删除当前用户的 dellog 条目。
	resp, err := rdb.DB(a.dbName).Table("dellog").
		// 选择给定表的所有日志条目。
		Between([]any{topic, rdb.MinVal}, []any{topic, rdb.MaxVal},
			rdb.BetweenOpts{Index: "Topic_DelId"}).
		// 仅保留为当前用户软删除的条目。
		Filter(rdb.Row.Field("DeletedFor").Eq(forUser)).
		// 删除它们。
		Delete().
		RunWrite(a.conn)

	if err != nil || resp.Deleted == 0 {
		// 要么是错误，要么是没有删除任何内容。对此错误无能为力。
		// 即使失败也返回 nil。
		return nil
	}

	// 从消息的软删除列表中移除当前用户。
	// 此处可能的错误将被忽略。
	rdb.DB(a.dbName).Table("messages").
		// 选择给定 Topic 中的所有消息。
		Between(
			[]any{topic, forUser, rdb.MinVal},
			[]any{topic, forUser, rdb.MaxVal},
			rdb.BetweenOpts{Index: "Topic_DeletedFor"}).
		// 更新 DeletedFor 字段：
		Update(map[string]any{
			// 取 DeletedFor 数组，减去所有包含当前用户 ID 的值。
			"DeletedFor": rdb.Row.Field("DeletedFor").
				SetDifference(
					rdb.Row.Field("DeletedFor").
						Filter(map[string]any{"User": forUser}))}).
		RunWrite(a.conn)

	return nil
}

// subsDelForTopic 将给定 Topic 的所有订阅标记为已删除。
func (a *adapter) subsDelForTopic(topic string, isChan, hard bool) error {
	var err error

	q := rdb.DB(a.dbName).Table("subscriptions")
	if isChan {
		// 如果 Topic 是 Channel，必须尝试在 grpXXX 和 chnXXX 名称下删除订阅。
		q = q.GetAllByIndex("Topic", topic, t.GrpToChn(topic))
	} else {
		q = q.GetAllByIndex("Topic", topic)
	}
	if hard {
		_, err = q.Delete().RunWrite(a.conn)
	} else {
		now := t.TimeNow()
		_, err = q.Update(map[string]any{
			"UpdatedAt": now,
			"DeletedAt": now,
		}).RunWrite(a.conn)
	}
	return err
}

// subsDelForUser 将给定用户的所有订阅标记为已删除。
func (a *adapter) subsDelForUser(user t.Uid, hard bool) error {
	var err error

	forUser := user.String()

	// 获取用户订阅的所有 Topic。Channel 保留为 Channel。
	topics, err := a.topicNamesForUser(rdb.DB(a.dbName).Table("subscriptions").
		GetAllByIndex("User", forUser).Field("Topic"), false)
	if err != nil {
		logs.Err.Println("subsDelForUser: topicNamesForUser:", err)
		return err
	}

	// 1. 减少 Topic 中的 SubCnt。
	if _, err = rdb.DB(a.dbName).Table("topics").Get(topics...).
		Update(map[string]any{"SubCnt": rdb.Row.Field("SubCnt").
			Default(1).Sub(1)}).
		RunWrite(a.conn); err != nil {
		return err
	}

	err = a.clearUserDellog(user, topics)
	if err != nil {
		logs.Err.Println("subsDelForUser: clearUserDellog:", err)
		return err
	}

	if hard {
		_, err = rdb.DB(a.dbName).Table("subscriptions").GetAllByIndex("User", user.String()).
			Delete().RunWrite(a.conn)
	} else {
		now := t.TimeNow()
		update := map[string]any{
			"UpdatedAt": now,
			"DeletedAt": now,
		}
		_, err = rdb.DB(a.dbName).Table("subscriptions").GetAllByIndex("User", user.String()).
			Update(update).RunWrite(a.conn)
	}

	return err
}

// Find 返回匹配给定标签的用户和 Topic 列表，如 "email:jdoe@example.com" 或 "tel:+18003287448"。
func (a *adapter) Find(caller, promoPrefix string, req [][]string, opt []string, activeOnly bool) ([]t.Subscription, error) {
	index := make(map[string]struct{})
	allReq := t.FlattenDoubleSlice(req)
	var allTags []any
	for _, tag := range append(allReq, opt...) {
		allTags = append(allTags, tag)
		index[tag] = struct{}{}
	}
	// 查询以选择匹配项，其中每个组包含至少一个必需匹配（限制搜索范围为组成员）。
	/*
		r.db('im').
			table('users').
			getAll('basic:alice', 'travel', {index: "Tags"}).
			union(r.db('im').table('topics').getAll('basic:alice', 'travel', {index: "Tags"})).
			pluck('Id', 'Access', 'CreatedAt', 'UpdatedAt', 'UseBt', 'Public', 'Trusted', 'Tags').
			group('Id').
			ungroup().
			map(row => row.getField('reduction').nth(0).merge(
				{matchedCount: row.getField('reduction').
					getField('Tags').
					nth(0).
					setIntersection(['alias:aliassa', 'basic:alice', 'travel']).
					map(tag => r.branch(tag.match('^alias:'), 20, 1)).
					sum()
				})).
			filter(row => row.getField('Tags').setIntersection(['basic:alice', 'travel']).count().ne(0)).
			orderBy(r.desc('matchedCount')).
			limit(20)
	*/

	// 获取标签匹配的用户和 Topic，按匹配数从高到低排序。
	query := rdb.DB(a.dbName).
		Table("users").
		GetAllByIndex("Tags", allTags...).
		Union(rdb.DB(a.dbName).Table("topics").
			GetAllByIndex("Tags", allTags...))
	if activeOnly {
		query = query.Filter(rdb.Row.Field("State").Eq(t.StateOK))
	}
	query = query.Pluck("Id", "Access", "CreatedAt", "UpdatedAt", "UseBt", "SubCnt", "Public", "Trusted", "Tags").
		Group("Id").
		Ungroup().
		Map(func(row rdb.Term) rdb.Term {
			return row.Field("reduction").
				Nth(0).
				Merge(map[string]any{"MatchedTagsCount": row.Field("reduction").
					Field("Tags").
					Nth(0).
					SetIntersection(allTags).
					Map(func(tag rdb.Term) any {
						return rdb.Branch(
							tag.Match("^"+promoPrefix),
							20, // 如果标签匹配 promo 前缀，计为 20。
							1)  // 否则计为 1。
					}).
					Sum()})
		})

	for _, reqDisjunction := range req {
		if len(reqDisjunction) == 0 {
			continue
		}
		var reqTags []any
		for _, tag := range reqDisjunction {
			reqTags = append(reqTags, tag)
		}
		// 过滤出不匹配任何必需标签的对象。
		query = query.Filter(func(row rdb.Term) rdb.Term {
			return row.Field("Tags").SetIntersection(reqTags).Count().Ne(0)
		})
	}
	cursor, err := query.OrderBy(rdb.Desc("MatchedTagsCount")).Limit(a.maxResults).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var topic t.Topic
	var sub t.Subscription
	var subs []t.Subscription
	for cursor.Next(&topic) {
		if uid := t.ParseUid(topic.Id); !uid.IsZero() {
			topic.Id = uid.UserId()
			if topic.Id == caller {
				// 跳过调用者
				continue
			}
		}

		if topic.UseBt {
			sub.Topic = t.GrpToChn(topic.Id)
		} else {
			sub.Topic = topic.Id
		}

		sub.CreatedAt = topic.CreatedAt
		sub.UpdatedAt = topic.UpdatedAt
		sub.SetSubCnt(topic.SubCnt)
		sub.SetPublic(topic.Public)
		sub.SetTrusted(topic.Trusted)
		sub.SetDefaultAccess(topic.Access.Auth, topic.Access.Anon)
		// 表示模式未设置，不是 'N'。
		sub.ModeGiven = t.ModeUnset
		sub.ModeWant = t.ModeUnset
		sub.Private = common.FilterFoundTags(topic.Tags, index)
		subs = append(subs, sub)
	}

	return subs, cursor.Err()
}

// FindOne 返回匹配给定标签的 Topic 或用户。
func (a *adapter) FindOne(tag string) (string, error) {
	query := rdb.DB(a.dbName).
		Table("users").GetAllByIndex("Tags", tag).
		Union(rdb.DB(a.dbName).Table("topics").GetAllByIndex("Tags", tag)).
		Field("Id").
		Limit(1)
	cursor, err := query.Run(a.conn)
	if err != nil {
		return "", err
	}
	defer cursor.Close()

	var found string
	if err = cursor.One(&found); err != nil {
		if err == rdb.ErrEmptyResult {
			return "", nil
		}
		return "", err
	}

	if user := t.ParseUid(found); !user.IsZero() {
		found = user.UserId()
	}

	return found, nil
}

// 消息

// MessageSave 将消息保存到数据库。
func (a *adapter) MessageSave(msg *t.Message) error {
	_, err := rdb.DB(a.dbName).Table("messages").Insert(msg).RunWrite(a.conn)
	return err
}

// MessageGetAll 检索给定用户可用的所有消息。
func (a *adapter) MessageGetAll(topic string, forUser t.Uid, opts *t.QueryOpt) ([]t.Message, error) {

	var limit = a.maxMessageResults
	var lower, upper any

	upper = rdb.MaxVal
	lower = rdb.MinVal

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

	lower = []any{topic, lower}
	upper = []any{topic, upper}

	requester := forUser.String()
	cursor, err := rdb.DB(a.dbName).Table("messages").
		Between(lower, upper, rdb.BetweenOpts{Index: "Topic_SeqId"}).
		// 按索引排序必须在过滤之前
		OrderBy(rdb.OrderByOpts{Index: rdb.Desc("Topic_SeqId")}).
		// 跳过硬删除的消息
		Filter(rdb.Row.HasFields("DelId").Not()).
		// 跳过为当前用户软删除的消息
		Filter(func(row rdb.Term) any {
			return rdb.Not(row.Field("DeletedFor").Default([]any{}).Contains(
				func(df rdb.Term) any {
					return df.Field("User").Eq(requester)
				}))
		}).Limit(limit).Run(a.conn)

	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var msgs []t.Message
	if err = cursor.All(&msgs); err != nil {
		return nil, err
	}

	return msgs, nil
}

// MessageGetDeleted 返回已删除消息的范围。
func (a *adapter) MessageGetDeleted(topic string, forUser t.Uid, opts *t.QueryOpt) ([]t.DelMessage, error) {
	/*
		r.db('im_test')
			.table('dellog')
			.between(
				['p2p9AVDamaNCRbfKzGSh3mE0w', 1],
				['p2p9AVDamaNCRbfKzGSh3mE0w', 10],
				{index: 'Topic_DelId'}
			)
			.orderBy('Topic_DelId')
			.filter(
				row => row.getField('DeletedFor').eq('0QLrX3WPS2o').or(row.getField('DeletedFor').eq(''))
			)
	*/
	var limit = a.maxResults
	var lower, upper any

	upper = rdb.MaxVal
	lower = rdb.MinVal

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

	// 获取删除日志
	cursor, err := rdb.DB(a.dbName).Table("dellog").
		// 选择给定表和 DelId 值在两个限制之间的日志条目。
		// 默认情况下，左边界为闭区间，右边界为开区间。
		Between([]any{topic, lower}, []any{topic, upper},
			rdb.BetweenOpts{Index: "Topic_DelId"}).
		// 按 DelId 从低到高排序
		OrderBy(rdb.OrderByOpts{Index: "Topic_DelId"}).
		// 保留为当前用户软删除的条目和所有硬删除的条目。
		Filter(func(row rdb.Term) any {
			return row.Field("DeletedFor").Eq(forUser.String()).Or(row.Field("DeletedFor").Eq(""))
		}).
		Limit(limit).Run(a.conn)

	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var dmsgs []t.DelMessage
	if err = cursor.All(&dmsgs); err != nil {
		return nil, err
	}

	return dmsgs, nil
}

// messagesHardDelete 删除 Topic 中的所有消息。
func (a *adapter) messagesHardDelete(topic string) error {
	var err error

	// 扣减关联文件附件的使用计数 (decFileUseCounter 在下文执行)

	if _, err = rdb.DB(a.dbName).Table("dellog").Between(
		[]any{topic, rdb.MinVal},
		[]any{topic, rdb.MaxVal},
		rdb.BetweenOpts{Index: "Topic_DelId"}).Delete().RunWrite(a.conn); err != nil {
		return err
	}

	q := rdb.DB(a.dbName).Table("messages").Between(
		[]any{topic, rdb.MinVal},
		[]any{topic, rdb.MaxVal},
		rdb.BetweenOpts{Index: "Topic_SeqId"})

	if err = a.decFileUseCounter(q); err != nil {
		return err
	}

	_, err = q.Delete().RunWrite(a.conn)

	return err
}

func rangeToQuery(delRanges []t.Range, topic string, query rdb.Term) rdb.Term {
	if len(delRanges) > 1 || delRanges[0].Hi <= delRanges[0].Low {
		var indexVals []any
		for _, rng := range delRanges {
			if rng.Hi == 0 {
				indexVals = append(indexVals, []any{topic, rng.Low})
			} else {
				for i := rng.Low; i <= rng.Hi; i++ {
					indexVals = append(indexVals, []any{topic, i})
				}
			}
		}
		query = query.GetAllByIndex("Topic_SeqId", indexVals...)
	} else {
		// 优化单个范围 low..hi 的特殊情况
		query = query.Between(
			[]any{topic, delRanges[0].Low},
			[]any{topic, delRanges[0].Hi},
			rdb.BetweenOpts{Index: "Topic_SeqId", RightBound: "closed"})
	}
	return query
}

// MessageDeleteList 删除给定 Topic 中 seqId 在列表中的消息。
func (a *adapter) MessageDeleteList(topic string, toDel *t.DelMessage) error {
	var err error

	if toDel == nil {
		// 删除所有消息。
		return a.messagesHardDelete(topic)
	}

	// 仅删除部分消息

	delRanges := toDel.SeqIdRanges
	query := rangeToQuery(delRanges, topic, rdb.DB(a.dbName).Table("messages"))
	// 跳过已硬删除的消息。
	query = query.Filter(rdb.Row.HasFields("DelId").Not())
	if toDel.DeletedFor == "" {
		// 硬删除消息需要更新消息表。

		// 要求删除不超过 newerThan 的消息。
		if newerThan := toDel.GetNewerThan(); newerThan != nil {
			query = query.Filter(rdb.Row.Field("CreatedAt").Gt(newerThan))
		}

		query = query.Field("SeqId")

		// 查找数据库中仍存在的实际 ID。
		cursor, err := query.Run(a.conn)
		if err != nil {
			return err
		}
		defer cursor.Close()

		var seqIDs []int
		if err = cursor.All(&seqIDs); err != nil {
			return err
		}

		if len(seqIDs) == 0 {
			// 无需删除。无需创建日志条目。全部完成。
			return nil
		}

		// 重新计算实际要删除的范围。
		sort.Ints(seqIDs)
		delRanges = t.SliceToRanges(seqIDs)

		// 用新范围组成新查询。
		query = rangeToQuery(delRanges, topic, rdb.DB(a.dbName).Table("messages"))

		// 首先减少附件的使用计数。
		if err = a.decFileUseCounter(query); err != nil {
			return err
		}

		// 硬删除单个消息。消息不会被删除，但所有包含个人内容的字段将被移除。
		if _, err = query.Replace(rdb.Row.Without("Head", "From", "Content", "Attachments").Merge(
			map[string]any{
				"DeletedAt": t.TimeNow(), "DelId": toDel.DelId})).
			RunWrite(a.conn); err != nil {
			return err
		}

	} else {
		// 软删除：将 DelId 添加到 DeletedFor。
		_, err = query.
			// 跳过已为当前用户软删除的消息
			Filter(func(row rdb.Term) any {
				return rdb.Not(row.Field("DeletedFor").Default([]any{}).Contains(
					func(df rdb.Term) any {
						return df.Field("User").Eq(toDel.DeletedFor)
					}))
			}).
			Update(map[string]any{"DeletedFor": rdb.Row.Field("DeletedFor").
				Default([]any{}).Append(
				&t.SoftDelete{
					User:  toDel.DeletedFor,
					DelId: toDel.DelId})}).RunWrite(a.conn)
		if err != nil {
			return err
		}
	}

	// 创建日志条目。硬删除和软删除都需要。
	if _, err = rdb.DB(a.dbName).Table("dellog").Insert(toDel).RunWrite(a.conn); err != nil {
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

func deviceHasher(deviceID string) string {
	// 生成自定义密钥作为 [64 位设备 ID 哈希] 以确保密钥长度可预测
	hasher := fnv.New64()
	hasher.Write([]byte(deviceID))
	return strconv.FormatUint(uint64(hasher.Sum64()), 16)
}

// 推送通知的设备管理

// DeviceUpsert 添加或更新用户的设备 FCM 推送令牌。
func (a *adapter) DeviceUpsert(uid t.Uid, def *t.DeviceDef) error {
	hash := deviceHasher(def.DeviceId)
	user := uid.String()

	// 确保设备 ID 的唯一性
	// 查找已使用此设备 ID 的用户，忽略当前用户。
	cursor, err := rdb.DB(a.dbName).Table("users").GetAllByIndex("DeviceIds", def.DeviceId).
		// 我们只关心用户 Id
		Pluck("Id").
		// 确保过滤掉可能合法使用此设备 ID 的当前用户
		Filter(rdb.Not(rdb.Row.Field("Id").Eq(user))).
		// 将对象切片转换为字符串切片
		ConcatMap(func(row rdb.Term) any { return []any{row.Field("Id")} }).
		// 执行
		Run(a.conn)
	if err != nil {
		return err
	}
	defer cursor.Close()

	var others []any
	if err = cursor.All(&others); err != nil {
		return err
	}

	if len(others) > 0 {
		// 删除其他用户的设备 ID。
		_, err = rdb.DB(a.dbName).Table("users").GetAll(others...).Replace(rdb.Row.Without(
			map[string]string{"Devices": hash})).RunWrite(a.conn)
		if err != nil {
			return err
		}
	}

	// 实际为新用户添加/更新 DeviceId
	_, err = rdb.DB(a.dbName).Table("users").Get(user).
		Update(map[string]any{
			"Devices": map[string]*t.DeviceDef{
				hash: def,
			}}).RunWrite(a.conn)
	return err
}

// DeviceGetAll 检索用户的设备列表（推送令牌）。
func (a *adapter) DeviceGetAll(uids ...t.Uid) (map[t.Uid][]t.DeviceDef, int, error) {
	ids := make([]any, len(uids))
	for i, id := range uids {
		ids[i] = id.String()
	}

	// {Id: "userid", Devices: {"hash1": {..def1..}, "hash2": {..def2..}}
	cursor, err := rdb.DB(a.dbName).Table("users").GetAll(ids...).Pluck("Id", "Devices").
		Default(nil).Limit(a.maxResults).Run(a.conn)
	if err != nil {
		return nil, 0, err
	}
	defer cursor.Close()

	var row struct {
		Id      string
		Devices map[string]*t.DeviceDef
	}

	result := make(map[t.Uid][]t.DeviceDef)
	count := 0
	var uid t.Uid
	for cursor.Next(&row) {
		if len(row.Devices) > 0 {
			if err := uid.UnmarshalText([]byte(row.Id)); err != nil {
				continue
			}

			result[uid] = make([]t.DeviceDef, len(row.Devices))
			i := 0
			for _, def := range row.Devices {
				if def != nil {
					result[uid][i] = *def
					i++
					count++
				}
			}
		}
	}

	return result, count, cursor.Err()
}

// DeviceDelete 删除用户的设备（推送令牌）。
func (a *adapter) DeviceDelete(uid t.Uid, deviceID string) error {
	var err error
	q := rdb.DB(a.dbName).Table("users").Get(uid.String())
	if deviceID == "" {
		q = q.Update(map[string]any{"Devices": nil})
	} else {
		q = q.Replace(rdb.Row.Without(map[string]string{"Devices": deviceHasher(deviceID)}))
	}
	_, err = q.RunWrite(a.conn)
	return err
}

// 凭据管理

// CredUpsert 添加或更新验证记录。插入返回 true，更新返回 false。
// 1. 如果凭据已验证：
// 1.1 硬删除未确认的等效记录（如果存在）。
// 1.2 插入新记录。如果重复则报告错误。
// 2. 如果凭据未验证：
// 2.1 检查已验证的等效记录是否存在。如果存在，报告错误。
// 2.2 软删除同一方法的所有未验证记录。
// 2.3 恢复现有凭据。成功则返回。
// 2.4 插入新凭据记录。
func (a *adapter) CredUpsert(cred *t.Credential) (bool, error) {
	var err error
	tableCredentials := rdb.DB(a.dbName).Table("credentials")

	cred.Id = cred.Method + ":" + cred.Value

	if !cred.Done {
		// 检查相同凭据是否已验证。
		cursor, err := tableCredentials.Get(cred.Id).Run(a.conn)
		if err != nil {
			return false, err
		}
		defer cursor.Close()
		if !cursor.IsNil() {
			// 有人已经验证了此凭据。
			return false, t.ErrDuplicate
		}

		// 停用该用户和该方法的所有未验证记录。
		_, err = tableCredentials.GetAllByIndex("User", cred.User).
			Filter(map[string]any{"Method": cred.Method, "Done": false}).Update(
			map[string]any{"DeletedAt": t.TimeNow()}).RunWrite(a.conn)
		if err != nil {
			return false, err
		}

		// 如果凭据未确认，不应阻止其他人尝试验证：
		// 使索引为用户唯一而非全局唯一。
		cred.Id = cred.User + ":" + cred.Id

		// 检查此凭据是否已被用户添加。
		cursor2, err := tableCredentials.Get(cred.Id).Run(a.conn)
		if err != nil {
			return false, err
		}
		defer cursor2.Close()
		if !cursor2.IsNil() {
			_, err = tableCredentials.Get(cred.Id).
				Replace(rdb.Row.Without("DeletedAt").
					Merge(map[string]any{
						"UpdatedAt": cred.UpdatedAt,
						"Resp":      cred.Resp})).RunWrite(a.conn)
			if err != nil {
				return false, err
			}
			// 记录已更新，一切正常。
			return false, nil
		}

	} else {
		// 硬删除可能存在的未验证凭据。
		_, err = tableCredentials.Get(cred.User + ":" + cred.Id).Delete().RunWrite(a.conn)
		if err != nil {
			return false, err
		}
	}

	// 插入新记录。
	_, err = tableCredentials.Insert(cred).RunWrite(a.conn)
	if rdb.IsConflictErr(err) {
		return true, t.ErrDuplicate
	}

	return true, err
}

// CredDel 删除给定方法的凭据。如果方法为空，则删除用户的所有凭据。
func (a *adapter) CredDel(uid t.Uid, method, value string) error {
	q := rdb.DB(a.dbName).Table("credentials").
		GetAllByIndex("User", uid.String())
	if method != "" {
		q = q.Filter(map[string]any{"Method": method})
		if value != "" {
			q = q.Filter(map[string]any{"Value": value})
		}
	}

	if method == "" {
		res, err := q.Delete().RunWrite(a.conn)
		if err == nil {
			if res.Deleted == 0 {
				err = t.ErrNotFound
			}
		}
		return err
	}

	// 硬删除所有已确认的值或没有确认尝试的值。
	res, err := q.Filter(rdb.Or(rdb.Row.Field("Done").Eq(true), rdb.Row.Field("Retries").Eq(0))).Delete().RunWrite(a.conn)
	if err != nil {
		return err
	}
	if res.Deleted > 0 {
		return nil
	}

	// 软删除所有其他值。
	res, err = q.Update(map[string]any{"DeletedAt": t.TimeNow()}).RunWrite(a.conn)
	if err == nil {
		if res.Deleted == 0 {
			err = t.ErrNotFound
		}
	}
	return err
}

// credGetActive 读取当前活跃的未验证凭据
func (a *adapter) credGetActive(uid t.Uid, method string) (*t.Credential, error) {
	// 获取活跃的未确认凭据：
	cursor, err := rdb.DB(a.dbName).Table("credentials").GetAllByIndex("User", uid.String()).
		Filter(rdb.Row.HasFields("DeletedAt").Not()).
		Filter(map[string]any{"Method": method, "Done": false}).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return nil, nil
	}

	var cred t.Credential
	if err = cursor.One(&cred); err != nil {
		return nil, err
	}

	return &cred, nil
}

// CredConfirm 将给定凭据标记为已验证。
func (a *adapter) CredConfirm(uid t.Uid, method string) error {

	cred, err := a.credGetActive(uid, method)
	if err != nil {
		return err
	}

	// RethinkDb 不允许更改主键（userid:method:value -> method:value）
	// 必须删除并用不同的主键重新插入。

	cred.Done = true
	cred.UpdatedAt = t.TimeNow()
	if _, err = a.CredUpsert(cred); err != nil {
		return err
	}

	rdb.DB(a.dbName).
		Table("credentials").
		Get(uid.String() + ":" + cred.Method + ":" + cred.Value).
		Delete(rdb.DeleteOpts{Durability: "soft", ReturnChanges: false}).
		RunWrite(a.conn)

	return nil
}

// CredFail 增加给定凭据的验证失败尝试计数。
func (a *adapter) CredFail(uid t.Uid, method string) error {
	_, err := rdb.DB(a.dbName).Table("credentials").
		GetAllByIndex("User", uid.String()).
		Filter(map[string]any{"Method": method, "Done": false}).
		Filter(rdb.Row.HasFields("DeletedAt").Not()).
		Update(map[string]any{
			"Retries":   rdb.Row.Field("Retries").Default(0).Add(1),
			"UpdatedAt": t.TimeNow(),
		}).RunWrite(a.conn)
	return err
}

// CredGetActive 返回给定方法的当前活跃凭据记录。
func (a *adapter) CredGetActive(uid t.Uid, method string) (*t.Credential, error) {
	return a.credGetActive(uid, method)
}

// CredGetAll 返回用户的给定方法的凭据记录，仅已验证或全部。
func (a *adapter) CredGetAll(uid t.Uid, method string, validatedOnly bool) ([]t.Credential, error) {
	q := rdb.DB(a.dbName).Table("credentials").GetAllByIndex("User", uid.String())
	if method != "" {
		q = q.Filter(map[string]any{"Method": method})
	}
	if validatedOnly {
		q = q.Filter(map[string]any{"Done": true})
	} else {
		q = q.Filter(rdb.Row.HasFields("DeletedAt").Not())
	}

	cursor, err := q.Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return nil, nil
	}

	var credentials []t.Credential
	err = cursor.All(&credentials)
	return credentials, err
}

// 文件上传

// FileStartUpload 初始化文件上传
func (a *adapter) FileStartUpload(fd *t.FileDef) error {
	_, err := rdb.DB(a.dbName).Table("fileuploads").Insert(fd).RunWrite(a.conn)
	return err
}

// FileFinishUpload 将文件上传标记为已完成，无论成功与否
func (a *adapter) FileFinishUpload(fd *t.FileDef, success bool, size int64) (*t.FileDef, error) {
	now := t.TimeNow()
	if success {
		if _, err := rdb.DB(a.dbName).Table("fileuploads").Get(fd.Uid()).
			Update(map[string]any{
				"UpdatedAt": now,
				"Status":    t.UploadCompleted,
				"Size":      size,
				"ETag":      fd.ETag,
				"Location":  fd.Location,
			}).RunWrite(a.conn); err != nil {

			return nil, err
		}
		fd.Status = t.UploadCompleted
		fd.Size = size
	} else {
		if _, err := rdb.DB(a.dbName).Table("fileuploads").Get(fd.Uid()).Delete().RunWrite(a.conn); err != nil {
			return nil, err
		}
		fd.Status = t.UploadFailed
		fd.Size = 0
	}
	fd.UpdatedAt = now

	return fd, nil
}

// FileGet 获取特定文件的记录
func (a *adapter) FileGet(fid string) (*t.FileDef, error) {
	cursor, err := rdb.DB(a.dbName).Table("fileuploads").Get(fid).Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return nil, nil
	}

	var fd t.FileDef
	if err = cursor.One(&fd); err != nil {
		return nil, err
	}

	return &fd, nil

}

// FileLinkAttachments 将给定的 Topic 或消息连接到列表中的文件记录 ID。
func (a *adapter) FileLinkAttachments(topic string, userId, msgId t.Uid, fids []string) error {
	if len(fids) == 0 || (topic == "" && userId.IsZero() && msgId.IsZero()) {
		return t.ErrMalformed
	}

	now := t.TimeNow()
	var err error

	if msgId.IsZero() {
		// 每个用户或 Topic 只允许一个链接。
		fids = fids[0:1]

		// Topic 和用户是可变的。必须先取消关联之前的附件。
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
		var cursor *rdb.Cursor
		cursor, err = rdb.DB(a.dbName).Table(table).Get(linkId).
			Field("Attachments").Default([]string{}).Run(a.conn)
		if err != nil {
			return err
		}
		defer cursor.Close()

		if !cursor.IsNil() {
			var attachments []string
			if err = cursor.One(&attachments); err != nil {
				if err != rdb.ErrEmptyResult {
					return err
				}
				err = nil
			}

			if len(attachments) > 0 {
				// 减少旧附件的使用计数。
				if _, err = rdb.DB(a.dbName).Table("fileuploads").Get(attachments[0]).
					Update(map[string]any{
						"UpdatedAt": now,
						"UseCount":  rdb.Row.Field("UseCount").Default(1).Sub(1),
					}).RunWrite(a.conn); err != nil {
					return err
				}
			}
		}

		_, err = rdb.DB(a.dbName).Table(table).Get(linkId).
			Update(map[string]any{
				"UpdatedAt":   now,
				"Attachments": fids,
			}).RunWrite(a.conn)
		if err != nil {
			return err
		}
	} else {
		// 消息是不可变的。只需保存 ID。
		_, err := rdb.DB(a.dbName).Table("messages").Get(msgId.String()).
			Update(map[string]any{
				"UpdatedAt":   now,
				"Attachments": fids,
			}).RunWrite(a.conn)
		if err != nil {
			return err
		}
	}

	ids := make([]any, len(fids))
	for i, id := range fids {
		ids[i] = id
	}

	_, err = rdb.DB(a.dbName).Table("fileuploads").GetAll(ids...).
		Update(map[string]any{
			"UpdatedAt": now,
			"UseCount":  rdb.Row.Field("UseCount").Default(0).Add(1),
		}).RunWrite(a.conn)

	return err
}

// FileDeleteUnused 删除孤立的文件上传。
func (a *adapter) FileDeleteUnused(olderThan time.Time, limit int) ([]string, error) {
	q := rdb.DB(a.dbName).Table("fileuploads").GetAllByIndex("UseCount", 0)
	if !olderThan.IsZero() {
		q = q.Filter(rdb.Row.Field("UpdatedAt").Lt(olderThan))
	}
	if limit > 0 {
		q = q.Limit(limit)
	}

	cursor, err := q.Field("Location").Run(a.conn)
	if err != nil {
		return nil, err
	}
	defer cursor.Close()

	var locations []string
	var loc string
	for cursor.Next(&loc) {
		locations = append(locations, loc)
	}

	if err = cursor.Err(); err != nil {
		return nil, err
	}

	_, err = q.Delete().RunWrite(a.conn)

	return locations, err
}

// 给定选择查询，减少 'fileuploads' 表中相应的使用计数。
// 'query' 必须返回数组，即 GetAll，而非 Get。
func (a *adapter) decFileUseCounter(query rdb.Term) error {
	/*
		r.db("test").table("one")
			.getAll(
				r.args(r.db("test").table("zero")
					.getAll(
						"07e2c6fe-ac91-49cb-9834-ff34bf50aad1",
			  			"0098a829-6da5-4f7b-8432-32b40de9ab3b",
						"0926e7dd-321a-49cb-adb1-7a705d9d9a78",
						"8e195450-babd-4954-a8fb-0cc414b43156")
					.filter(r.row.hasFields("att"))
					.concatMap(function(row) { return row.getField("att"); })
					.coerceTo("array"))
				)
			.update({useCount: r.row.getField("useCount").default(0).add(1)})
	*/
	_, err := rdb.DB(a.dbName).Table("fileuploads").GetAll(
		rdb.Args(
			query.
				// 仅获取有附件的消息
				Filter(rdb.Row.HasFields("Attachments")).
				// 扁平化数组
				ConcatMap(func(row rdb.Term) any { return row.Field("Attachments") }).
				CoerceTo("array"))).
		// 减少 UseCount。
		Update(map[string]any{"UseCount": rdb.Row.Field("UseCount").Default(1).Sub(1)}).
		RunWrite(a.conn)
	return err
}

// PCacheGet 读取持久缓存条目。
func (a *adapter) PCacheGet(key string) (string, error) {
	cursor, err := rdb.DB(a.dbName).Table("kvmeta").Get(key).Run(a.conn)
	if err != nil {
		return "", err
	}
	defer cursor.Close()

	if cursor.IsNil() {
		return "", t.ErrNotFound
	}

	var result map[string]string
	if err = cursor.One(&result); err != nil {
		return "", err
	}

	return result["value"], nil
}

// PCacheUpsert 创建或更新持久缓存条目。
func (a *adapter) PCacheUpsert(key string, value string, failOnDuplicate bool) error {
	if strings.Contains(key, "^") {
		// 不允许键中包含 ^：它会干扰 Match() 查询。
		return t.ErrMalformed
	}

	doc := map[string]any{
		"key":   key,
		"value": value,
	}

	var action string
	if failOnDuplicate {
		action = "error"
		doc["CreatedAt"] = t.TimeNow()
	} else {
		action = "update"
	}

	_, err := rdb.DB(a.dbName).Table("kvmeta").Insert(doc, rdb.InsertOpts{Conflict: action}).RunWrite(a.conn)
	if rdb.IsConflictErr(err) {
		return t.ErrDuplicate
	}

	return err
}

// PCacheDelete 删除一个持久缓存条目。
func (a *adapter) PCacheDelete(key string) error {
	_, err := rdb.DB(a.dbName).Table("kvmeta").Get(key).Delete().RunWrite(a.conn)
	return err
}

// PCacheExpire 使具有给定键前缀的旧条目过期。
func (a *adapter) PCacheExpire(keyPrefix string, olderThan time.Time) error {
	if keyPrefix == "" {
		return t.ErrMalformed
	}

	_, err := rdb.DB(a.dbName).Table("kvmeta").
		Filter(rdb.Row.Field("CreatedAt").Lt(olderThan).And(rdb.Row.Field("key").Match("^" + keyPrefix))).
		Delete().
		RunWrite(a.conn)

	return err
}

// GetTestDB 返回当前打开的数据库连接。
func (a *adapter) GetTestDB() any {
	return a.conn
}

// 检查错误是否由于无结果。
// 涉及的情况是在非对象值上调用 Field('name')。
func isNoResults(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "perform get_field on a non-object non-sequence")
}

// 检查给定错误是否为 '数据库未找到'。
func isMissingDb(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	// "数据库 `db_name` 不存在"
	return strings.Contains(msg, "Database `") && strings.Contains(msg, "` does not exist")
}

// GetTestAdapter 返回适配器对象。对运行测试有用。
func GetTestAdapter() *adapter {
	return &adapter{}
}

func init() {
	store.RegisterAdapter(&adapter{})
}
