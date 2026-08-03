// Package server 实现关键词 Peer 发现与当前 Topic 消息全文搜索。
package server

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"unicode/utf8"

	"chat/server/auth"
	"chat/server/store"
	"chat/server/store/types"
	"golang.org/x/text/unicode/norm"
)

const (
	searchDefaultLimit  = 20
	searchMaxLimit      = 100
	searchMaxQueryRunes = 256
)

// searchCursor 是服务端分页状态的内部编码形式。
// Key 绑定完整查询条件，防止客户端把游标复用于另一个搜索请求。
type searchCursor struct {
	Version   int    `json:"v"`
	Scope     string `json:"s"`
	Key       string `json:"k"`
	Offset    int    `json:"o,omitempty"`
	BeforeSeq int    `json:"b,omitempty"`
}

// searchQueryKey 为会影响结果集的查询参数生成稳定摘要。
func searchQueryKey(topic string, opts *MsgSearchOpts) string {
	kinds := append([]string(nil), opts.Kinds...)
	sort.Strings(kinds)
	payload := struct {
		Topic   string
		Scope   string
		Query   string
		From    string
		Kinds   []string
		MinDate int64
		MaxDate int64
	}{
		Topic: topic,
		Scope: opts.Scope,
		Query: opts.Query,
		From:  opts.From,
		Kinds: kinds,
	}
	if opts.MinDate != nil {
		payload.MinDate = opts.MinDate.UnixNano()
	}
	if opts.MaxDate != nil {
		payload.MaxDate = opts.MaxDate.UnixNano()
	}
	encoded, _ := json.Marshal(payload)
	sum := sha256.Sum256(encoded)
	return hex.EncodeToString(sum[:12])
}

// encodeSearchCursor 把分页状态编码为客户端不可依赖内部结构的不透明字符串。
func encodeSearchCursor(cursor searchCursor) string {
	encoded, _ := json.Marshal(cursor)
	return base64.RawURLEncoding.EncodeToString(encoded)
}

// decodeSearchCursor 解码并校验游标与当前查询是否一致。
func decodeSearchCursor(raw, scope, key string) (searchCursor, error) {
	if raw == "" {
		return searchCursor{Version: 1, Scope: scope, Key: key}, nil
	}
	encoded, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil {
		return searchCursor{}, types.ErrMalformed
	}
	var cursor searchCursor
	if err = json.Unmarshal(encoded, &cursor); err != nil ||
		cursor.Version != 1 || cursor.Scope != scope || cursor.Key != key ||
		cursor.Offset < 0 || cursor.BeforeSeq < 0 {
		return searchCursor{}, types.ErrMalformed
	}
	return cursor, nil
}

// normalizeSearchOpts 校验公共搜索参数并填充默认值。
func normalizeSearchOpts(topicCategory types.TopicCat, opts *MsgSearchOpts) error {
	if opts == nil {
		return types.ErrMalformed
	}
	// 与消息写入阶段使用相同的 Unicode 兼容归一化，避免全角字符等价形式漏检。
	opts.Query = strings.TrimSpace(norm.NFKC.String(opts.Query))
	if opts.Scope == "" {
		if topicCategory == types.TopicCatFnd {
			opts.Scope = types.SearchScopePeers
		} else {
			opts.Scope = types.SearchScopeTopic
		}
	}
	opts.Scope = strings.ToLower(strings.TrimSpace(opts.Scope))
	if !utf8.ValidString(opts.Query) || utf8.RuneCountInString(opts.Query) < 2 ||
		utf8.RuneCountInString(opts.Query) > searchMaxQueryRunes {
		return types.ErrMalformed
	}
	if opts.Limit <= 0 {
		opts.Limit = searchDefaultLimit
	} else if opts.Limit > searchMaxLimit {
		opts.Limit = searchMaxLimit
	}
	if opts.MinDate != nil && opts.MaxDate != nil && !opts.MinDate.Before(*opts.MaxDate) {
		return types.ErrMalformed
	}
	return nil
}

// searchPeerToProtocol 将持久层统一的 Subscription 搜索结果转换为客户端 Peer。
func searchPeerToProtocol(sub *types.Subscription) MsgTopicSub {
	result := MsgTopicSub{
		Topic:     sub.Topic,
		UpdatedAt: &sub.UpdatedAt,
		Public:    sub.GetPublic(),
		Trusted:   externalIdentityClientTrusted(sub.GetTrusted()),
		Private:   sub.Private,
		SubCnt:    sub.GetSubCnt(),
	}
	if sub.ModeGiven.IsDefined() && sub.ModeWant.IsDefined() {
		result.Acs.Mode = (sub.ModeGiven & sub.ModeWant).String()
	} else if types.IsChannel(sub.Topic) {
		result.Acs.Mode = types.ModeCChnReader.String()
	} else if defacs := sub.GetDefaultAccess(); defacs != nil {
		result.Acs.Mode = defacs.Auth.String()
	}
	return result
}

// replySearch 根据 Scope 执行 Peer 发现或当前 Topic 消息全文搜索。
func (t *Topic) replySearch(sess *Session, asUid types.Uid, asChan bool,
	authLevel auth.Level, msg *ClientComMessage) error {

	now := types.TimeNow()
	opts := msg.Get.Search
	if err := normalizeSearchOpts(t.cat, opts); err != nil {
		sess.queueOut(ErrMalformedReply(msg, now))
		return err
	}
	key := searchQueryKey(t.name, opts)
	cursor, err := decodeSearchCursor(opts.Cursor, opts.Scope, key)
	if err != nil {
		sess.queueOut(ErrMalformedReply(msg, now))
		return err
	}

	result := &MsgSearchResult{Scope: opts.Scope}
	var startTranslations []func()
	switch opts.Scope {
	case types.SearchScopePeers:
		if t.cat != types.TopicCatFnd || asChan {
			sess.queueOut(ErrPermissionDeniedReply(msg, now))
			return types.ErrPermissionDenied
		}
		query := &types.PeerSearchQuery{
			Query:       opts.Query,
			AliasPrefix: globals.aliasTagNS,
			Offset:      cursor.Offset,
			Limit:       opts.Limit + 1,
			ActiveOnly:  authLevel != auth.LevelRoot,
		}
		found, searchErr := store.Users.Search(asUid, query)
		if searchErr != nil {
			sess.queueOut(decodeStoreErrorExplicitTs(searchErr, msg.Id, msg.Original, now, msg.Timestamp, nil))
			return searchErr
		}
		hasMore := len(found) > opts.Limit
		if hasMore {
			found = found[:opts.Limit]
		}
		result.Peers = make([]MsgTopicSub, 0, len(found))
		for i := range found {
			result.Peers = append(result.Peers, searchPeerToProtocol(&found[i]))
		}
		if hasMore {
			cursor.Offset += len(found)
			result.Next = encodeSearchCursor(cursor)
		}

	case types.SearchScopeTopic:
		if t.cat != types.TopicCatP2P && t.cat != types.TopicCatGrp {
			sess.queueOut(ErrPermissionDeniedReply(msg, now))
			return types.ErrPermissionDenied
		}
		userData, ok := t.perUser[asUid]
		mode := userData.modeGiven & userData.modeWant
		if !ok || !mode.IsReader() {
			sess.queueOut(ErrPermissionDeniedReply(msg, now))
			return types.ErrPermissionDenied
		}
		from := types.ZeroUid
		if opts.From != "" {
			from = types.ParseUserId(opts.From)
			if from.IsZero() {
				sess.queueOut(ErrMalformedReply(msg, now))
				return types.ErrMalformed
			}
		}
		kinds := make([]string, 0, len(opts.Kinds))
		for _, kind := range opts.Kinds {
			kind = strings.ToLower(strings.TrimSpace(kind))
			if !validMessageKinds[kind] {
				sess.queueOut(ErrMalformedReply(msg, now))
				return types.ErrMalformed
			}
			kinds = append(kinds, kind)
		}
		found, searchErr := store.Messages.Search(t.name, asUid, &types.MessageSearchQuery{
			Query:     opts.Query,
			From:      from,
			Kinds:     kinds,
			MinDate:   opts.MinDate,
			MaxDate:   opts.MaxDate,
			BeforeSeq: cursor.BeforeSeq,
			Limit:     opts.Limit + 1,
		})
		if searchErr != nil {
			sess.queueOut(decodeStoreErrorExplicitTs(searchErr, msg.Id, msg.Original, now, msg.Timestamp, nil))
			return searchErr
		}
		hasMore := len(found) > opts.Limit
		if hasMore {
			found = found[:opts.Limit]
		}
		result.Messages = make([]*MsgServerData, 0, len(found))
		startTranslations = make([]func(), 0, len(found))
		for i := range found {
			fromID := types.ParseUid(found[i].From).UserId()
			data := serverDataFromStored(msg.Original, fromID, &found[i])
			if t.cat == types.TopicCatP2P && globals.translation != nil {
				var start func()
				data, start = globals.translation.projectHistoricalData(t.name, data, sess, asUid)
				if start != nil {
					startTranslations = append(startTranslations, start)
				}
			}
			result.Messages = append(result.Messages, data)
		}
		if hasMore && len(found) > 0 {
			cursor.BeforeSeq = found[len(found)-1].SeqId
			result.Next = encodeSearchCursor(cursor)
		}

	default:
		sess.queueOut(ErrMalformedReply(msg, now))
		return errors.New("unsupported search scope")
	}

	if !sess.queueOut(&ServerComMessage{Meta: &MsgServerMeta{
		Id:        msg.Id,
		Topic:     msg.Original,
		Timestamp: &now,
		Search:    result,
	}}) {
		return errors.New("session send queue is full")
	}
	for _, start := range startTranslations {
		start()
	}
	return nil
}
