/******************************************************************************
 *
 *  描述 :
 *
 *  处理 Session 在内存中的索引存储、生命周期维护及批量淘汰机制。
 *
 *****************************************************************************/

package main

import (
	"container/list"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"chat/pbx"
	"chat/server/logs"
	"chat/server/store"
	"chat/server/store/types"
)

// SessionStore 表示活动会话在内存中的全局集中存储和索引结构。
type SessionStore struct {
	lock sync.Mutex

	// 存放长轮询 Session 的双向链表，按最后活跃时间升序排列（最旧的在链表尾部），用于 LRU 超时淘汰。
	lru *list.List

	// Session 映射表，按会话 Sid 索引
	sessCache map[string]*Session

	// 快速索引映射表：用户 UID -> 该用户的所有活动 Session 集合 (sid -> *Session)
	uidCache map[types.Uid]map[string]*Session

	// 长轮询 Session 未活动超时淘汰的时间跨度
	lifeTime time.Duration
}

// NewSessionStore 创建并初始化 SessionStore 实例。
func NewSessionStore(lifetime time.Duration) *SessionStore {
	return &SessionStore{
		lru:       list.New(),
		sessCache: make(map[string]*Session),
		uidCache:  make(map[types.Uid]map[string]*Session),
		lifeTime:  lifetime,
	}
}

// NewSession 创建新的 Session 实例并将其注册到 SessionStore 存储中。
// 参数 conn 支持 *websocket.Conn, http.ResponseWriter, *ClusterNode, pbx.Node_MessageLoopServer 等类型。
// 第二个返回值表示创建后内存中的总活动会话数。
func (ss *SessionStore) NewSession(conn any, sid string) (*Session, int) {
	var s Session

	if sid == "" {
		s.sid = store.Store.GetUidString()
	} else {
		s.sid = sid
	}

	switch c := conn.(type) {
	case *websocket.Conn:
		s.proto = WEBSOCK
		s.ws = c
	case http.ResponseWriter:
		s.proto = LPOLL
		// 长轮询不需要在此保存 http.ResponseWriter，因为每次轮询 HTTP 请求都会更新
	case *ClusterNode:
		s.proto = MULTIPLEX
		s.clnode = c
	case pbx.Node_MessageLoopServer:
		s.proto = GRPC
		s.grpcnode = c
	default:
		logs.Err.Panicln("session: 未知的连接类型", conn)
	}

	s.subs = make(map[string]*Subscription)
	s.send = make(chan any, sendQueueLimit+32) // 带缓冲
	s.stop = make(chan any, 1)                 // 缓冲大小 1 保证非阻塞关机
	s.detach = make(chan string, 64)           // 带缓冲

	s.bkgTimer = time.NewTimer(time.Hour)
	s.bkgTimer.Stop()

	// 保证同一时间最多有 1 个请求在修改该 Session/Topic 的状态
	s.inflightReqs = newBoundedWaitGroup(1)

	s.lastTouched = time.Now()

	ss.lock.Lock()

	if s.proto == LPOLL {
		// 仅长轮询会话需要按最后活动时间在 LRU 链表中排序
		s.lpTracker = ss.lru.PushFront(&s)
	}

	ss.sessCache[s.sid] = &s

	// 自动清理淘汰超时的长轮询 Session。
	var expired []*Session
	expire := s.lastTouched.Add(-ss.lifeTime)
	for elem := ss.lru.Back(); elem != nil; elem = ss.lru.Back() {
		sess := elem.Value.(*Session)
		if sess.lastTouched.Before(expire) {
			ss.lru.Remove(elem)
			delete(ss.sessCache, sess.sid)
			expired = append(expired, sess)
		} else {
			break
		}
	}

	numSessions := len(ss.sessCache)
	statsSet("LiveSessions", int64(numSessions))
	statsInc("TotalSessions", 1)

	ss.lock.Unlock()

	// 清理过期的长轮询会话（在锁外执行，避免锁竞争死锁）。
	for _, sess := range expired {
		sess.cleanUp(true)
	}

	return &s, numSessions
}

// Get 根据会话 Sid 从 SessionStore 中获取对应的 Session 实例。
func (ss *SessionStore) Get(sid string) *Session {
	ss.lock.Lock()
	defer ss.lock.Unlock()

	if sess := ss.sessCache[sid]; sess != nil {
		if sess.proto == LPOLL {
			ss.lru.MoveToFront(sess.lpTracker)
			sess.lastTouched = time.Now()
		}

		return sess
	}

	return nil
}

// SetSessionUid 在 SessionStore 的索引中将指定 Session 与用户 UID 绑定关联。
func (ss *SessionStore) SetSessionUid(s *Session, uid types.Uid) {
	if ss == nil {
		s.uid = uid
		return
	}
	ss.lock.Lock()
	defer ss.lock.Unlock()

	if !s.uid.IsZero() {
		if m := ss.uidCache[s.uid]; m != nil {
			delete(m, s.sid)
			if len(m) == 0 {
				delete(ss.uidCache, s.uid)
			}
		}
	}

	s.uid = uid

	if !uid.IsZero() {
		m := ss.uidCache[uid]
		if m == nil {
			m = make(map[string]*Session)
			ss.uidCache[uid] = m
		}
		m[s.sid] = s
	}
}

// Delete 从 SessionStore 存储与索引中彻底移除指定 Session。
func (ss *SessionStore) Delete(s *Session) {
	ss.lock.Lock()
	defer ss.lock.Unlock()

	delete(ss.sessCache, s.sid)
	if !s.uid.IsZero() {
		if m := ss.uidCache[s.uid]; m != nil {
			delete(m, s.sid)
			if len(m) == 0 {
				delete(ss.uidCache, s.uid)
			}
		}
	}
	if s.proto == LPOLL {
		ss.lru.Remove(s.lpTracker)
	}

	statsSet("LiveSessions", int64(len(ss.sessCache)))
}

// Range 遍历 SessionStore 中的所有活动会话。若传入的闭包函数返回 false 则中途停止遍历。
func (ss *SessionStore) Range(f func(sid string, s *Session) bool) {
	ss.lock.Lock()
	for sid, s := range ss.sessCache {
		if !f(sid, s) {
			break
		}
	}
	ss.lock.Unlock()
}

// Shutdown 终止 SessionStore 中保存的所有活动会话并发送关机下行广播。
func (ss *SessionStore) Shutdown() {
	ss.lock.Lock()
	defer ss.lock.Unlock()

	shutdown := NoErrShutdown(types.TimeNow())
	for _, s := range ss.sessCache {
		if !s.isMultiplex() {
			_, data := s.serialize(shutdown)
			s.stopSession(data)
		}
	}

	if globals.cluster != nil {
		logs.Info.Println("集群模式：会话存储库在节点", globals.cluster.thisNodeName, "上已成功关闭")
	}

	logs.Info.Println("SessionStore 存储库已关停，清理活动会话数:", len(ss.sessCache))
}

// EvictUser 强制断开并剔除指定 UID 用户的全部活动 Session（可保留跳过指定的 skipSid 会话）。
func (ss *SessionStore) EvictUser(uid types.Uid, skipSid string) {
	ss.lock.Lock()
	defer ss.lock.Unlock()

	userSess := ss.uidCache[uid]
	if len(userSess) == 0 {
		return
	}

	evicted := NoErrEvicted("", "", types.TimeNow())
	for sid, s := range userSess {
		if sid != skipSid {
			_, data := s.serialize(evicted)
			s.stopSession(data)
		}
	}
}

// GetUserSessions 获取指定 UID 用户的全部活动 Session 切片列表。
func (ss *SessionStore) GetUserSessions(uid types.Uid) []*Session {
	ss.lock.Lock()
	defer ss.lock.Unlock()

	userSess := ss.uidCache[uid]
	if len(userSess) == 0 {
		return nil
	}

	result := make([]*Session, 0, len(userSess))
	for _, s := range userSess {
		result = append(result, s)
	}
	return result
}
