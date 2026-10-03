package genshin

import (
	"context"
	"database/sql"
	"errors"

	"github.com/shiroha-a/mk/plugin"
)

var privacyMigration = plugin.Migration{Version: 9, SQL: `
	ALTER TABLE accounts ADD COLUMN public_id text NOT NULL DEFAULT gen_random_uuid()::text UNIQUE;
	CREATE TABLE user_preferences (
		user_id text PRIMARY KEY,
		publish_uid boolean NOT NULL DEFAULT true,
		publish_signature boolean NOT NULL DEFAULT true,
		ranking_enabled boolean NOT NULL DEFAULT true
	);
	ALTER TABLE snapshots
		ADD COLUMN stygian_id int NOT NULL DEFAULT 0,
		ADD COLUMN stygian_difficulty int NOT NULL DEFAULT 0,
		ADD COLUMN stygian_seconds int NOT NULL DEFAULT 0;
`}

type preferences struct {
	PublishUID       bool `json:"publishUid"`
	PublishSignature bool `json:"publishSignature"`
	RankingEnabled   bool `json:"rankingEnabled"`
}

func loadPreferences(c context.Context, db *sql.DB, userID string) (preferences, error) {
	p := preferences{true, true, true}
	err := db.QueryRowContext(c, `SELECT publish_uid, publish_signature, ranking_enabled FROM user_preferences WHERE user_id=$1`, userID).
		Scan(&p.PublishUID, &p.PublishSignature, &p.RankingEnabled)
	if errors.Is(err, sql.ErrNoRows) {
		err = nil
	}
	return p, err
}

func registerPrivacyRoutes(ctx plugin.Context, r plugin.Router, db *sql.DB, rankingGuard *rankingScanGuard) {
	r.POST("/me/preferences", func(req plugin.Request) (any, error) {
		if req.UserID() == "" {
			return nil, plugin.Errorf(401, "ログインが必要です")
		}
		return loadPreferences(req.Context(), db, req.UserID())
	})
	r.POST("/me/preferences/update", func(req plugin.Request) (any, error) {
		if req.UserID() == "" {
			return nil, plugin.Errorf(401, "ログインが必要です")
		}
		var body struct {
			PublishUID       *bool `json:"publishUid"`
			PublishSignature *bool `json:"publishSignature"`
			RankingEnabled   *bool `json:"rankingEnabled"`
		}
		if req.Bind(&body) != nil || body.PublishUID == nil || body.PublishSignature == nil || body.RankingEnabled == nil {
			return nil, plugin.Errorf(400, "公開設定とランキング参加設定をすべて指定してください")
		}
		if _, err := linkLimit(req.Context(), ctx.API(), req.UserID()); err != nil {
			return nil, err
		}
		p := preferences{*body.PublishUID, *body.PublishSignature, *body.RankingEnabled}
		_, err := db.ExecContext(req.Context(), `INSERT INTO user_preferences (user_id,publish_uid,publish_signature,ranking_enabled)
			VALUES ($1,$2,$3,$4) ON CONFLICT (user_id) DO UPDATE SET publish_uid=$2,publish_signature=$3,ranking_enabled=$4`,
			req.UserID(), p.PublishUID, p.PublishSignature, p.RankingEnabled)
		if err != nil {
			return nil, err
		}
		return p, nil
	})
	r.POST("/rankings", func(req plugin.Request) (any, error) {
		return rankingResponse(req.Context(), ctx, db, req, rankingGuard)
	})
	r.POST("/rankings/profile", func(req plugin.Request) (any, error) {
		return profileRankingResponse(req.Context(), ctx, db, req, rankingGuard)
	})
}
