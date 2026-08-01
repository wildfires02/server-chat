//由MockGen生成的代码。 请勿编辑。
//来源：store/store.go

// Package mock_store 为自动生成的 GoMock 持久化存储 Mock 包。
package mock_store

import (
	json "encoding/json"
	reflect "reflect"
	time "time"

	auth "chat/server/auth"
	adapter "chat/server/db"
	media "chat/server/media"
	types "chat/server/store/types"
	validate "chat/server/validate"

	gomock "go.uber.org/mock/gomock"
)

// MockPersistentStorageInterface 是 PersistentStorageInterface 接口的 Mock 实现。
type MockPersistentStorageInterface struct {
	ctrl     *gomock.Controller
	recorder *MockPersistentStorageInterfaceMockRecorder
}

// MockPersistentStorageInterfaceMockRecorder 是 MockPersistentStorageInterface 的 Mock 记录器。
type MockPersistentStorageInterfaceMockRecorder struct {
	mock *MockPersistentStorageInterface
}

// NewMockPersistentStorageInterface创建一个新的模拟实例。
func NewMockPersistentStorageInterface(ctrl *gomock.Controller) *MockPersistentStorageInterface {
	mock := &MockPersistentStorageInterface{ctrl: ctrl}
	mock.recorder = &MockPersistentStorageInterfaceMockRecorder{mock}
	return mock
}

// 期待返回一个对象，允许调用者指示预期使用。
func (m *MockPersistentStorageInterface) EXPECT() *MockPersistentStorageInterfaceMockRecorder {
	return m.recorder
}

// 关闭模拟基础方法。
func (m *MockPersistentStorageInterface) Close() error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Close")
	ret0, _ := ret[0].(error)
	return ret0
}

// 关闭表示“关闭”的预期呼叫。
func (mr *MockPersistentStorageInterfaceMockRecorder) Close() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Close", reflect.TypeOf((*MockPersistentStorageInterface)(nil).Close))
}

// DbStats模拟基础方法。
func (m *MockPersistentStorageInterface) DbStats() func() any {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DbStats")
	ret0, _ := ret[0].(func() any)
	return ret0
}

// DbStats表示DbStats的预期调用。
func (mr *MockPersistentStorageInterfaceMockRecorder) DbStats() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DbStats", reflect.TypeOf((*MockPersistentStorageInterface)(nil).DbStats))
}

// 获取适配器模拟基础方法。
func (m *MockPersistentStorageInterface) GetAdapter() adapter.Adapter {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetAdapter")
	ret0, _ := ret[0].(adapter.Adapter)
	return ret0
}

// GetAdapter表示GetAdapter的预期呼叫。
func (mr *MockPersistentStorageInterfaceMockRecorder) GetAdapter() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetAdapter", reflect.TypeOf((*MockPersistentStorageInterface)(nil).GetAdapter))
}

// GetAdapterName模拟基本方法。
func (m *MockPersistentStorageInterface) GetAdapterName() string {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetAdapterName")
	ret0, _ := ret[0].(string)
	return ret0
}

// GetAdapterName表示GetAdapterName的预期调用。
func (mr *MockPersistentStorageInterfaceMockRecorder) GetAdapterName() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetAdapterName", reflect.TypeOf((*MockPersistentStorageInterface)(nil).GetAdapterName))
}

// 获取AdapterVersion模拟基本方法。
func (m *MockPersistentStorageInterface) GetAdapterVersion() int {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetAdapterVersion")
	ret0, _ := ret[0].(int)
	return ret0
}

// GetAdapterVersion表示GetAdapterVersion的预期调用。
func (mr *MockPersistentStorageInterfaceMockRecorder) GetAdapterVersion() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetAdapterVersion", reflect.TypeOf((*MockPersistentStorageInterface)(nil).GetAdapterVersion))
}

// GetAuthHandler模拟基础方法。
func (m *MockPersistentStorageInterface) GetAuthHandler(name string) auth.AuthHandler {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetAuthHandler", name)
	ret0, _ := ret[0].(auth.AuthHandler)
	return ret0
}

// GetAuthHandler表示GetAuthHandler的预期调用。
func (mr *MockPersistentStorageInterfaceMockRecorder) GetAuthHandler(name interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetAuthHandler", reflect.TypeOf((*MockPersistentStorageInterface)(nil).GetAuthHandler), name)
}

// GetAuthNames模拟基础方法。
func (m *MockPersistentStorageInterface) GetAuthNames() []string {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetAuthNames")
	ret0, _ := ret[0].([]string)
	return ret0
}

// GetAuthNames表示GetAuthNames的预期调用。
func (mr *MockPersistentStorageInterfaceMockRecorder) GetAuthNames() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetAuthNames", reflect.TypeOf((*MockPersistentStorageInterface)(nil).GetAuthNames))
}

// GetDbVersion模拟基础方法。
func (m *MockPersistentStorageInterface) GetDbVersion() int {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetDbVersion")
	ret0, _ := ret[0].(int)
	return ret0
}

// GetDbVersion表示GetDbVersion的预期调用。
func (mr *MockPersistentStorageInterfaceMockRecorder) GetDbVersion() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetDbVersion", reflect.TypeOf((*MockPersistentStorageInterface)(nil).GetDbVersion))
}

// GetLogicalAuthHandler模拟基本方法。
func (m *MockPersistentStorageInterface) GetLogicalAuthHandler(name string) auth.AuthHandler {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetLogicalAuthHandler", name)
	ret0, _ := ret[0].(auth.AuthHandler)
	return ret0
}

// GetLogicalAuthHandler表示GetLogicalAuthHandler的预期调用。
func (mr *MockPersistentStorageInterfaceMockRecorder) GetLogicalAuthHandler(name interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetLogicalAuthHandler", reflect.TypeOf((*MockPersistentStorageInterface)(nil).GetLogicalAuthHandler), name)
}

// GetMediaHandler模拟基础方法。
func (m *MockPersistentStorageInterface) GetMediaHandler() media.Handler {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetMediaHandler")
	ret0, _ := ret[0].(media.Handler)
	return ret0
}

// GetMediaHandler表示GetMediaHandler的预期调用。
func (mr *MockPersistentStorageInterfaceMockRecorder) GetMediaHandler() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetMediaHandler", reflect.TypeOf((*MockPersistentStorageInterface)(nil).GetMediaHandler))
}

// GetUid模拟基础方法。
func (m *MockPersistentStorageInterface) GetUid() types.Uid {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetUid")
	ret0, _ := ret[0].(types.Uid)
	return ret0
}

// GetUid表示GetUid的预期调用。
func (mr *MockPersistentStorageInterfaceMockRecorder) GetUid() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetUid", reflect.TypeOf((*MockPersistentStorageInterface)(nil).GetUid))
}

// GetUidString模拟基础方法。
func (m *MockPersistentStorageInterface) GetUidString() string {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetUidString")
	ret0, _ := ret[0].(string)
	return ret0
}

// GetUidString表示GetUidString的预期调用。
func (mr *MockPersistentStorageInterfaceMockRecorder) GetUidString() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetUidString", reflect.TypeOf((*MockPersistentStorageInterface)(nil).GetUidString))
}

// GetValidator模拟基本方法。
func (m *MockPersistentStorageInterface) GetValidator(name string) validate.Validator {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetValidator", name)
	ret0, _ := ret[0].(validate.Validator)
	return ret0
}

// GetValidator表示GetValidator的预期调用。
func (mr *MockPersistentStorageInterfaceMockRecorder) GetValidator(name interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetValidator", reflect.TypeOf((*MockPersistentStorageInterface)(nil).GetValidator), name)
}

// InitDb模拟基础方法。
func (m *MockPersistentStorageInterface) InitDb(jsonconf json.RawMessage, reset bool) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "InitDb", jsonconf, reset)
	ret0, _ := ret[0].(error)
	return ret0
}

// InitDb表示InitDb的预期调用。
func (mr *MockPersistentStorageInterfaceMockRecorder) InitDb(jsonconf, reset interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "InitDb", reflect.TypeOf((*MockPersistentStorageInterface)(nil).InitDb), jsonconf, reset)
}

// IsOpen模拟基础方法。
func (m *MockPersistentStorageInterface) IsOpen() bool {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "IsOpen")
	ret0, _ := ret[0].(bool)
	return ret0
}

// IsOpen表示IsOpen的预期调用。
func (mr *MockPersistentStorageInterfaceMockRecorder) IsOpen() *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "IsOpen", reflect.TypeOf((*MockPersistentStorageInterface)(nil).IsOpen))
}

// 打开模拟基础方法。
func (m *MockPersistentStorageInterface) Open(workerId int, jsonconf json.RawMessage) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Open", workerId, jsonconf)
	ret0, _ := ret[0].(error)
	return ret0
}

// 开放表示开放的预期呼叫。
func (mr *MockPersistentStorageInterfaceMockRecorder) Open(workerId, jsonconf interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Open", reflect.TypeOf((*MockPersistentStorageInterface)(nil).Open), workerId, jsonconf)
}

// 升级Db模拟基础方法。
func (m *MockPersistentStorageInterface) UpgradeDb(jsonconf json.RawMessage) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "UpgradeDb", jsonconf)
	ret0, _ := ret[0].(error)
	return ret0
}

// UpgradeDb表示UpgradeDb的预期呼叫。
func (mr *MockPersistentStorageInterfaceMockRecorder) UpgradeDb(jsonconf interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "UpgradeDb", reflect.TypeOf((*MockPersistentStorageInterface)(nil).UpgradeDb), jsonconf)
}

// 使用MediaHandler模拟基本方法。
func (m *MockPersistentStorageInterface) UseMediaHandler(name, config string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "UseMediaHandler", name, config)
	ret0, _ := ret[0].(error)
	return ret0
}

// UseMediaHandler表示UseMediaHandler的预期调用。
func (mr *MockPersistentStorageInterfaceMockRecorder) UseMediaHandler(name, config interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "UseMediaHandler", reflect.TypeOf((*MockPersistentStorageInterface)(nil).UseMediaHandler), name, config)
}

// MockUsersPersistenceInterface是UsersPersistenceInterface接口的模拟。
type MockUsersPersistenceInterface struct {
	ctrl     *gomock.Controller
	recorder *MockUsersPersistenceInterfaceMockRecorder
}

// MockUsersPersistenceInterfaceMockRecorder是MockUsersPersistenceInterface的模拟录音机。
type MockUsersPersistenceInterfaceMockRecorder struct {
	mock *MockUsersPersistenceInterface
}

// NewMockUsersPersistenceInterface创建一个新的模拟实例。
func NewMockUsersPersistenceInterface(ctrl *gomock.Controller) *MockUsersPersistenceInterface {
	mock := &MockUsersPersistenceInterface{ctrl: ctrl}
	mock.recorder = &MockUsersPersistenceInterfaceMockRecorder{mock}
	return mock
}

// 期待返回一个对象，允许调用者指示预期使用。
func (m *MockUsersPersistenceInterface) EXPECT() *MockUsersPersistenceInterfaceMockRecorder {
	return m.recorder
}

// AddAuthRecord模拟基础方法。
func (m *MockUsersPersistenceInterface) AddAuthRecord(uid types.Uid, authLvl auth.Level, scheme, unique string, secret []byte, expires time.Time) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "AddAuthRecord", uid, authLvl, scheme, unique, secret, expires)
	ret0, _ := ret[0].(error)
	return ret0
}

// AddAuthRecord表示AddAuthRecord的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) AddAuthRecord(uid, authLvl, scheme, unique, secret, expires interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "AddAuthRecord", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).AddAuthRecord), uid, authLvl, scheme, unique, secret, expires)
}

// ConfirmCred模拟基础方法。
func (m *MockUsersPersistenceInterface) ConfirmCred(id types.Uid, method string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "ConfirmCred", id, method)
	ret0, _ := ret[0].(error)
	return ret0
}

// ConfirmCred 表示 ConfirmCred 的预期呼叫。
func (mr *MockUsersPersistenceInterfaceMockRecorder) ConfirmCred(id, method interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "ConfirmCred", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).ConfirmCred), id, method)
}

// 创建模拟基础方法。
func (m *MockUsersPersistenceInterface) Create(user *types.User, private any) (*types.User, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Create", user, private)
	ret0, _ := ret[0].(*types.User)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// 创建表示创建的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) Create(user, private interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Create", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).Create), user, private)
}

// DelAuthRecords模拟基本方法。
func (m *MockUsersPersistenceInterface) DelAuthRecords(uid types.Uid, scheme string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DelAuthRecords", uid, scheme)
	ret0, _ := ret[0].(error)
	return ret0
}

// DelAuthRecords表示DelAuthRecords的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) DelAuthRecords(uid, scheme interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DelAuthRecords", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).DelAuthRecords), uid, scheme)
}

// DelCred模拟基础方法。
func (m *MockUsersPersistenceInterface) DelCred(id types.Uid, method, value string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DelCred", id, method, value)
	ret0, _ := ret[0].(error)
	return ret0
}

// DelCred表示DelCred的预期呼叫。
func (mr *MockUsersPersistenceInterfaceMockRecorder) DelCred(id, method, value interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DelCred", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).DelCred), id, method, value)
}

// 删除模拟基础方法。
func (m *MockUsersPersistenceInterface) Delete(id types.Uid, hard bool) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Delete", id, hard)
	ret0, _ := ret[0].(error)
	return ret0
}

// 删除表示预期的删除调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) Delete(id, hard interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Delete", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).Delete), id, hard)
}

// FailCred模拟基础方法。
func (m *MockUsersPersistenceInterface) FailCred(id types.Uid, method string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "FailCred", id, method)
	ret0, _ := ret[0].(error)
	return ret0
}

// FailCred表示FailCred的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) FailCred(id, method interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "FailCred", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).FailCred), id, method)
}

// FindOne模拟基本方法。
func (m *MockUsersPersistenceInterface) FindOne(tag string) (string, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "FindOne", tag)
	ret0, _ := ret[0].(string)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// FindOne表示FindOne的预期呼叫。
func (mr *MockUsersPersistenceInterfaceMockRecorder) FindOne(tag interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "FindOne", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).FindOne), tag)
}

// FindSubs模仿基本方法。
func (m *MockUsersPersistenceInterface) FindSubs(caller types.Uid, prefPrefix string, required [][]string, optional []string, activeOnly bool) ([]types.Subscription, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "FindSubs", caller, prefPrefix, required, optional, activeOnly)
	ret0, _ := ret[0].([]types.Subscription)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// FindSubs表示FindSubs的预期呼叫。
func (mr *MockUsersPersistenceInterfaceMockRecorder) FindSubs(caller, prefPrefix, required, optional, activeOnly interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "FindSubs", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).FindSubs), caller, prefPrefix, required, optional, activeOnly)
}

// 搜索模拟基础方法。
func (m *MockUsersPersistenceInterface) Search(caller types.Uid, query *types.PeerSearchQuery) ([]types.Subscription, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Search", caller, query)
	ret0, _ := ret[0].([]types.Subscription)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// 搜索表示“搜索”的预期呼叫。
func (mr *MockUsersPersistenceInterfaceMockRecorder) Search(caller, query interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Search", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).Search), caller, query)
}

// 获取模拟基础方法。
func (m *MockUsersPersistenceInterface) Get(uid types.Uid) (*types.User, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", uid)
	ret0, _ := ret[0].(*types.User)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get表示Get的预期呼叫。
func (mr *MockUsersPersistenceInterfaceMockRecorder) Get(uid interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).Get), uid)
}

// GetActiveCred模拟基础方法。
func (m *MockUsersPersistenceInterface) GetActiveCred(id types.Uid, method string) (*types.Credential, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetActiveCred", id, method)
	ret0, _ := ret[0].(*types.Credential)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetActiveCred表示GetActiveCred的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) GetActiveCred(id, method interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetActiveCred", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).GetActiveCred), id, method)
}

// 获取所有模拟基础方法。
func (m *MockUsersPersistenceInterface) GetAll(uid ...types.Uid) ([]types.User, error) {
	m.ctrl.T.Helper()
	varargs := []interface{}{}
	for _, a := range uid {
		varargs = append(varargs, a)
	}
	ret := m.ctrl.Call(m, "GetAll", varargs...)
	ret0, _ := ret[0].([]types.User)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetAll表示GetAll的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) GetAll(uid ...interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetAll", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).GetAll), uid...)
}

// GetAllCreds模拟基础方法。
func (m *MockUsersPersistenceInterface) GetAllCreds(id types.Uid, method string, validatedOnly bool) ([]types.Credential, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetAllCreds", id, method, validatedOnly)
	ret0, _ := ret[0].([]types.Credential)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetAllCreds表示GetAllCreds的预期呼叫。
func (mr *MockUsersPersistenceInterfaceMockRecorder) GetAllCreds(id, method, validatedOnly interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetAllCreds", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).GetAllCreds), id, method, validatedOnly)
}

// GetAuthRecord模拟基础方法。
func (m *MockUsersPersistenceInterface) GetAuthRecord(user types.Uid, scheme string) (string, auth.Level, []byte, time.Time, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetAuthRecord", user, scheme)
	ret0, _ := ret[0].(string)
	ret1, _ := ret[1].(auth.Level)
	ret2, _ := ret[2].([]byte)
	ret3, _ := ret[3].(time.Time)
	ret4, _ := ret[4].(error)
	return ret0, ret1, ret2, ret3, ret4
}

// GetAuthRecord表示GetAuthRecord的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) GetAuthRecord(user, scheme interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetAuthRecord", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).GetAuthRecord), user, scheme)
}

// GetAuthUniqueRecord模拟基础方法。
func (m *MockUsersPersistenceInterface) GetAuthUniqueRecord(scheme, unique string) (types.Uid, auth.Level, []byte, time.Time, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetAuthUniqueRecord", scheme, unique)
	ret0, _ := ret[0].(types.Uid)
	ret1, _ := ret[1].(auth.Level)
	ret2, _ := ret[2].([]byte)
	ret3, _ := ret[3].(time.Time)
	ret4, _ := ret[4].(error)
	return ret0, ret1, ret2, ret3, ret4
}

// GetAuthUniqueRecord表示GetAuthUniqueRecord的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) GetAuthUniqueRecord(scheme, unique interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetAuthUniqueRecord", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).GetAuthUniqueRecord), scheme, unique)
}

// GetByCred模拟基础方法。
func (m *MockUsersPersistenceInterface) GetByCred(method, value string) (types.Uid, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetByCred", method, value)
	ret0, _ := ret[0].(types.Uid)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetByCred表示GetByCred的预期呼叫。
func (mr *MockUsersPersistenceInterfaceMockRecorder) GetByCred(method, value interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetByCred", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).GetByCred), method, value)
}

// GetChannels模拟基础方法。
func (m *MockUsersPersistenceInterface) GetChannels(id types.Uid) ([]string, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetChannels", id)
	ret0, _ := ret[0].([]string)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetChannels表示GetChannels的预期呼叫。
func (mr *MockUsersPersistenceInterfaceMockRecorder) GetChannels(id interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetChannels", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).GetChannels), id)
}

// GetOwnTopics模拟基础方法。
func (m *MockUsersPersistenceInterface) GetOwnTopics(id types.Uid) ([]string, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetOwnTopics", id)
	ret0, _ := ret[0].([]string)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetOwnTopics表示GetOwnTopics的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) GetOwnTopics(id interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetOwnTopics", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).GetOwnTopics), id)
}

// GetSubs模拟基础方法。
func (m *MockUsersPersistenceInterface) GetSubs(id types.Uid) ([]types.Subscription, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetSubs", id)
	ret0, _ := ret[0].([]types.Subscription)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetSubs表示GetSubs的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) GetSubs(id interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetSubs", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).GetSubs), id)
}

// GetTopics模拟基础方法。
func (m *MockUsersPersistenceInterface) GetTopics(id types.Uid, opts *types.QueryOpt) ([]types.Subscription, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetTopics", id, opts)
	ret0, _ := ret[0].([]types.Subscription)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetTopics表示GetTopics的预期呼叫。
func (mr *MockUsersPersistenceInterfaceMockRecorder) GetTopics(id, opts interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetTopics", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).GetTopics), id, opts)
}

// 获取主题任何模拟基本方法。
func (m *MockUsersPersistenceInterface) GetTopicsAny(id types.Uid, opts *types.QueryOpt) ([]types.Subscription, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetTopicsAny", id, opts)
	ret0, _ := ret[0].([]types.Subscription)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetTopicsAny表示GetTopicsAny的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) GetTopicsAny(id, opts interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetTopicsAny", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).GetTopicsAny), id, opts)
}

// GetUnreadCount模拟基础方法。
func (m *MockUsersPersistenceInterface) GetUnreadCount(ids ...types.Uid) (map[types.Uid]int, error) {
	m.ctrl.T.Helper()
	varargs := []interface{}{}
	for _, a := range ids {
		varargs = append(varargs, a)
	}
	ret := m.ctrl.Call(m, "GetUnreadCount", varargs...)
	ret0, _ := ret[0].(map[types.Uid]int)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetUnreadCount表示GetUnreadCount的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) GetUnreadCount(ids ...interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetUnreadCount", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).GetUnreadCount), ids...)
}

// 获取未验证的模拟基本方法。
func (m *MockUsersPersistenceInterface) GetUnvalidated(lastUpdatedBefore time.Time, limit int) ([]types.Uid, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetUnvalidated", lastUpdatedBefore, limit)
	ret0, _ := ret[0].([]types.Uid)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetUnvalidated表示GetUnvalidated的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) GetUnvalidated(lastUpdatedBefore, limit interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetUnvalidated", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).GetUnvalidated), lastUpdatedBefore, limit)
}

// 更新模拟基础方法。
func (m *MockUsersPersistenceInterface) Update(uid types.Uid, update map[string]any) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Update", uid, update)
	ret0, _ := ret[0].(error)
	return ret0
}

// 更新表示预计的更新呼叫。
func (mr *MockUsersPersistenceInterfaceMockRecorder) Update(uid, update interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Update", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).Update), uid, update)
}

// 更新AuthRecord模拟基础方法。
func (m *MockUsersPersistenceInterface) UpdateAuthRecord(uid types.Uid, authLvl auth.Level, scheme, unique string, secret []byte, expires time.Time) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "UpdateAuthRecord", uid, authLvl, scheme, unique, secret, expires)
	ret0, _ := ret[0].(error)
	return ret0
}

// UpdateAuthRecord表示UpdateAuthRecord的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) UpdateAuthRecord(uid, authLvl, scheme, unique, secret, expires interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "UpdateAuthRecord", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).UpdateAuthRecord), uid, authLvl, scheme, unique, secret, expires)
}

// 更新LastSeen模拟基础方法。
func (m *MockUsersPersistenceInterface) UpdateLastSeen(uid types.Uid, userAgent string, when time.Time) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "UpdateLastSeen", uid, userAgent, when)
	ret0, _ := ret[0].(error)
	return ret0
}

// UpdateLastSeen 表示 UpdateLastSeen 的預期呼叫。
func (mr *MockUsersPersistenceInterfaceMockRecorder) UpdateLastSeen(uid, userAgent, when interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "UpdateLastSeen", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).UpdateLastSeen), uid, userAgent, when)
}

// 更新状态模拟基础方法。
func (m *MockUsersPersistenceInterface) UpdateState(uid types.Uid, state types.ObjState) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "UpdateState", uid, state)
	ret0, _ := ret[0].(error)
	return ret0
}

// UpdateState表示UpdateState的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) UpdateState(uid, state interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "UpdateState", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).UpdateState), uid, state)
}

// 更新标签模拟基础方法。
func (m *MockUsersPersistenceInterface) UpdateTags(uid types.Uid, add, remove, reset []string) ([]string, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "UpdateTags", uid, add, remove, reset)
	ret0, _ := ret[0].([]string)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// UpdateTags表示UpdateTags的预期调用。
func (mr *MockUsersPersistenceInterfaceMockRecorder) UpdateTags(uid, add, remove, reset interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "UpdateTags", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).UpdateTags), uid, add, remove, reset)
}

// UpsertCred模拟基础方法。
func (m *MockUsersPersistenceInterface) UpsertCred(cred *types.Credential) (bool, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "UpsertCred", cred)
	ret0, _ := ret[0].(bool)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// UpsertCred表示UpsertCred的预期呼叫。
func (mr *MockUsersPersistenceInterfaceMockRecorder) UpsertCred(cred interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "UpsertCred", reflect.TypeOf((*MockUsersPersistenceInterface)(nil).UpsertCred), cred)
}

// MockTopicsPersistenceInterface是TopicsPersistenceInterface接口的模拟。
type MockTopicsPersistenceInterface struct {
	ctrl     *gomock.Controller
	recorder *MockTopicsPersistenceInterfaceMockRecorder
}

// MockTopicsPersistenceInterfaceMockRecorder是MockTopicsPersistenceInterface的模拟录音机。
type MockTopicsPersistenceInterfaceMockRecorder struct {
	mock *MockTopicsPersistenceInterface
}

// NewMockTopicsPersistenceInterface创建了新的模拟实例。
func NewMockTopicsPersistenceInterface(ctrl *gomock.Controller) *MockTopicsPersistenceInterface {
	mock := &MockTopicsPersistenceInterface{ctrl: ctrl}
	mock.recorder = &MockTopicsPersistenceInterfaceMockRecorder{mock}
	return mock
}

// 期待返回一个对象，允许调用者指示预期使用。
func (m *MockTopicsPersistenceInterface) EXPECT() *MockTopicsPersistenceInterfaceMockRecorder {
	return m.recorder
}

// 创建模拟基础方法。
func (m *MockTopicsPersistenceInterface) Create(topic *types.Topic, owner types.Uid, private any) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Create", topic, owner, private)
	ret0, _ := ret[0].(error)
	return ret0
}

// 创建表示创建的预期调用。
func (mr *MockTopicsPersistenceInterfaceMockRecorder) Create(topic, owner, private interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Create", reflect.TypeOf((*MockTopicsPersistenceInterface)(nil).Create), topic, owner, private)
}

// 创建P2P模拟基本方法。
func (m *MockTopicsPersistenceInterface) CreateP2P(initiator, invited *types.Subscription) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CreateP2P", initiator, invited)
	ret0, _ := ret[0].(error)
	return ret0
}

// CreateP2P表示CreateP2P的预期调用。
func (mr *MockTopicsPersistenceInterfaceMockRecorder) CreateP2P(initiator, invited interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CreateP2P", reflect.TypeOf((*MockTopicsPersistenceInterface)(nil).CreateP2P), initiator, invited)
}

// 删除模拟基础方法。
func (m *MockTopicsPersistenceInterface) Delete(topic string, isChan, hard bool) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Delete", topic, isChan, hard)
	ret0, _ := ret[0].(error)
	return ret0
}

// 删除表示预期的删除调用。
func (mr *MockTopicsPersistenceInterfaceMockRecorder) Delete(topic, isChan, hard interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Delete", reflect.TypeOf((*MockTopicsPersistenceInterface)(nil).Delete), topic, isChan, hard)
}

// 获取模拟基础方法。
func (m *MockTopicsPersistenceInterface) Get(topic string) (*types.Topic, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", topic)
	ret0, _ := ret[0].(*types.Topic)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get表示Get的预期呼叫。
func (mr *MockTopicsPersistenceInterfaceMockRecorder) Get(topic interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*MockTopicsPersistenceInterface)(nil).Get), topic)
}

// GetSubs模拟基础方法。
func (m *MockTopicsPersistenceInterface) GetSubs(topic string, opts *types.QueryOpt) ([]types.Subscription, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetSubs", topic, opts)
	ret0, _ := ret[0].([]types.Subscription)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetSubs表示GetSubs的预期调用。
func (mr *MockTopicsPersistenceInterfaceMockRecorder) GetSubs(topic, opts interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetSubs", reflect.TypeOf((*MockTopicsPersistenceInterface)(nil).GetSubs), topic, opts)
}

// GetSubsAny模拟基础方法。
func (m *MockTopicsPersistenceInterface) GetSubsAny(topic string, opts *types.QueryOpt) ([]types.Subscription, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetSubsAny", topic, opts)
	ret0, _ := ret[0].([]types.Subscription)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetSubsAny表示GetSubsAny的预期调用。
func (mr *MockTopicsPersistenceInterfaceMockRecorder) GetSubsAny(topic, opts interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetSubsAny", reflect.TypeOf((*MockTopicsPersistenceInterface)(nil).GetSubsAny), topic, opts)
}

// 获取用户模拟基础方法。
func (m *MockTopicsPersistenceInterface) GetUsers(topic string, opts *types.QueryOpt) ([]types.Subscription, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetUsers", topic, opts)
	ret0, _ := ret[0].([]types.Subscription)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetUsers表示GetUsers的预期调用。
func (mr *MockTopicsPersistenceInterfaceMockRecorder) GetUsers(topic, opts interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetUsers", reflect.TypeOf((*MockTopicsPersistenceInterface)(nil).GetUsers), topic, opts)
}

// 获取用户任何模拟基础方法。
func (m *MockTopicsPersistenceInterface) GetUsersAny(topic string, opts *types.QueryOpt) ([]types.Subscription, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetUsersAny", topic, opts)
	ret0, _ := ret[0].([]types.Subscription)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetUsersAny表示GetUsersAny的預期呼叫。
func (mr *MockTopicsPersistenceInterfaceMockRecorder) GetUsersAny(topic, opts interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetUsersAny", reflect.TypeOf((*MockTopicsPersistenceInterface)(nil).GetUsersAny), topic, opts)
}

// 所有者更改模拟基础方法。
func (m *MockTopicsPersistenceInterface) OwnerChange(topic string, newOwner types.Uid) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "OwnerChange", topic, newOwner)
	ret0, _ := ret[0].(error)
	return ret0
}

// OwnerChange表示OwnerChange的预期调用。
func (mr *MockTopicsPersistenceInterfaceMockRecorder) OwnerChange(topic, newOwner interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "OwnerChange", reflect.TypeOf((*MockTopicsPersistenceInterface)(nil).OwnerChange), topic, newOwner)
}

// 更新模拟基础方法。
func (m *MockTopicsPersistenceInterface) Update(topic string, update map[string]any) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Update", topic, update)
	ret0, _ := ret[0].(error)
	return ret0
}

// 更新表示预计的更新呼叫。
func (mr *MockTopicsPersistenceInterfaceMockRecorder) Update(topic, update interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Update", reflect.TypeOf((*MockTopicsPersistenceInterface)(nil).Update), topic, update)
}

// UpdateSubCnt模拟基础方法。
func (m *MockTopicsPersistenceInterface) UpdateSubCnt(topic string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "UpdateSubCnt", topic)
	ret0, _ := ret[0].(error)
	return ret0
}

// UpdateSubCnt表示UpdateSubCnt的预期调用。
func (mr *MockTopicsPersistenceInterfaceMockRecorder) UpdateSubCnt(topic interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "UpdateSubCnt", reflect.TypeOf((*MockTopicsPersistenceInterface)(nil).UpdateSubCnt), topic)
}

// MockSubsPersistenceInterface是SubsPersistenceInterface接口的模拟。
type MockSubsPersistenceInterface struct {
	ctrl     *gomock.Controller
	recorder *MockSubsPersistenceInterfaceMockRecorder
}

// MockSubsPersistenceInterfaceMockRecorder是MockSubsPersistenceInterface的模拟录音机。
type MockSubsPersistenceInterfaceMockRecorder struct {
	mock *MockSubsPersistenceInterface
}

// NewMockSubsPersistenceInterface创建了新的模拟实例。
func NewMockSubsPersistenceInterface(ctrl *gomock.Controller) *MockSubsPersistenceInterface {
	mock := &MockSubsPersistenceInterface{ctrl: ctrl}
	mock.recorder = &MockSubsPersistenceInterfaceMockRecorder{mock}
	return mock
}

// 期待返回一个对象，允许调用者指示预期使用。
func (m *MockSubsPersistenceInterface) EXPECT() *MockSubsPersistenceInterfaceMockRecorder {
	return m.recorder
}

// 创建模拟基础方法。
func (m *MockSubsPersistenceInterface) Create(subs ...*types.Subscription) error {
	m.ctrl.T.Helper()
	varargs := []interface{}{}
	for _, a := range subs {
		varargs = append(varargs, a)
	}
	ret := m.ctrl.Call(m, "Create", varargs...)
	ret0, _ := ret[0].(error)
	return ret0
}

// 创建表示创建的预期调用。
func (mr *MockSubsPersistenceInterfaceMockRecorder) Create(subs ...interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Create", reflect.TypeOf((*MockSubsPersistenceInterface)(nil).Create), subs...)
}

// 删除模拟基础方法。
func (m *MockSubsPersistenceInterface) Delete(topic string, user types.Uid) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Delete", topic, user)
	ret0, _ := ret[0].(error)
	return ret0
}

// 删除表示预期的删除调用。
func (mr *MockSubsPersistenceInterfaceMockRecorder) Delete(topic, user interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Delete", reflect.TypeOf((*MockSubsPersistenceInterface)(nil).Delete), topic, user)
}

// 获取模拟基础方法。
func (m *MockSubsPersistenceInterface) Get(topic string, user types.Uid, keepDeleted bool) (*types.Subscription, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", topic, user, keepDeleted)
	ret0, _ := ret[0].(*types.Subscription)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get表示Get的预期呼叫。
func (mr *MockSubsPersistenceInterfaceMockRecorder) Get(topic, user, keepDeleted interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*MockSubsPersistenceInterface)(nil).Get), topic, user, keepDeleted)
}

// 更新模拟基础方法。
func (m *MockSubsPersistenceInterface) Update(topic string, user types.Uid, update map[string]any) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Update", topic, user, update)
	ret0, _ := ret[0].(error)
	return ret0
}

// 更新表示预计的更新呼叫。
func (mr *MockSubsPersistenceInterfaceMockRecorder) Update(topic, user, update interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Update", reflect.TypeOf((*MockSubsPersistenceInterface)(nil).Update), topic, user, update)
}

// MockMessagesPersistenceInterface是MessagesPersistenceInterface接口的模拟。
type MockMessagesPersistenceInterface struct {
	ctrl     *gomock.Controller
	recorder *MockMessagesPersistenceInterfaceMockRecorder
}

// MockMessagesPersistenceInterfaceMockRecorder是MockMessagesPersistenceInterface的模拟录音机。
type MockMessagesPersistenceInterfaceMockRecorder struct {
	mock *MockMessagesPersistenceInterface
}

// NewMockMessagesPersistenceInterface创建了一个新的模拟实例。
func NewMockMessagesPersistenceInterface(ctrl *gomock.Controller) *MockMessagesPersistenceInterface {
	mock := &MockMessagesPersistenceInterface{ctrl: ctrl}
	mock.recorder = &MockMessagesPersistenceInterfaceMockRecorder{mock}
	return mock
}

// 期待返回一个对象，允许调用者指示预期使用。
func (m *MockMessagesPersistenceInterface) EXPECT() *MockMessagesPersistenceInterfaceMockRecorder {
	return m.recorder
}

// 删除列表模拟基础方法。
func (m *MockMessagesPersistenceInterface) DeleteList(topic string, delID int, forUser types.Uid, msgDelAge time.Duration, ranges []types.Range) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DeleteList", topic, delID, forUser, msgDelAge, ranges)
	ret0, _ := ret[0].(error)
	return ret0
}

// 删除列表表示删除列表的预期调用。
func (mr *MockMessagesPersistenceInterfaceMockRecorder) DeleteList(topic, delID, forUser, msgDelAge, ranges interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DeleteList", reflect.TypeOf((*MockMessagesPersistenceInterface)(nil).DeleteList), topic, delID, forUser, msgDelAge, ranges)
}

// 获取所有模拟基础方法。
func (m *MockMessagesPersistenceInterface) GetAll(topic string, forUser types.Uid, opt *types.QueryOpt) ([]types.Message, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetAll", topic, forUser, opt)
	ret0, _ := ret[0].([]types.Message)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetAll表示GetAll的预期调用。
func (mr *MockMessagesPersistenceInterfaceMockRecorder) GetAll(topic, forUser, opt interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetAll", reflect.TypeOf((*MockMessagesPersistenceInterface)(nil).GetAll), topic, forUser, opt)
}

// GetLatest mocks base method.
func (m *MockMessagesPersistenceInterface) GetLatest(topics []string, forUser types.Uid) ([]types.Message, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetLatest", topics, forUser)
	ret0, _ := ret[0].([]types.Message)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetLatest indicates an expected call of GetLatest.
func (mr *MockMessagesPersistenceInterfaceMockRecorder) GetLatest(topics, forUser interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetLatest", reflect.TypeOf((*MockMessagesPersistenceInterface)(nil).GetLatest), topics, forUser)
}

// GetByClientId模拟基础方法。
func (m *MockMessagesPersistenceInterface) GetByClientId(topic string, from types.Uid, clientID string) (*types.Message, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetByClientId", topic, from, clientID)
	ret0, _ := ret[0].(*types.Message)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetByClientId表示GetByClientId的预期调用。
func (mr *MockMessagesPersistenceInterfaceMockRecorder) GetByClientId(topic, from, clientID interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetByClientId", reflect.TypeOf((*MockMessagesPersistenceInterface)(nil).GetByClientId), topic, from, clientID)
}

// 获取模拟基础方法。
func (m *MockMessagesPersistenceInterface) Get(topic string, seqID int) (*types.Message, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", topic, seqID)
	ret0, _ := ret[0].(*types.Message)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get表示Get的预期呼叫。
func (mr *MockMessagesPersistenceInterfaceMockRecorder) Get(topic, seqID interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*MockMessagesPersistenceInterface)(nil).Get), topic, seqID)
}

// 更新模拟基础方法。
func (m *MockMessagesPersistenceInterface) Update(msg *types.Message) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Update", msg)
	ret0, _ := ret[0].(error)
	return ret0
}

// 更新表示预计的更新呼叫。
func (mr *MockMessagesPersistenceInterfaceMockRecorder) Update(msg interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Update", reflect.TypeOf((*MockMessagesPersistenceInterface)(nil).Update), msg)
}

// 计划模拟基础方法。
func (m *MockMessagesPersistenceInterface) Schedule(msg *types.ScheduledMessage) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Schedule", msg)
	ret0, _ := ret[0].(error)
	return ret0
}

// 时间表表示时间表的预期呼叫。
func (mr *MockMessagesPersistenceInterfaceMockRecorder) Schedule(msg interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Schedule", reflect.TypeOf((*MockMessagesPersistenceInterface)(nil).Schedule), msg)
}

// GetScheduledByClientId模拟基础方法。
func (m *MockMessagesPersistenceInterface) GetScheduledByClientId(topic string, from types.Uid, clientID string) (*types.ScheduledMessage, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetScheduledByClientId", topic, from, clientID)
	ret0, _ := ret[0].(*types.ScheduledMessage)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetScheduledByClientId表示GetScheduledByClientId的预期调用。
func (mr *MockMessagesPersistenceInterfaceMockRecorder) GetScheduledByClientId(topic, from, clientID interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetScheduledByClientId", reflect.TypeOf((*MockMessagesPersistenceInterface)(nil).GetScheduledByClientId), topic, from, clientID)
}

// GetDueScheduled模拟基本方法。
func (m *MockMessagesPersistenceInterface) GetDueScheduled(now time.Time, limit int) ([]types.ScheduledMessage, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetDueScheduled", now, limit)
	ret0, _ := ret[0].([]types.ScheduledMessage)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// GetDueScheduled表示GetDueScheduled的预期调用。
func (mr *MockMessagesPersistenceInterfaceMockRecorder) GetDueScheduled(now, limit interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetDueScheduled", reflect.TypeOf((*MockMessagesPersistenceInterface)(nil).GetDueScheduled), now, limit)
}

// 删除计划模拟基础方法。
func (m *MockMessagesPersistenceInterface) DeleteScheduled(id, topic string, from types.Uid) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DeleteScheduled", id, topic, from)
	ret0, _ := ret[0].(error)
	return ret0
}

// 删除计划显示删除计划的预期调用。
func (mr *MockMessagesPersistenceInterfaceMockRecorder) DeleteScheduled(id, topic, from interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DeleteScheduled", reflect.TypeOf((*MockMessagesPersistenceInterface)(nil).DeleteScheduled), id, topic, from)
}

// GetDeleted模拟基本方法。
func (m *MockMessagesPersistenceInterface) GetDeleted(topic string, forUser types.Uid, opt *types.QueryOpt) ([]types.Range, int, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "GetDeleted", topic, forUser, opt)
	ret0, _ := ret[0].([]types.Range)
	ret1, _ := ret[1].(int)
	ret2, _ := ret[2].(error)
	return ret0, ret1, ret2
}

// GetDeleted表示GetDeleted的预期调用。
func (mr *MockMessagesPersistenceInterfaceMockRecorder) GetDeleted(topic, forUser, opt interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetDeleted", reflect.TypeOf((*MockMessagesPersistenceInterface)(nil).GetDeleted), topic, forUser, opt)
}

// 搜索模拟基础方法。
func (m *MockMessagesPersistenceInterface) Search(topic string, forUser types.Uid, query *types.MessageSearchQuery) ([]types.Message, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Search", topic, forUser, query)
	ret0, _ := ret[0].([]types.Message)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// 搜索表示“搜索”的预期呼叫。
func (mr *MockMessagesPersistenceInterfaceMockRecorder) Search(topic, forUser, query interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Search", reflect.TypeOf((*MockMessagesPersistenceInterface)(nil).Search), topic, forUser, query)
}

// 保存模拟基础方法。
func (m *MockMessagesPersistenceInterface) Save(msg *types.Message, attachmentURLs []string, readBySender bool) (error, bool) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Save", msg, attachmentURLs, readBySender)
	ret0, _ := ret[0].(error)
	ret1, _ := ret[1].(bool)
	return ret0, ret1
}

// 保存表示“保存”的预期调用。
func (mr *MockMessagesPersistenceInterfaceMockRecorder) Save(msg, attachmentURLs, readBySender interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Save", reflect.TypeOf((*MockMessagesPersistenceInterface)(nil).Save), msg, attachmentURLs, readBySender)
}

// MockDevicePersistenceInterface是DevicePersistenceInterface接口的模拟。
type MockDevicePersistenceInterface struct {
	ctrl     *gomock.Controller
	recorder *MockDevicePersistenceInterfaceMockRecorder
}

// MockDevicePersistenceInterfaceMockRecorder是MockDevicePersistenceInterface的模拟录音机。
type MockDevicePersistenceInterfaceMockRecorder struct {
	mock *MockDevicePersistenceInterface
}

// NewMockDevicePersistenceInterface创建一个新的模拟实例。
func NewMockDevicePersistenceInterface(ctrl *gomock.Controller) *MockDevicePersistenceInterface {
	mock := &MockDevicePersistenceInterface{ctrl: ctrl}
	mock.recorder = &MockDevicePersistenceInterfaceMockRecorder{mock}
	return mock
}

// 期待返回一个对象，允许调用者指示预期使用。
func (m *MockDevicePersistenceInterface) EXPECT() *MockDevicePersistenceInterfaceMockRecorder {
	return m.recorder
}

// 删除模拟基础方法。
func (m *MockDevicePersistenceInterface) Delete(uid types.Uid, deviceID string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Delete", uid, deviceID)
	ret0, _ := ret[0].(error)
	return ret0
}

// 删除表示预期的删除调用。
func (mr *MockDevicePersistenceInterfaceMockRecorder) Delete(uid, deviceID interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Delete", reflect.TypeOf((*MockDevicePersistenceInterface)(nil).Delete), uid, deviceID)
}

// 获取所有模拟基础方法。
func (m *MockDevicePersistenceInterface) GetAll(uid ...types.Uid) (map[types.Uid][]types.DeviceDef, int, error) {
	m.ctrl.T.Helper()
	varargs := []interface{}{}
	for _, a := range uid {
		varargs = append(varargs, a)
	}
	ret := m.ctrl.Call(m, "GetAll", varargs...)
	ret0, _ := ret[0].(map[types.Uid][]types.DeviceDef)
	ret1, _ := ret[1].(int)
	ret2, _ := ret[2].(error)
	return ret0, ret1, ret2
}

// GetAll表示GetAll的预期调用。
func (mr *MockDevicePersistenceInterfaceMockRecorder) GetAll(uid ...interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "GetAll", reflect.TypeOf((*MockDevicePersistenceInterface)(nil).GetAll), uid...)
}

// 更新模拟基础方法。
func (m *MockDevicePersistenceInterface) Update(uid types.Uid, oldDeviceID string, dev *types.DeviceDef) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Update", uid, oldDeviceID, dev)
	ret0, _ := ret[0].(error)
	return ret0
}

// 更新表示预计的更新呼叫。
func (mr *MockDevicePersistenceInterfaceMockRecorder) Update(uid, oldDeviceID, dev interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Update", reflect.TypeOf((*MockDevicePersistenceInterface)(nil).Update), uid, oldDeviceID, dev)
}

// MockFilePersistenceInterface是FilePersistenceInterface接口的模拟。
type MockFilePersistenceInterface struct {
	ctrl     *gomock.Controller
	recorder *MockFilePersistenceInterfaceMockRecorder
}

// MockFilePersistenceInterfaceMockRecorder是MockFilePersistenceInterface的模拟录音机。
type MockFilePersistenceInterfaceMockRecorder struct {
	mock *MockFilePersistenceInterface
}

// NewMockFilePersistenceInterface创建一个新的模拟实例。
func NewMockFilePersistenceInterface(ctrl *gomock.Controller) *MockFilePersistenceInterface {
	mock := &MockFilePersistenceInterface{ctrl: ctrl}
	mock.recorder = &MockFilePersistenceInterfaceMockRecorder{mock}
	return mock
}

// 期待返回一个对象，允许调用者指示预期使用。
func (m *MockFilePersistenceInterface) EXPECT() *MockFilePersistenceInterfaceMockRecorder {
	return m.recorder
}

// 删除未使用的模拟基础方法。
func (m *MockFilePersistenceInterface) DeleteUnused(olderThan time.Time, limit int) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "DeleteUnused", olderThan, limit)
	ret0, _ := ret[0].(error)
	return ret0
}

// 删除未使用表示删除未使用的预期调用。
func (mr *MockFilePersistenceInterfaceMockRecorder) DeleteUnused(olderThan, limit interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "DeleteUnused", reflect.TypeOf((*MockFilePersistenceInterface)(nil).DeleteUnused), olderThan, limit)
}

// 完成上传模拟基础方法。
func (m *MockFilePersistenceInterface) FinishUpload(fd *types.FileDef, success bool, size int64) (*types.FileDef, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "FinishUpload", fd, success, size)
	ret0, _ := ret[0].(*types.FileDef)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// FinishUpload表示FinishUpload的预期调用。
func (mr *MockFilePersistenceInterfaceMockRecorder) FinishUpload(fd, success, size interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "FinishUpload", reflect.TypeOf((*MockFilePersistenceInterface)(nil).FinishUpload), fd, success, size)
}

// 获取模拟基础方法。
func (m *MockFilePersistenceInterface) Get(fid string) (*types.FileDef, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", fid)
	ret0, _ := ret[0].(*types.FileDef)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get表示Get的预期呼叫。
func (mr *MockFilePersistenceInterfaceMockRecorder) Get(fid interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*MockFilePersistenceInterface)(nil).Get), fid)
}

// 链接附件模拟基础方法。
func (m *MockFilePersistenceInterface) LinkAttachments(topic string, msgId types.Uid, attachments []string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "LinkAttachments", topic, msgId, attachments)
	ret0, _ := ret[0].(error)
	return ret0
}

// LinkAttachments表示LinkAttachments的预期调用。
func (mr *MockFilePersistenceInterfaceMockRecorder) LinkAttachments(topic, msgId, attachments interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "LinkAttachments", reflect.TypeOf((*MockFilePersistenceInterface)(nil).LinkAttachments), topic, msgId, attachments)
}

// 启动上传模拟基础方法。
func (m *MockFilePersistenceInterface) StartUpload(fd *types.FileDef) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "StartUpload", fd)
	ret0, _ := ret[0].(error)
	return ret0
}

// 启动上传表示启动上传的预期调用。
func (mr *MockFilePersistenceInterfaceMockRecorder) StartUpload(fd interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "StartUpload", reflect.TypeOf((*MockFilePersistenceInterface)(nil).StartUpload), fd)
}

// MockPersistentCacheInterface是PersistentCacheInterface接口的模拟。
type MockPersistentCacheInterface struct {
	ctrl     *gomock.Controller
	recorder *MockPersistentCacheInterfaceMockRecorder
}

// MockPersistentCacheInterfaceMockRecorder是MockPersistentCacheInterface的模拟记录器。
type MockPersistentCacheInterfaceMockRecorder struct {
	mock *MockPersistentCacheInterface
}

// NewMockPersistentCacheInterface创建了新的模拟实例。
func NewMockPersistentCacheInterface(ctrl *gomock.Controller) *MockPersistentCacheInterface {
	mock := &MockPersistentCacheInterface{ctrl: ctrl}
	mock.recorder = &MockPersistentCacheInterfaceMockRecorder{mock}
	return mock
}

// 期待返回一个对象，允许调用者指示预期使用。
func (m *MockPersistentCacheInterface) EXPECT() *MockPersistentCacheInterfaceMockRecorder {
	return m.recorder
}

// 比较和交换模拟基本方法。
func (m *MockPersistentCacheInterface) CompareAndSwap(key, oldValue, newValue string) (bool, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "CompareAndSwap", key, oldValue, newValue)
	ret0, _ := ret[0].(bool)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// 比较和交换表示比较和交换的预期呼叫。
func (mr *MockPersistentCacheInterfaceMockRecorder) CompareAndSwap(key, oldValue, newValue interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "CompareAndSwap",
		reflect.TypeOf((*MockPersistentCacheInterface)(nil).CompareAndSwap), key, oldValue, newValue)
}

// 删除模拟基础方法。
func (m *MockPersistentCacheInterface) Delete(key string) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Delete", key)
	ret0, _ := ret[0].(error)
	return ret0
}

// 删除表示预期的删除调用。
func (mr *MockPersistentCacheInterfaceMockRecorder) Delete(key interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Delete", reflect.TypeOf((*MockPersistentCacheInterface)(nil).Delete), key)
}

// 过期模拟基础方法。
func (m *MockPersistentCacheInterface) Expire(keyPrefix string, olderThan time.Time) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Expire", keyPrefix, olderThan)
	ret0, _ := ret[0].(error)
	return ret0
}

// 到期表示到期的预期呼叫。
func (mr *MockPersistentCacheInterfaceMockRecorder) Expire(keyPrefix, olderThan interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Expire", reflect.TypeOf((*MockPersistentCacheInterface)(nil).Expire), keyPrefix, olderThan)
}

// 获取模拟基础方法。
func (m *MockPersistentCacheInterface) Get(key string) (string, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Get", key)
	ret0, _ := ret[0].(string)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// Get表示Get的预期呼叫。
func (mr *MockPersistentCacheInterfaceMockRecorder) Get(key interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Get", reflect.TypeOf((*MockPersistentCacheInterface)(nil).Get), key)
}

// 列表模拟基础方法。
func (m *MockPersistentCacheInterface) List(keyPrefix string, limit int) (map[string]string, error) {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "List", keyPrefix, limit)
	ret0, _ := ret[0].(map[string]string)
	ret1, _ := ret[1].(error)
	return ret0, ret1
}

// 列表表示列表的预期调用。
func (mr *MockPersistentCacheInterfaceMockRecorder) List(keyPrefix, limit interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "List",
		reflect.TypeOf((*MockPersistentCacheInterface)(nil).List), keyPrefix, limit)
}

// 向上模拟基础方法。
func (m *MockPersistentCacheInterface) Upsert(key, value string, failOnDuplicate bool) error {
	m.ctrl.T.Helper()
	ret := m.ctrl.Call(m, "Upsert", key, value, failOnDuplicate)
	ret0, _ := ret[0].(error)
	return ret0
}

// Upsert表示Upsert的预期呼叫。
func (mr *MockPersistentCacheInterfaceMockRecorder) Upsert(key, value, failOnDuplicate interface{}) *gomock.Call {
	mr.mock.ctrl.T.Helper()
	return mr.mock.ctrl.RecordCallWithMethodType(mr.mock, "Upsert", reflect.TypeOf((*MockPersistentCacheInterface)(nil).Upsert), key, value, failOnDuplicate)
}
