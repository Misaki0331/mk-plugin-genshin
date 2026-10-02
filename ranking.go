package genshin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"time"

	"github.com/shiroha-a/mk/plugin"
)

const maxRankingCandidates = 10000

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

func rankingResponse(c context.Context, ctx plugin.Context, db *sql.DB, req plugin.Request) (any, error) {
	var body struct {
		Metric     string `json:"metric"`
		ScheduleID int    `json:"scheduleId"`
		Limit      int    `json:"limit"`
		Offset     int    `json:"offset"`
		AccountID  string `json:"accountId"`
	}
	if req.Bind(&body) != nil || body.ScheduleID < 0 || body.Offset < 0 || body.Offset > maxRankingCandidates || body.Limit < 0 || body.Limit > 100 || len(body.AccountID) > 128 || (body.AccountID != "" && body.Offset != 0) {
		return nil, plugin.Errorf(400, "ランキングの条件が不正です")
	}
	if body.Limit == 0 {
		body.Limit = 50
	}
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
	default:
		return nil, plugin.Errorf(400, "ランキングの種類が不正です")
	}
	if ctx.API() == nil {
		return nil, plugin.Errorf(503, "ランキングのユーザー情報を確認できません")
	}
	order := "s." + column + " DESC"
	filter := "AND $1::int>=0"
	if body.Metric == "stygian" {
		order = "s.stygian_id DESC, " + order + ", s.stygian_seconds ASC"
		filter = "AND ($1=0 OR s.stygian_id=$1) AND s.stygian_id>0 AND s.stygian_difficulty>0 AND s.stygian_seconds>0"
	}
	// 候補は1つのSQL文で取得する。refresh が途中で値を変えても、PostgreSQLの
	// statement snapshot により順序と集合が一貫する。OFFSET の分割取得はしない。
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
		var e rankingEntry
		if err := rows.Scan(&e.UserID, &e.AccountID, &e.UID, &e.Nickname, &e.Value, &e.Difficulty, &e.Seconds, &e.FetchedAt, &e.scheduleID); err != nil {
			_ = rows.Close()
			return nil, err
		}
		if body.Metric != "stygian" {
			e.Difficulty, e.Seconds = 0, 0
		}
		entries = append(entries, e)
	}
	err = rows.Err()
	_ = rows.Close()
	if err != nil {
		return nil, err
	}
	// 部分的な順位を返さず、上限超過では明示的に失敗する。APIへの要求数も
	// 最大100回（100人/バッチ）に制限する。削除・凍結は匿名APIが除外する。
	if len(entries) > maxRankingCandidates {
		return nil, plugin.Errorf(503, "ランキングの集計対象が上限を超えています")
	}
	ids := []string{}
	seenIDs := map[string]bool{}
	for _, e := range entries {
		if !seenIDs[e.UserID] {
			ids = append(ids, e.UserID)
			seenIDs[e.UserID] = true
		}
	}
	eligible := map[string]bool{}
	for start := 0; start < len(ids); start += 100 {
		end := min(start+100, len(ids))
		raw, err := ctx.API().Anonymous().Call(c, "users/show", map[string]any{"userIds": ids[start:end]})
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
			if seenIDs[user.ID] && user.Host == nil && !user.IsSuspended {
				eligible[user.ID] = true
			}
		}
	}
	visible := []rankingEntry{}
	seen, rank := 0, 0
	previousValue, previousSeconds := -1, -1
	hasMore := false
	for _, e := range entries {
		if !eligible[e.UserID] {
			continue
		}
		if body.Metric == "stygian" {
			if body.ScheduleID == 0 {
				body.ScheduleID = e.scheduleID
			}
			if e.scheduleID != body.ScheduleID {
				continue
			}
		}
		seen++
		if seen == 1 || e.Value != previousValue || e.Seconds != previousSeconds {
			rank = seen
		}
		previousValue, previousSeconds = e.Value, e.Seconds
		e.Rank = rank
		// 順位は全対象から計算する。プロフィールの指定UID以外を返さない。
		if body.AccountID != "" && e.AccountID != body.AccountID {
			continue
		}
		if seen <= body.Offset {
			continue
		}
		if len(visible) == body.Limit {
			hasMore = true
			break
		}
		visible = append(visible, e)
	}
	return map[string]any{"metric": body.Metric, "scheduleId": body.ScheduleID, "entries": visible, "limit": body.Limit, "offset": body.Offset, "hasMore": hasMore}, nil
}
