package genshin

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/shiroha-a/mk/plugin"
)

const maxRankingCandidates = 10000

var profileRankingMetrics = []string{"spiral", "achievements", "friendship", "stygian"}

type rankingEntry struct {
	Rank       int       `json:"rank"`
	UserID     string    `json:"userId"`
	AccountID  string    `json:"accountId"`
	UID        string    `json:"uid,omitempty"`
	Nickname   string    `json:"nickname"`
	Value      int       `json:"value"`
	Difficulty int       `json:"difficulty,omitempty"`
	Seconds    int       `json:"seconds,omitempty"`
	FetchedAt  time.Time `json:"fetchedAt"`
	scheduleID int
}

type rankingRequest struct {
	Metric     string `json:"metric"`
	ScheduleID int    `json:"scheduleId"`
	Limit      int    `json:"limit"`
	Offset     int    `json:"offset"`
	AccountID  string `json:"accountId"`
}

type rankingQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func rankingResponse(c context.Context, ctx plugin.Context, db *sql.DB, req plugin.Request, guard *rankingScanGuard) (any, error) {
	var body rankingRequest
	if req.Bind(&body) != nil || !validRankingRequest(body) {
		return nil, plugin.Errorf(400, "ランキングの条件が不正です")
	}
	if ctx.API() == nil {
		return nil, plugin.Errorf(503, "ランキングのユーザー状態を確認できません")
	}
	return rankingResponseBody(c, ctx, db, body, guard, nil)
}

func profileRankingResponse(c context.Context, ctx plugin.Context, db *sql.DB, req plugin.Request, guard *rankingScanGuard) (any, error) {
	var body struct {
		AccountID string `json:"accountId"`
	}
	if req.Bind(&body) != nil || body.AccountID == "" || len(body.AccountID) > 128 {
		return nil, plugin.Errorf(400, "ランキングの連携IDが不正です")
	}
	if ctx.API() == nil {
		return nil, plugin.Errorf(503, "ランキングのユーザー状態を確認できません")
	}

	tx, err := db.BeginTx(c, &sql.TxOptions{Isolation: sql.LevelRepeatableRead, ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	entriesByMetric := make(map[string][]rankingEntry, len(profileRankingMetrics))
	ids := make([]string, 0)
	for _, metric := range profileRankingMetrics {
		entries, err := rankingEntries(c, tx, rankingRequest{Metric: metric})
		if err != nil {
			return nil, err
		}
		if len(entries) > maxRankingCandidates {
			return nil, plugin.Errorf(503, "ランキングの集計対象が上限を超えています")
		}
		entriesByMetric[metric] = entries
		for _, entry := range entries {
			ids = append(ids, entry.UserID)
		}
	}
	eligible, err := rankingEligibleUserIDs(c, ctx, ids, guard)
	if err != nil {
		return nil, err
	}

	rankings := map[string]any{}
	for _, metric := range profileRankingMetrics {
		rankings[metric] = visibleRanking(rankingRequest{Metric: metric, AccountID: body.AccountID, Limit: 50}, entriesByMetric[metric], eligible)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"rankings": rankings}, nil
}

func validRankingRequest(body rankingRequest) bool {
	if body.ScheduleID < 0 || body.Offset < 0 || body.Offset > maxRankingCandidates || body.Limit < 0 || body.Limit > 100 || len(body.AccountID) > 128 || (body.AccountID != "" && body.Offset != 0) {
		return false
	}
	switch body.Metric {
	case "spiral", "achievements", "friendship", "stygian":
		return true
	default:
		return false
	}
}

func rankingResponseBody(c context.Context, ctx plugin.Context, db rankingQueryer, body rankingRequest, guard *rankingScanGuard, eligible map[string]bool) (any, error) {
	if body.Limit == 0 {
		body.Limit = 50
	}
	entries, err := rankingEntries(c, db, body)
	if err != nil {
		return nil, err
	}
	if len(entries) > maxRankingCandidates {
		return nil, plugin.Errorf(503, "ランキングの集計対象が上限を超えています")
	}
	if eligible == nil {
		ids := make([]string, 0, len(entries))
		for _, entry := range entries {
			ids = append(ids, entry.UserID)
		}
		eligible, err = rankingEligibleUserIDs(c, ctx, ids, guard)
		if err != nil {
			return nil, err
		}
	}
	return visibleRanking(body, entries, eligible), nil
}

func rankingEntries(c context.Context, db rankingQueryer, body rankingRequest) ([]rankingEntry, error) {
	column := ""
	switch body.Metric {
	case "spiral":
		column = "tower_star"
	case "achievements":
		column = "achievements"
	case "friendship":
		column = "fetter_count"
	case "stygian":
		column = "stygian_difficulty"
	}
	order := "s." + column + " DESC"
	filter := "AND $1::int>=0"
	if body.Metric == "stygian" {
		order = "s.stygian_id DESC, " + order + ", s.stygian_seconds ASC"
		filter = "AND ($1=0 OR s.stygian_id=$1) AND s.stygian_id>0 AND s.stygian_difficulty>0 AND s.stygian_seconds>0"
	}
	// Privacy and participation settings are deliberately read on every request.
	// Eligibility coalescing must never cache or replace this query.
	query := fmt.Sprintf(`SELECT a.user_id,a.public_id,CASE WHEN COALESCE(p.publish_uid,true) THEN a.uid ELSE '' END,
		s.nickname,s.%s,s.stygian_difficulty,s.stygian_seconds,s.fetched_at,s.stygian_id
		FROM accounts a JOIN snapshots s ON s.uid=a.uid LEFT JOIN user_preferences p ON p.user_id=a.user_id
		WHERE COALESCE(p.ranking_enabled,true) %s ORDER BY %s,a.public_id LIMIT $2`, column, filter, order)
	rows, err := db.QueryContext(c, query, body.ScheduleID, maxRankingCandidates+1)
	if err != nil {
		return nil, err
	}
	entries := []rankingEntry{}
	for rows.Next() {
		var entry rankingEntry
		if err := rows.Scan(&entry.UserID, &entry.AccountID, &entry.UID, &entry.Nickname, &entry.Value, &entry.Difficulty, &entry.Seconds, &entry.FetchedAt, &entry.scheduleID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if body.Metric != "stygian" {
			entry.Difficulty, entry.Seconds = 0, 0
		}
		entries = append(entries, entry)
	}
	err = rows.Err()
	_ = rows.Close()
	return entries, err
}

func rankingEligibleUserIDs(c context.Context, ctx plugin.Context, candidates []string, guard *rankingScanGuard) (map[string]bool, error) {
	seen := map[string]bool{}
	ids := make([]string, 0, len(candidates))
	for _, id := range candidates {
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 {
		return map[string]bool{}, nil
	}
	sort.Strings(ids)
	hash := sha256.New()
	for _, id := range ids {
		_, _ = hash.Write([]byte(id))
		_, _ = hash.Write([]byte{0})
	}
	key := fmt.Sprintf("%x", hash.Sum(nil))
	eligible, err := guard.run(c, key, func(scanContext context.Context) (map[string]bool, error) {
		result := map[string]bool{}
		for start := 0; start < len(ids); start += 100 {
			end := min(start+100, len(ids))
			raw, err := ctx.API().Anonymous().Call(scanContext, "users/show", map[string]any{"userIds": ids[start:end]})
			if err != nil {
				return nil, err
			}
			var users []struct {
				ID          string  `json:"id"`
				Host        *string `json:"host"`
				IsSuspended bool    `json:"isSuspended"`
			}
			if err := json.Unmarshal(raw, &users); err != nil {
				return nil, err
			}
			for _, user := range users {
				if seen[user.ID] && user.Host == nil && !user.IsSuspended {
					result[user.ID] = true
				}
			}
		}
		return result, nil
	})
	if errors.Is(err, errRankingScanBusy) {
		return nil, plugin.Errorf(429, "ランキングの集計が混み合っています。しばらく待ってから再試行してください")
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return nil, plugin.Errorf(503, "ランキングのユーザー状態確認がタイムアウトしました")
	}
	return eligible, err
}

func visibleRanking(body rankingRequest, entries []rankingEntry, eligible map[string]bool) map[string]any {
	visible := []rankingEntry{}
	seen, rank := 0, 0
	previousValue, previousSeconds := -1, -1
	hasMore := false
	for _, entry := range entries {
		if !eligible[entry.UserID] {
			continue
		}
		if body.Metric == "stygian" {
			if body.ScheduleID == 0 {
				body.ScheduleID = entry.scheduleID
			}
			if entry.scheduleID != body.ScheduleID {
				continue
			}
		}
		seen++
		if seen == 1 || entry.Value != previousValue || entry.Seconds != previousSeconds {
			rank = seen
		}
		previousValue, previousSeconds = entry.Value, entry.Seconds
		entry.Rank = rank
		if body.AccountID != "" && entry.AccountID != body.AccountID {
			continue
		}
		if seen <= body.Offset {
			continue
		}
		if len(visible) == body.Limit {
			hasMore = true
			break
		}
		visible = append(visible, entry)
	}
	return map[string]any{"metric": body.Metric, "scheduleId": body.ScheduleID, "entries": visible, "limit": body.Limit, "offset": body.Offset, "hasMore": hasMore}
}
