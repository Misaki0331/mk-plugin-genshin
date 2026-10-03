package genshin

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"math"
	"math/big"
	"strings"
	"time"

	"github.com/shiroha-a/mk/plugin"
)

const challengeLifetime = 10 * time.Minute
const maxVerificationAttempts = 10
const linkCodeSymbols = "-+!?@#%&=_"

// Legacy registrations are preserved, but are not ownership proof. They must
// not reserve a UID or appear publicly until the user completes verification.
var linkingMigration = plugin.Migration{Version: 8, SQL: `
	ALTER TABLE accounts RENAME TO accounts_unverified;
	CREATE TABLE accounts (
		user_id text NOT NULL,
		uid text NOT NULL UNIQUE,
		updated_at timestamptz NOT NULL DEFAULT now(),
		PRIMARY KEY (user_id, uid)
	);
	CREATE TABLE link_challenges (
		user_id text PRIMARY KEY,
		uid text NOT NULL,
		code text NOT NULL,
		expires_at timestamptz NOT NULL,
		issued_at timestamptz NOT NULL DEFAULT now(),
		next_check_at timestamptz NOT NULL DEFAULT now(),
		attempts int NOT NULL DEFAULT 0
	);
	CREATE TABLE verification_cache (
		uid text PRIMARY KEY,
		signature text NOT NULL,
		expires_at timestamptz NOT NULL
	);
`}

type challenge struct {
	UID         string    `json:"uid"`
	Code        string    `json:"code"`
	ExpiresAt   time.Time `json:"expiresAt"`
	NextCheckAt time.Time `json:"nextCheckAt"`
	Attempts    int       `json:"attempts"`
}

func newLinkCode() (string, error) {
	const alphabet = "0123456789" + linkCodeSymbols
	for {
		var code [6]byte
		hasSymbol := false
		for i := range code {
			n, err := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
			if err != nil {
				return "", err
			}
			code[i] = alphabet[n.Int64()]
			hasSymbol = hasSymbol || n.Int64() >= 10
		}
		// Reject digit-only samples so every valid six-character code is uniform.
		if hasSymbol {
			return string(code[:]), nil
		}
	}
}

// The complete issued code must occur verbatim; surrounding text is allowed.
func signatureHasCode(signature, code string) bool {
	return code != "" && strings.Contains(signature, code)
}

func linkLimit(c context.Context, api plugin.API, userID string) (int, error) {
	raw, err := api.AsUser(userID).Call(c, "i", map[string]any{})
	if err != nil {
		return 0, err
	}
	var me struct {
		Host     *string                    `json:"host"`
		Policies map[string]json.RawMessage `json:"policies"`
	}
	if err := json.Unmarshal(raw, &me); err != nil {
		return 0, err
	}
	if me.Host != nil {
		return 0, plugin.Errorf(403, "ローカルアカウントでログインしてください")
	}
	value, ok := me.Policies["genshinUidLimit"]
	if !ok {
		return 1, nil
	}
	var n float64
	if err := json.Unmarshal(value, &n); err != nil || n < 0 || n >= float64(math.MaxInt) || math.Trunc(n) != n {
		return 0, plugin.Errorf(503, "UID連携上限の設定が不正です")
	}
	return int(n), nil
}

// All mutations of one user's local registrations use this transaction lock.
// UID exclusivity is additionally enforced by the accounts.uid unique index.
func lockLinkUser(c context.Context, db *sql.DB, userID string) (*sql.Tx, error) {
	tx, err := db.BeginTx(c, nil)
	if err != nil {
		return nil, err
	}
	if _, err = tx.ExecContext(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 48127))`, userID); err != nil {
		_ = tx.Rollback()
		return nil, err
	}
	return tx, nil
}

func checkLinkCapacity(c context.Context, tx *sql.Tx, userID, uid string, limit int) error {
	var owner string
	err := tx.QueryRowContext(c, `SELECT user_id FROM accounts WHERE uid=$1`, uid).Scan(&owner)
	if err == nil {
		return plugin.Errorf(409, "このUIDは既に連携されています")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var count int
	if err := tx.QueryRowContext(c, `SELECT count(*) FROM accounts WHERE user_id=$1`, userID).Scan(&count); err != nil {
		return err
	}
	if count >= limit {
		return plugin.Errorf(403, "UID連携数の上限に達しています")
	}
	return nil
}

func registerLinkRoutes(ctx plugin.Context, r plugin.Router, db *sql.DB, client *enkaClient) {
	r.POST("/me", func(req plugin.Request) (any, error) {
		me := req.UserID()
		if me == "" {
			return nil, plugin.Errorf(401, "ログインが必要です")
		}
		limit, err := linkLimit(req.Context(), ctx.API(), me)
		if err != nil {
			return nil, err
		}
		rows, err := db.QueryContext(req.Context(), `SELECT uid FROM accounts WHERE user_id=$1 ORDER BY updated_at, uid`, me)
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
		var pending challenge
		err = db.QueryRowContext(req.Context(), `SELECT uid,code,expires_at,next_check_at,attempts FROM link_challenges WHERE user_id=$1 AND expires_at>clock_timestamp()`, me).
			Scan(&pending.UID, &pending.Code, &pending.ExpiresAt, &pending.NextCheckAt, &pending.Attempts)
		var value any
		if err == nil {
			value = pending
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		return map[string]any{"uids": uids, "limit": limit, "pending": value}, nil
	})
	r.POST("/me/set", func(req plugin.Request) (any, error) {
		if req.UserID() == "" {
			return nil, plugin.Errorf(401, "ログインが必要です")
		}
		return nil, plugin.Errorf(400, "UID連携には紐づけコードによる本人確認が必要です")
	})
	r.POST("/me/begin", func(req plugin.Request) (any, error) {
		me := req.UserID()
		if me == "" {
			return nil, plugin.Errorf(401, "ログインが必要です")
		}
		var body struct {
			UID string `json:"uid"`
		}
		if req.Bind(&body) != nil || !uidPattern.MatchString(body.UID) {
			return nil, plugin.Errorf(400, "UIDの形式が正しくありません")
		}
		tx, err := lockLinkUser(req.Context(), db, me)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		limit, err := linkLimit(req.Context(), ctx.API(), me)
		if err != nil {
			return nil, err
		}
		if err = checkLinkCapacity(req.Context(), tx, me, body.UID, limit); err != nil {
			return nil, err
		}
		var recent bool
		if err = tx.QueryRowContext(req.Context(), `SELECT EXISTS(SELECT 1 FROM link_challenges WHERE user_id=$1 AND issued_at>clock_timestamp()-interval '30 seconds')`, me).Scan(&recent); err != nil {
			return nil, err
		}
		if recent {
			return nil, plugin.Errorf(429, "コードの再発行は30秒待ってから行ってください")
		}
		code, err := newLinkCode()
		if err != nil {
			return nil, err
		}
		var pending challenge
		err = tx.QueryRowContext(req.Context(), `INSERT INTO link_challenges(user_id,uid,code,expires_at)
		 VALUES($1,$2,$3,clock_timestamp()+interval '10 minutes') ON CONFLICT(user_id) DO UPDATE SET
		 uid=EXCLUDED.uid,code=EXCLUDED.code,expires_at=EXCLUDED.expires_at,issued_at=clock_timestamp(),next_check_at=GREATEST(link_challenges.next_check_at,clock_timestamp()),attempts=0
		 RETURNING uid,code,expires_at,next_check_at,attempts`, me, body.UID, code).
			Scan(&pending.UID, &pending.Code, &pending.ExpiresAt, &pending.NextCheckAt, &pending.Attempts)
		if err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return pending, nil
	})
	r.POST("/me/verify", func(req plugin.Request) (any, error) { return verifyLink(req, ctx, db, client) })
	r.POST("/me/unlink", func(req plugin.Request) (any, error) {
		me := req.UserID()
		if me == "" {
			return nil, plugin.Errorf(401, "ログインが必要です")
		}
		var body struct {
			UID string `json:"uid"`
		}
		if req.Bind(&body) != nil || !uidPattern.MatchString(body.UID) {
			return nil, plugin.Errorf(400, "UIDの形式が正しくありません")
		}
		tx, err := lockLinkUser(req.Context(), db, me)
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		if _, err = tx.ExecContext(req.Context(), `DELETE FROM accounts WHERE user_id=$1 AND uid=$2`, me, body.UID); err != nil {
			return nil, err
		}
		return map[string]any{"unlinked": true}, tx.Commit()
	})
}

func verifyLink(req plugin.Request, ctx plugin.Context, db *sql.DB, client *enkaClient) (any, error) {
	me := req.UserID()
	if me == "" {
		return nil, plugin.Errorf(401, "ログインが必要です")
	}
	var body struct {
		Code string `json:"code"`
	}
	if req.Bind(&body) != nil {
		return nil, plugin.Errorf(400, "リクエストを読めません")
	}
	c := req.Context()
	tx, err := lockLinkUser(c, db, me)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var pending challenge
	var now time.Time
	err = tx.QueryRowContext(c, `SELECT uid,code,expires_at,next_check_at,attempts,clock_timestamp() FROM link_challenges WHERE user_id=$1 FOR UPDATE`, me).
		Scan(&pending.UID, &pending.Code, &pending.ExpiresAt, &pending.NextCheckAt, &pending.Attempts, &now)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, plugin.Errorf(400, "紐づけコードを発行してください")
	}
	if err != nil {
		return nil, err
	}
	if body.Code != pending.Code {
		return nil, plugin.Errorf(409, "コードが再発行されています。画面を更新してください")
	}
	if !now.Before(pending.ExpiresAt) {
		return nil, plugin.Errorf(410, "コードの有効期限が切れました。再発行してください")
	}
	if pending.Attempts >= maxVerificationAttempts {
		return nil, plugin.Errorf(429, "確認回数の上限に達しました。コードを再発行してください")
	}
	if now.Before(pending.NextCheckAt) {
		return map[string]any{"verified": false, "nextCheckAt": pending.NextCheckAt, "expiresAt": pending.ExpiresAt}, nil
	}
	limit, err := linkLimit(c, ctx.API(), me)
	if err != nil {
		return nil, err
	}
	if err = checkLinkCapacity(c, tx, me, pending.UID, limit); err != nil {
		return nil, err
	}
	// Serialize Enka verification reads for a UID across users. Neither pending
	// challenges nor peer-cache rows occupy a local registration slot.
	if _, err = tx.ExecContext(c, `SELECT pg_advisory_xact_lock(hashtextextended($1, 48128))`, pending.UID); err != nil {
		return nil, err
	}
	var signature string
	var cacheUntil time.Time
	var fetched *snapshot
	err = tx.QueryRowContext(c, `SELECT signature,expires_at FROM (
	 SELECT signature,expires_at FROM verification_cache WHERE uid=$1
	 UNION ALL SELECT signature,expires_at FROM snapshots WHERE uid=$1
	 ) cached WHERE expires_at>clock_timestamp() ORDER BY expires_at DESC LIMIT 1`, pending.UID).Scan(&signature, &cacheUntil)
	if errors.Is(err, sql.ErrNoRows) {
		fetched, err = client.fetch(c, pending.UID)
		if err == nil {
			signature = fetched.signature
			cacheUntil = time.Now().Add(time.Duration(fetched.ttl) * time.Second)
		}
		if err == nil {
			_, err = tx.ExecContext(c, `INSERT INTO verification_cache(uid,signature,expires_at) VALUES($1,$2,$3) ON CONFLICT(uid) DO UPDATE SET signature=EXCLUDED.signature,expires_at=EXCLUDED.expires_at`, pending.UID, signature, cacheUntil)
		}
	}
	if err != nil {
		// Failed upstream calls also consume a rate-limited attempt. No UID is linked.
		if _, e := tx.ExecContext(c, `UPDATE link_challenges SET attempts=attempts+1,next_check_at=clock_timestamp()+interval '60 seconds' WHERE user_id=$1`, me); e != nil {
			return nil, e
		}
		if e := tx.Commit(); e != nil {
			return nil, e
		}
		return nil, plugin.Errorf(503, "ゲーム情報を取得できませんでした。60秒待って再確認してください")
	}
	if err = tx.QueryRowContext(c, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	if !now.Before(pending.ExpiresAt) {
		return nil, plugin.Errorf(410, "コードの有効期限が切れました。再発行してください")
	}
	if !signatureHasCode(signature, pending.Code) {
		err = tx.QueryRowContext(c, `UPDATE link_challenges SET attempts=attempts+1,next_check_at=GREATEST($2,clock_timestamp()+interval '60 seconds') WHERE user_id=$1 RETURNING next_check_at`, me, cacheUntil).Scan(&cacheUntil)
		if err != nil {
			return nil, err
		}
		if err = tx.Commit(); err != nil {
			return nil, err
		}
		return map[string]any{"verified": false, "nextCheckAt": cacheUntil, "expiresAt": pending.ExpiresAt}, nil
	}
	// Re-read the effective policy at completion, not just challenge issuance.
	limit, err = linkLimit(c, ctx.API(), me)
	if err != nil {
		return nil, err
	}
	if err = checkLinkCapacity(c, tx, me, pending.UID, limit); err != nil {
		return nil, err
	}
	if err = tx.QueryRowContext(c, `SELECT clock_timestamp()`).Scan(&now); err != nil {
		return nil, err
	}
	if !now.Before(pending.ExpiresAt) {
		return nil, plugin.Errorf(410, "コードの有効期限が切れました。再発行してください")
	}
	// A successful upstream lookup is not ownership proof by itself. Keep only
	// its signature and TTL until the complete issued code matches.
	if fetched != nil {
		if err = saveSnapshot(c, tx, fetched); err != nil {
			return nil, err
		}
	}
	result, err := tx.ExecContext(c, `INSERT INTO accounts(user_id,uid) VALUES($1,$2) ON CONFLICT(uid) DO NOTHING`, me, pending.UID)
	if err != nil {
		return nil, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return nil, err
	}
	if n != 1 {
		return nil, plugin.Errorf(409, "このUIDは既に連携されています")
	}
	if _, err = tx.ExecContext(c, `DELETE FROM link_challenges WHERE user_id=$1`, me); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"verified": true, "uid": pending.UID}, nil
}
