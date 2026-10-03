// Package genshin shows a user's Genshin Impact profile (nickname / Adventure
// Rank) on their Misskey profile.
//
// データ元は Enka.Network (https://enka.network/)。認証不要の公開 API だが、
// ttl に従ったキャッシュを求められているのでそれに従う。
package genshin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"time"

	"github.com/shiroha-a/mk/plugin"
	"github.com/shiroha-a/mk/plugin/peercache"
)

// Plugin is the entry point referenced by the generated registration code.
var Plugin = plugin.Definition{
	Name:       "genshin",
	Version:    "0.2.0",
	APIVersion: plugin.APIVersion,
	Migrations: append(append(migrations, peerCacheMigration...), linkingMigration, privacyMigration),
	Routes:     routes,
	Jobs:       jobs,
	// 同じプラグインを入れた mk-go 同士で、リモート利用者の戦績を取り寄せる。
	// ActivityPub には出ない経路 (mk-go #2537)。
	Peered: true,
	// **登録はここ (mk-go #2819)。** Routes の中でやると、ロールを分割した
	// 構成で応答が届かない (送信の POST は queue ロールで走る)。
	Peer: peer,
}

// settings mirrors the `plugins.genshin` section of the instance config.
type settings struct {
	// Endpoint is the Enka.Network base URL. テストで差し替えられるように
	// 設定にしている。
	Endpoint string `json:"endpoint"`
	// UserAgent identifies this instance to Enka.Network. 向こうが
	// 「追跡できるように付けてほしい」と明示しているので既定でも名乗る。
	UserAgent string `json:"userAgent"`
	// TimeoutSeconds bounds one upstream request.
	TimeoutSeconds int `json:"timeoutSeconds"`
	// Language selects the slice of Enka's localisation files to use for
	// character / weapon / artifact names.
	Language string `json:"language"`
}

func loadSettings(ctx plugin.Context) (settings, error) {
	s := settings{
		Endpoint:       "https://enka.network",
		UserAgent:      "mk-go-plugin-genshin/0.2.0 (+https://github.com/shiroha-a/mk)",
		TimeoutSeconds: 10,
		Language:       "ja",
	}
	if err := ctx.Config().Unmarshal(&s); err != nil {
		return s, err
	}
	return s, nil
}

// peerCacheMigration replaces the hand-written remote cache with
// plugin/peercache (mk-go #2820)。**中身はキャッシュなので捨ててよい。**
var peerCacheMigration = append([]plugin.Migration{{
	Version: 6,
	SQL: `
		DROP TABLE IF EXISTS remote_snapshots;
		DROP TABLE IF EXISTS remote_pending;
	`,
}}, peercache.Migrations(7)...)

// peer registers both directions of the plugin channel.
func peer(ctx plugin.Context, p plugin.Peer) error {
	set, err := loadSettings(ctx)
	if err != nil {
		return err
	}
	return registerPeer(ctx, p, ctx.Storage().DB(), newEnkaClient(set))
}

var migrations = []plugin.Migration{
	{Version: 5, SQL: `
		CREATE TABLE remote_pending (
			id         text PRIMARY KEY,
			host       text NOT NULL,
			username   text NOT NULL,
			created_at timestamptz NOT NULL DEFAULT now()
		)
	`},
	{Version: 4, SQL: `
		CREATE TABLE remote_snapshots (
			host       text NOT NULL,
			username   text NOT NULL,
			payload    jsonb NOT NULL,
			fetched_at timestamptz NOT NULL DEFAULT now(),
			expires_at timestamptz NOT NULL,
			PRIMARY KEY (host, username)
		)
	`},
	{Version: 3, SQL: `
		ALTER TABLE snapshots
			ADD COLUMN tower_star   int   NOT NULL DEFAULT 0,
			ADD COLUMN theater_act  int   NOT NULL DEFAULT 0,
			ADD COLUMN theater_mode int   NOT NULL DEFAULT 0,
			ADD COLUMN theater_star int   NOT NULL DEFAULT 0,
			ADD COLUMN fetter_count int   NOT NULL DEFAULT 0,
			ADD COLUMN characters   jsonb NOT NULL DEFAULT '[]'
	`},
	{Version: 2, SQL: `
		ALTER TABLE snapshots
			ADD COLUMN name_card_id  int  NOT NULL DEFAULT 0,
			ADD COLUMN region        text NOT NULL DEFAULT '',
			ADD COLUMN achievements  int  NOT NULL DEFAULT 0,
			ADD COLUMN tower_floor   int  NOT NULL DEFAULT 0,
			ADD COLUMN tower_level   int  NOT NULL DEFAULT 0,
			ADD COLUMN profile_icon  text NOT NULL DEFAULT '',
			ADD COLUMN showcase      jsonb NOT NULL DEFAULT '[]'
	`},
	{Version: 1, SQL: `
		CREATE TABLE accounts (
			user_id    text PRIMARY KEY,
			uid        text NOT NULL,
			updated_at timestamptz NOT NULL DEFAULT now()
		);
		CREATE TABLE snapshots (
			uid         text PRIMARY KEY,
			nickname    text NOT NULL,
			level       int  NOT NULL,
			world_level int  NOT NULL,
			signature   text NOT NULL,
			fetched_at  timestamptz NOT NULL DEFAULT now(),
			expires_at  timestamptz NOT NULL
		);
	`},
}

// uidPattern matches a Genshin UID. 9 桁が基本だが、サーバーによって 10 桁も
// あるので幅を持たせる。形式が違うものは upstream に投げる前に弾く
// (向こうのレート制限を無駄に消費しない)。
var uidPattern = regexp.MustCompile(`^[1-9][0-9]{8,9}$`)

func routes(ctx plugin.Context, r plugin.Router) error {
	return routesWithRankingScanInterval(ctx, r, time.Second)
}

func routesWithRankingScanInterval(ctx plugin.Context, r plugin.Router, interval time.Duration) error {
	set, err := loadSettings(ctx)
	if err != nil {
		return err
	}
	db := ctx.Storage().DB()
	client := newEnkaClient(set)

	// frontend から呼ぶものは POST にする。misskeyApi (= host.api) が POST
	// 固定で、Misskey 本体の API も POST 基本なのでそれに倣う。

	registerLinkRoutes(ctx, r, db, client)
	rankingGuard := newRankingScanGuard()
	rankingGuard.minInterval = interval
	registerPrivacyRoutes(ctx, r, db, rankingGuard)
	r.POST("/profiles", func(req plugin.Request) (any, error) {
		var body struct {
			UserID string `json:"userId"`
		}
		if req.Bind(&body) != nil || body.UserID == "" {
			return nil, plugin.Errorf(400, "userIdが必要です")
		}
		rows, err := db.QueryContext(req.Context(), `SELECT uid FROM accounts WHERE user_id=$1 ORDER BY updated_at, uid`, body.UserID)
		if err != nil {
			return nil, err
		}
		uids := []string{}
		for rows.Next() {
			var uid string
			if err := rows.Scan(&uid); err != nil {
				_ = rows.Close()
				return nil, err
			}
			uids = append(uids, uid)
		}
		err = rows.Err()
		_ = rows.Close()
		if err != nil {
			return nil, err
		}
		profiles := []map[string]any{}
		for _, uid := range uids {
			p, err := buildProfile(req.Context(), db, client, body.UserID, uid)
			if err != nil {
				return nil, err
			}
			if p != nil {
				profiles = append(profiles, p)
			}
		}
		if len(uids) == 0 {
			p, err := remoteLookup(req.Context(), ctx, db, req.UserID(), body.UserID)
			if err != nil {
				return nil, err
			}
			if payload, ok := p.(map[string]any); ok && payload["linked"] == true {
				profiles = append(profiles, payload)
			}
		}
		return map[string]any{"profiles": profiles}, nil
	})

	r.POST("/profile", func(req plugin.Request) (any, error) {
		var body struct {
			UserID string `json:"userId"`
		}
		if err := req.Bind(&body); err != nil || body.UserID == "" {
			return nil, plugin.Errorf(http.StatusBadRequest, "userId が必要です")
		}

		// まず自分のところの利用者として引く。
		profile, err := buildProfile(req.Context(), db, client, body.UserID)
		if err != nil {
			return nil, err
		}
		if profile != nil {
			return profile, nil
		}

		// 見つからなければリモート利用者かもしれない。相手のインスタンスに
		// 取り寄せを頼む (mk-go #2537 の peer channel、AP には出ない)。
		return remoteLookup(req.Context(), ctx, db, req.UserID(), body.UserID)
	})

	// 画像プロキシ。本体の CSP は `img-src 'self'` なので、外部の画像を
	// <img> で直接読めない。同一オリジンで配信することで CSP を緩めずに済む。
	//
	// **GET にする。** ブラウザの <img> は GET しか出さない。
	r.GET("/asset/:name", func(req plugin.Request) (any, error) {
		body, ct, err := fetchAsset(req.Context(), client.http, set.UserAgent, req.Param("name"))
		if err != nil {
			var ue *upstreamError
			if errors.As(err, &ue) && ue.status == http.StatusBadRequest {
				return nil, plugin.Errorf(http.StatusBadRequest, "asset 名が不正です")
			}
			return nil, plugin.ErrNotFound("asset が見つかりません")
		}
		return plugin.Blob{
			ContentType: ct,
			Body:        body,
			// 静的アセットなので長めに持たせる。取得元の負荷も減る。
			CacheControl: "public, max-age=86400, immutable",
		}, nil
	})

	return nil
}

func jobs(ctx plugin.Context, j plugin.Jobs) error {
	set, err := loadSettings(ctx)
	if err != nil {
		return err
	}
	db := ctx.Storage().DB()
	client := newEnkaClient(set)

	j.Handle("refresh", func(c context.Context, _ json.RawMessage) error {
		return refreshExpired(c, ctx, db, client)
	})
	j.Schedule("*/10 * * * *", "refresh", nil)
	return nil
}

// refreshExpired re-fetches snapshots whose ttl has run out.
//
// **上流が落ちていても古いデータは消さない。** 原神のデータが取れないせいで
// プロフィール表示が空になる方が困る (実際 Enka は upstream 不調で 424 を返す
// ことがある)。
func refreshExpired(c context.Context, ctx plugin.Context, db *sql.DB, client *enkaClient) error {
	rows, err := db.QueryContext(c, `
		SELECT DISTINCT a.uid FROM accounts a
		LEFT JOIN snapshots s ON s.uid = a.uid
		WHERE s.uid IS NULL OR s.expires_at <= now()
		LIMIT 50
	`)
	if err != nil {
		return err
	}
	var uids []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			_ = rows.Close()
			return err
		}
		uids = append(uids, uid)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return err
	}
	_ = rows.Close()

	for _, uid := range uids {
		if err := refreshUID(c, db, client, uid); err != nil {
			// 1件の失敗で全体を止めない。次回の実行で再試行する。
			ctx.Logger().Warn("更新に失敗しました", "uid", uid, "err", err)
		}
	}
	return nil
}

// The job and ownership verification share a UID lock and ttl. A verifier
// must not be followed by another fetch from a job selected before it finished.
func refreshUID(c context.Context, db *sql.DB, client *enkaClient, uid string) error {
	tx, err := db.BeginTx(c, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 48128))`, uid); err != nil {
		return err
	}
	var fresh bool
	if err = tx.QueryRowContext(c, `SELECT EXISTS(SELECT 1 FROM snapshots WHERE uid=$1 AND expires_at>clock_timestamp())`, uid).Scan(&fresh); err != nil {
		return err
	}
	if fresh {
		return nil
	}
	snap, err := client.fetch(c, uid)
	if err != nil {
		return err
	}
	if err = saveSnapshot(c, tx, snap); err != nil {
		return err
	}
	return tx.Commit()
}

type snapshotWriter interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}

func saveSnapshot(c context.Context, db snapshotWriter, s *snapshot) error {
	showcase, err := json.Marshal(s.showcase)
	if err != nil {
		return err
	}
	if s.showcase == nil {
		showcase = []byte("[]")
	}
	characters, err := json.Marshal(s.characters)
	if err != nil {
		return err
	}
	if s.characters == nil {
		characters = []byte("[]")
	}
	_, err = db.ExecContext(c, `
		INSERT INTO snapshots (
			uid, nickname, level, world_level, signature, fetched_at, expires_at,
			name_card_id, region, achievements, tower_floor, tower_level, profile_icon, showcase,
			tower_star, theater_act, theater_mode, theater_star, fetter_count, characters,
			stygian_id, stygian_difficulty, stygian_seconds)
		VALUES ($1, $2, $3, $4, $5, now(), now() + make_interval(secs => $6),
			$7, $8, $9, $10, $11, $12, $13, $14, $15, $16, $17, $18, $19, $20, $21, $22)
		ON CONFLICT (uid) DO UPDATE SET
			nickname = EXCLUDED.nickname, level = EXCLUDED.level,
			world_level = EXCLUDED.world_level, signature = EXCLUDED.signature,
			fetched_at = EXCLUDED.fetched_at, expires_at = EXCLUDED.expires_at,
			name_card_id = EXCLUDED.name_card_id, region = EXCLUDED.region,
			achievements = EXCLUDED.achievements, tower_floor = EXCLUDED.tower_floor,
			tower_level = EXCLUDED.tower_level, profile_icon = EXCLUDED.profile_icon,
			showcase = EXCLUDED.showcase,
			tower_star = EXCLUDED.tower_star, theater_act = EXCLUDED.theater_act,
			theater_mode = EXCLUDED.theater_mode, theater_star = EXCLUDED.theater_star,
			fetter_count = EXCLUDED.fetter_count, characters = EXCLUDED.characters,
			stygian_id = EXCLUDED.stygian_id, stygian_difficulty = EXCLUDED.stygian_difficulty,
			stygian_seconds = EXCLUDED.stygian_seconds
	`, s.uid, s.nickname, s.level, s.worldLevel, s.signature, s.ttl,
		s.nameCardID, s.region, s.achievements, s.towerFloor, s.towerLevel,
		s.profileIcon, showcase,
		s.towerStar, s.theaterAct, s.theaterMode, s.theaterStar, s.fetterCount, characters,
		s.stygianID, s.stygianDifficulty, s.stygianSeconds)
	return err
}

// --- Enka.Network ---

type snapshot struct {
	uid               string
	nickname          string
	level             int
	worldLevel        int
	signature         string
	nameCardID        int
	region            string
	achievements      int
	towerFloor        int
	towerLevel        int
	towerStar         int
	theaterAct        int
	theaterMode       int
	theaterStar       int
	fetterCount       int
	stygianID         int
	stygianDifficulty int
	stygianSeconds    int
	// profileIcon は保存する形そのもの。新形式は `pfp:<id>`、旧形式は
	// キャラ id の 10 進表記。**どちらの id 空間かを区別するため**に接頭辞を
	// 付ける (数値だけだと引く先を間違える)。
	profileIcon string
	showcase    []showcaseEntry
	// characters holds the full build of each showcased character.
	characters []character
	ttl        int
}

// showcaseEntry is one character in the player's showcase.
type showcaseEntry struct {
	AvatarID int    `json:"avatarId"`
	Level    int    `json:"level"`
	Icon     string `json:"icon"`
	Element  string `json:"element"`
}

type upstreamError struct {
	status int
	// userFacing is non-empty when the failure is the user's fault and should
	// be shown to them (invalid UID / no such player).
	userFacing string
	msg        string
}

func (e *upstreamError) Error() string { return e.msg }

type enkaClient struct {
	set       settings
	http      *http.Client
	chars     *characterStore
	namecards *characterStore
	// pfps はプロフィール画像の id -> アイコン。キャラ id とは別の id 空間。
	pfps *characterStore
	// texts resolves name hashes (キャラ / 武器 / 聖遺物セット)。
	texts *textStore
	// uiTexts resolves UI keys like FIGHT_PROP_CRITICAL.
	uiTexts *textStore
	// relicSets covers artifact sets whose name hash is missing from loc.json.
	relicSets *relicSetStore
}

// newEnkaClient wires the client and its master-data stores.
func newEnkaClient(set settings) *enkaClient {
	hc := &http.Client{Timeout: time.Duration(set.TimeoutSeconds) * time.Second}
	return &enkaClient{
		set: set, http: hc,
		chars:     newCharacterStore(hc),
		namecards: newNamecardStore(hc),
		pfps:      newPfpStore(hc),
		// **取得元は 1 つで足りる。** 旧形式ではキャラ名 (loc.json) と
		// ステータス名 (gi/locs.json) が別ファイルだったが、新形式は
		// gi/locs.json に統合されている。
		texts:     newTextStore(hc, locsURL, set.Language),
		uiTexts:   newTextStore(hc, locsURL, set.Language),
		relicSets: newRelicSetStore(hc),
	}
}

// fetch retrieves the full profile for a UID.
//
// `?info` を付けると playerInfo だけになって軽いが、ショーケースのビルド
// (avatarInfoList) が落ちる。表示する以上は取る。**ttl を必ず守る**ことで
// レート制限に配慮する (取得元が明示している要求事項)。
func (c *enkaClient) fetch(ctx context.Context, uid string) (*snapshot, error) {
	url := c.set.Endpoint + "/api/uid/" + uid
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.set.UserAgent)

	res, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("enka への接続に失敗しました: %w", err)
	}
	defer res.Body.Close() //nolint:errcheck // 読み捨て

	switch res.StatusCode {
	case http.StatusOK:
	case http.StatusBadRequest:
		return nil, &upstreamError{status: http.StatusBadRequest,
			userFacing: "UID の形式が正しくありません", msg: "enka: 400"}
	case http.StatusNotFound:
		return nil, &upstreamError{status: http.StatusNotFound,
			userFacing: "その UID のプレイヤーが見つかりません", msg: "enka: 404"}
	default:
		// 429 (レート制限) / 424 (ゲーム側に届かない) / 5xx。いずれも
		// こちらの都合ではないので、利用者には見せずキャッシュで凌ぐ。
		return nil, &upstreamError{status: res.StatusCode,
			msg: fmt.Sprintf("enka: status %d", res.StatusCode)}
	}

	// avatarInfoList を含めると 1 件で数百 KB になる。上限は残しつつ広げる。
	body, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return nil, err
	}
	var parsed struct {
		PlayerInfo struct {
			Nickname             string `json:"nickname"`
			Level                int    `json:"level"`
			WorldLevel           int    `json:"worldLevel"`
			Signature            string `json:"signature"`
			NameCardID           int    `json:"nameCardId"`
			FinishAchievementNum int    `json:"finishAchievementNum"`
			TowerFloorIndex      int    `json:"towerFloorIndex"`
			TowerLevelIndex      int    `json:"towerLevelIndex"`
			TowerStarIndex       int    `json:"towerStarIndex"`
			TheaterActIndex      int    `json:"theaterActIndex"`
			TheaterModeIndex     int    `json:"theaterModeIndex"`
			TheaterStarIndex     int    `json:"theaterStarIndex"`
			FetterCount          int    `json:"fetterCount"`
			StygianID            int    `json:"stygianId"`
			StygianIndex         int    `json:"stygianIndex"`
			StygianSeconds       int    `json:"stygianSeconds"`
			ProfilePicture       struct {
				// AvatarID は旧形式 (キャラ id)。
				AvatarID int `json:"avatarId"`
				// ID は現行形式 (プロフィール画像専用の id 空間)。
				ID int `json:"id"`
			} `json:"profilePicture"`
			ShowAvatarInfoList []struct {
				AvatarID int `json:"avatarId"`
				Level    int `json:"level"`
			} `json:"showAvatarInfoList"`
		} `json:"playerInfo"`
		AvatarInfoList []rawAvatar `json:"avatarInfoList"`
		Region         string      `json:"region"`
		TTL            int         `json:"ttl"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil, fmt.Errorf("enka の応答を解釈できません: %w", err)
	}
	ttl := parsed.TTL
	if ttl <= 0 {
		// ttl が無い応答でも、間を置かずに再取得しない。
		ttl = 300
	}
	pi := parsed.PlayerInfo
	snap := &snapshot{
		uid: uid, nickname: pi.Nickname, level: pi.Level, worldLevel: pi.WorldLevel,
		signature: pi.Signature, nameCardID: pi.NameCardID, region: parsed.Region,
		achievements: pi.FinishAchievementNum,
		towerFloor:   pi.TowerFloorIndex, towerLevel: pi.TowerLevelIndex,
		towerStar:   pi.TowerStarIndex,
		theaterAct:  pi.TheaterActIndex,
		theaterMode: pi.TheaterModeIndex,
		theaterStar: pi.TheaterStarIndex,
		fetterCount: pi.FetterCount,
		stygianID:   pi.StygianID, stygianDifficulty: pi.StygianIndex, stygianSeconds: pi.StygianSeconds,
		profileIcon: profileIconKey(pi.ProfilePicture.ID, pi.ProfilePicture.AvatarID), ttl: ttl,
		characters: make([]character, 0, len(parsed.AvatarInfoList)),
	}

	// ショーケースは最大 12 体。取得元が想定外の数を返しても保存が膨らまない
	// ように、showcase と同じくここでも切る。
	for i, a := range parsed.AvatarInfoList {
		if i >= 12 {
			break
		}
		snap.characters = append(snap.characters, c.buildCharacter(ctx, a))
	}

	// **ショーケースのキャラは 12 件までに切る。**
	// 取得元が想定外の数を返したときに保存が膨らむのを防ぐ。
	for i, a := range pi.ShowAvatarInfoList {
		if i >= 12 {
			break
		}
		e := showcaseEntry{AvatarID: a.AvatarID, Level: a.Level}
		if info, ok := c.chars.Lookup(ctx, a.AvatarID, 0); ok {
			e.Icon = info.IconName()
			e.Element = info.Element
		}
		snap.showcase = append(snap.showcase, e)
	}
	return snap, nil
}
