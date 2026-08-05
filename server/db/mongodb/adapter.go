//go:build mongodb

// Package mongodb 是 MongoDB 的数据库适配器。
package mongodb

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"strconv"
	"strings"

	"chat/server/store"

	b "go.mongodb.org/mongo-driver/v2/bson"
	mdb "go.mongodb.org/mongo-driver/v2/mongo"
	mdbopts "go.mongodb.org/mongo-driver/v2/mongo/options"
)

// adapter 保存 MongoDB 连接数据。
type adapter struct {
	// conn 保存连接。
	conn *mdb.Client
	// db 保存数据库。
	db *mdb.Database
	// dbName 保存数据库名称。
	dbName string
	// 最大返回记录数
	maxResults int
	// 最大返回消息记录数
	maxMessageResults int
	// version 保存版本。
	version int
	// ctx 保存ctx。
	ctx context.Context
	// useTransactions 指示是否启用或满足useTransactions。
	useTransactions bool
}

const (
	// adpVersion 指定adp版本。
	adpVersion = 123
	// adapterName 指定adapter名称。
	adapterName = "mongodb"

	// defaultHost 指定默认Host。
	defaultHost = "localhost:27017"
	// defaultDatabase 指定默认Database。
	defaultDatabase = "im"

	// defaultMaxResults 指定默认MaxResults。
	defaultMaxResults = 1024
	// 此值受 Session 发送队列上限 (128) 限制。
	defaultMaxMessageResults = 100

	// defaultAuthMechanism 指定默认认证Mechanism。
	defaultAuthMechanism = "SCRAM-SHA-256"
	// defaultAuthSource 指定默认认证Source。
	defaultAuthSource = "admin"
)

// 客户端选项说明参见 MongoDB Go 驱动文档。
type configType struct {
	// 连接字符串 URI https://www.mongodb.com/docs/manual/reference/connection-string/
	Uri string `json:"uri,omitempty"`
	// Addresses 保存Addresses。
	Addresses any `json:"addresses,omitempty"`
	// ConnectTimeout 保存Connect超时时间。
	ConnectTimeout int `json:"timeout,omitempty"`

	// 独立于 ClientOptions 的选项（自定义选项）：
	Database string `json:"database,omitempty"`
	// ReplicaSet 保存ReplicaSet。
	ReplicaSet string `json:"replica_set,omitempty"`

	// AuthMechanism 保存认证Mechanism。
	AuthMechanism string `json:"auth_mechanism,omitempty"`
	// AuthSource 保存认证Source。
	AuthSource string `json:"auth_source,omitempty"`
	// Username 指示是否启用或满足Username。
	Username string `json:"username,omitempty"`
	// Password 保存密码。
	Password string `json:"password,omitempty"`

	// UseTLS 指示是否启用或满足UseTLS。
	UseTLS bool `json:"tls,omitempty"`
	// TlsCertFile 保存TlsCert文件。
	TlsCertFile string `json:"tls_cert_file,omitempty"`
	// TlsPrivateKey 保存TlsPrivate键。
	TlsPrivateKey string `json:"tls_private_key,omitempty"`
	// InsecureSkipVerify 保存InsecureSkipVerify。
	InsecureSkipVerify bool `json:"tls_skip_verify,omitempty"`

	// 目前唯一支持的版本是 "1"。
	APIVersion mdbopts.ServerAPIVersion `json:"api_version,omitempty"`
}

// maybeStartTransaction 完成maybeStartTransaction所需的内部处理。
func (a *adapter) maybeStartTransaction(sess *mdb.Session) error {
	if a.useTransactions {
		return sess.StartTransaction()
	}
	return nil
}

// maybeCommitTransaction 完成maybeCommitTransaction所需的内部处理。
func (a *adapter) maybeCommitTransaction(ctx context.Context, sess *mdb.Session) error {
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
	a.conn, err = mdb.Connect(&opts)
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

// updateDbVersion 更新数据库版本。
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
	// 绕过 GetDbVersion 的启动期缓存，使用真实查询检测 MongoDB 可用性。
	var result struct {
		Key   string `bson:"_id"`
		Value int
	}
	if err := a.db.Collection("kvmeta").
		FindOne(a.ctx, b.M{"_id": "version"}).
		Decode(&result); err != nil {
		return err
	}
	version := result.Value

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
	if err := a.db.RunCommand(a.ctx, b.D{{Key: "serverStatus", Value: 1}}).Decode(&result); err != nil {
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

// GetTestDB returns a currently open 数据库 connection.
func (a *adapter) GetTestDB() any {
	return a.db
}

// isDbInitialized 判断是否满足数据库Initialized条件。
func (a *adapter) isDbInitialized() bool {
	var result map[string]int

	findOpts := mdbopts.FindOne().SetProjection(b.M{"value": 1, "_id": 0})
	if err := a.db.Collection("kvmeta").FindOne(a.ctx, b.M{"_id": "version"}, findOpts).Decode(&result); err != nil {
		return false
	}
	return true
}

// GetTestAdapter 返回适配器对象。用于运行测试。
func GetTestAdapter() *adapter {
	return &adapter{}
}

// init 注册当前包提供的实现并初始化包级状态。
func init() {
	store.RegisterAdapter(&adapter{})
}

// contains 完成contains所需的内部处理。
func contains(s []string, e string) bool {
	for _, a := range s {
		if a == e {
			return true
		}
	}
	return false
}

// union 完成union所需的内部处理。
func union(userTags, addTags []string) []string {
	for _, tag := range addTags {
		if !contains(userTags, tag) {
			userTags = append(userTags, tag)
		}
	}
	return userTags
}

// diff 完成diff所需的内部处理。
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
		for _, element := range obj {
			result[element.Key] = unmarshalBsonD(element.Value)
		}
		return result
	} else if obj, ok := bsonObj.(b.Binary); ok {
		// 二进制值包含子类型和数据字段，这里只需要数据内容。
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

// copyBsonMap 返回Bson映射的独立副本。
func copyBsonMap(mp b.M) b.M {
	result := b.M{}
	for k, v := range mp {
		result[k] = v
	}
	return result
}

// isDuplicateErr 判断是否满足DuplicateErr条件。
func isDuplicateErr(err error) bool {
	if err == nil {
		return false
	}

	msg := err.Error()
	return strings.Contains(msg, "duplicate key error")
}
