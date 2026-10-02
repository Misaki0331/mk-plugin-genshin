package genshin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"
)

// buildProfile assembles the display payload for a local user.
//
// 未登録なら (nil, nil)。**エラーと区別する** — 「登録していない」は普通の
// 状態で、表示側はそれを見て何も描かない。
func buildProfile(c context.Context, db *sql.DB, client *enkaClient, userID string, selectedUID ...string) (map[string]any, error) {
	filterUID := ""
	if len(selectedUID) > 0 {
		filterUID = selectedUID[0]
	}
	var (
		uid, nickname, signature, region, profileIcon string
		publicID                                      string
		publishUID, publishSignature                  bool
		level, worldLevel, nameCardID                 int
		achievements, towerFloor, towerLevel          int
		towerStar, theaterAct, theaterMode            int
		theaterStar, fetterCount                      int
		showcaseRaw, charactersRaw                    []byte
		fetchedAt                                     time.Time
	)
	err := db.QueryRowContext(c, `
		SELECT a.uid, s.nickname, s.level, s.world_level, s.signature, s.fetched_at,
		       s.name_card_id, s.region, s.achievements, s.tower_floor, s.tower_level,
		       s.profile_icon, s.showcase,
		       s.tower_star, s.theater_act, s.theater_mode, s.theater_star,
		       s.fetter_count, s.characters, a.public_id,
		       COALESCE(p.publish_uid, true), COALESCE(p.publish_signature, true)
		FROM accounts a JOIN snapshots s ON s.uid = a.uid
		LEFT JOIN user_preferences p ON p.user_id = a.user_id
		WHERE a.user_id = $1 AND ($2 = '' OR a.uid = $2)
		ORDER BY a.updated_at, a.uid LIMIT 1
	`, userID, filterUID).Scan(&uid, &nickname, &level, &worldLevel, &signature, &fetchedAt,
		&nameCardID, &region, &achievements, &towerFloor, &towerLevel, &profileIcon, &showcaseRaw,
		&towerStar, &theaterAct, &theaterMode, &theaterStar, &fetterCount, &charactersRaw,
		&publicID, &publishUID, &publishSignature)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}

	var showcase []showcaseEntry
	if len(showcaseRaw) > 0 {
		_ = json.Unmarshal(showcaseRaw, &showcase)
	}
	// 壊れた JSON でカード全体を落とさない。詳細が出ないだけで済ませる。
	characters := []character{}
	if len(charactersRaw) > 0 {
		_ = json.Unmarshal(charactersRaw, &characters)
	}

	// アイコンは**自分のプロキシ経由の URL**として返す。CSP が
	// `img-src 'self'` なので、取得元の URL を渡しても表示できない。
	iconURL := profileIconURL(c, client.pfps, client.chars, profileIcon)
	cards := make([]map[string]any, 0, len(showcase))
	for _, e := range showcase {
		cards = append(cards, map[string]any{
			"avatarId": e.AvatarID,
			"level":    e.Level, "element": e.Element, "icon": assetURL(e.Icon),
		})
	}

	profile := map[string]any{
		"accountId":     publicID,
		"linked":        true,
		"uid":           uid,
		"nickname":      nickname,
		"adventureRank": level,
		"worldLevel":    worldLevel,
		"signature":     signature,
		"region":        region,
		"achievements":  achievements,
		"spiral":        spiralLabel(towerFloor, towerLevel),
		"spiralStars":   towerStar,
		"theater":       theaterLabel(theaterAct, theaterMode),
		"theaterStars":  theaterStar,
		"fetterCount":   fetterCount,
		"characters":    characters,
		"profileIcon":   iconURL,
		"nameCard":      nameCardURL(c, client.namecards, nameCardID),
		"showcase":      cards,
		"fetchedAt":     fetchedAt,
	}
	if !publishUID {
		delete(profile, "uid")
	}
	if !publishSignature {
		delete(profile, "signature")
	}
	return profile, nil
}
