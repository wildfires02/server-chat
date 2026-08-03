package server

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"time"

	admincontrol "chat/server/admin"
	"chat/server/store"
	"chat/server/store/types"
)

const (
	officialReportPrefix      = "official-topic:report:"
	officialReportQuotaPrefix = "official-topic:report-quota:"
)

type officialMessageReport struct {
	ID         string    `json:"id"`
	Topic      string    `json:"topic"`
	SeqID      int       `json:"seq_id"`
	Reporter   string    `json:"reporter"`
	Reason     string    `json:"reason"`
	Note       string    `json:"note,omitempty"`
	Status     string    `json:"status"`
	CreatedAt  time.Time `json:"created_at"`
	ReviewedAt time.Time `json:"reviewed_at,omitempty"`
	ReviewedBy string    `json:"reviewed_by,omitempty"`
	ReviewNote string    `json:"review_note,omitempty"`
	Version    uint64    `json:"version"`
}

type officialReportDecisionInput struct {
	Note string `json:"note,omitempty"`
}

type officialReportQuota struct {
	Day   string `json:"day"`
	Count int    `json:"count"`
}

func validOfficialReportReason(reason string) bool {
	switch reason {
	case "spam", "abuse", "fraud", "illegal", "privacy", "other":
		return true
	default:
		return false
	}
}

func consumeOfficialReportQuota(topic string, uid types.Uid, now time.Time) error {
	key := officialReportQuotaPrefix + topic + ":" + uid.String()
	day := now.UTC().Format("2006-01-02")
	for attempt := 0; attempt < 8; attempt++ {
		oldRaw, err := store.PCache.Get(key)
		if errors.Is(err, types.ErrNotFound) {
			newRaw, _ := json.Marshal(officialReportQuota{Day: day, Count: 1})
			if err = store.PCache.Upsert(key, string(newRaw), true); err == nil {
				return nil
			}
			continue
		}
		if err != nil {
			return err
		}
		var quota officialReportQuota
		if json.Unmarshal([]byte(oldRaw), &quota) != nil || quota.Day != day {
			quota = officialReportQuota{Day: day}
		}
		if quota.Count >= 20 {
			return admincontrol.ErrProtected
		}
		quota.Count++
		newRaw, err := json.Marshal(quota)
		if err != nil {
			return err
		}
		swapped, err := store.PCache.CompareAndSwap(key, oldRaw, string(newRaw))
		if err != nil {
			return err
		}
		if swapped {
			return nil
		}
	}
	return admincontrol.ErrConflict
}

func (t *Topic) replySetReport(sess *Session, uid types.Uid, pkt *ClientComMessage) error {
	now := types.TimeNow()
	if !t.isOfficialTopic() || pkt.Set == nil || pkt.Set.Report == nil {
		sess.queueOut(ErrMalformedReply(pkt, now))
		return types.ErrMalformed
	}
	request := pkt.Set.Report
	request.Reason = strings.ToLower(strings.TrimSpace(request.Reason))
	request.Note = strings.TrimSpace(request.Note)
	pud, found := t.perUser[uid]
	mode := pud.modeWant & pud.modeGiven
	if !found || pud.deleted || !mode.IsJoiner() || !mode.IsReader() ||
		request.SeqID <= 0 || request.SeqID > t.lastID ||
		!validOfficialReportReason(request.Reason) || len(request.Note) > 500 {
		sess.queueOut(ErrPermissionDeniedReply(pkt, now))
		return types.ErrPermissionDenied
	}
	if err := consumeOfficialReportQuota(t.name, uid, now); err != nil {
		if errors.Is(err, admincontrol.ErrProtected) {
			sess.queueOut(ErrPolicyReply(pkt, now))
		} else {
			sess.queueOut(ErrServiceUnavailableReply(pkt, now))
		}
		return err
	}
	random := make([]byte, 12)
	if _, err := rand.Read(random); err != nil {
		sess.queueOut(ErrServiceUnavailableReply(pkt, now))
		return err
	}
	report := officialMessageReport{
		ID: hex.EncodeToString(random), Topic: t.name, SeqID: request.SeqID,
		Reporter: uid.UserId(), Reason: request.Reason, Note: request.Note,
		Status: "open", CreatedAt: now.UTC(), Version: 1,
	}
	raw, err := json.Marshal(report)
	if err == nil {
		err = store.PCache.Upsert(officialReportPrefix+t.name+":"+report.ID, string(raw), true)
	}
	if err != nil {
		sess.queueOut(ErrServiceUnavailableReply(pkt, now))
		return err
	}
	sess.queueOut(NoErrParamsReply(pkt, now, map[string]any{
		"report": map[string]any{"id": report.ID, "status": report.Status},
	}))
	return nil
}

func listOfficialReports(topic, status string, limit int) ([]officialMessageReport, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	entries, err := store.PCache.List(officialReportPrefix+topic+":", limit*3)
	if err != nil {
		return nil, err
	}
	result := make([]officialMessageReport, 0, len(entries))
	for _, raw := range entries {
		var report officialMessageReport
		if json.Unmarshal([]byte(raw), &report) != nil || report.Topic != topic {
			continue
		}
		if status != "" && report.Status != status {
			continue
		}
		result = append(result, report)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].CreatedAt.After(result[j].CreatedAt) })
	if len(result) > limit {
		result = result[:limit]
	}
	return result, nil
}

func decideOfficialReport(topic, reportID, status, reviewer, note string,
	now time.Time) (officialMessageReport, error) {
	if status != "resolved" && status != "dismissed" {
		return officialMessageReport{}, admincontrol.ErrInvalid
	}
	note = strings.TrimSpace(note)
	if len(note) > 500 {
		return officialMessageReport{}, admincontrol.ErrInvalid
	}
	key := officialReportPrefix + topic + ":" + reportID
	for attempt := 0; attempt < 8; attempt++ {
		oldRaw, err := store.PCache.Get(key)
		if err != nil {
			return officialMessageReport{}, err
		}
		var report officialMessageReport
		if json.Unmarshal([]byte(oldRaw), &report) != nil || report.Topic != topic {
			return officialMessageReport{}, admincontrol.ErrInvalid
		}
		if report.Status != "open" {
			return report, admincontrol.ErrConflict
		}
		report.Status = status
		report.ReviewedAt = now.UTC()
		report.ReviewedBy = reviewer
		report.ReviewNote = note
		report.Version++
		newRaw, err := json.Marshal(report)
		if err != nil {
			return officialMessageReport{}, err
		}
		swapped, err := store.PCache.CompareAndSwap(key, oldRaw, string(newRaw))
		if err != nil {
			return officialMessageReport{}, err
		}
		if swapped {
			return report, nil
		}
	}
	return officialMessageReport{}, admincontrol.ErrConflict
}
