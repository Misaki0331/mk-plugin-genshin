package genshin

import (
	"encoding/json"
	"fmt"
	"testing"

	"github.com/shiroha-a/mk/plugin/plugintest"
)

func TestRankingAccountLookupKeepsGlobalRankAndEligibility(t *testing.T) {
	db := testDB(t)
	h := plugintest.New(t).WithName("genshin").WithDB(db).WithAPI(&rankingEligibilityAPI{}).Routes(Plugin)
	var target string
	for i := range 52 {
		uid := fmt.Sprintf("800000%03d", i)
		user := fmt.Sprintf("u%d", i)
		var accountID string
		if err := db.QueryRow(`INSERT INTO accounts(user_id,uid) VALUES($1,$2) RETURNING public_id`, user, uid).Scan(&accountID); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`INSERT INTO snapshots(uid,nickname,level,world_level,signature,expires_at,achievements)
			VALUES($1,'Traveler',60,9,'',now()+interval '5 minutes',$2)`, uid, 100-i); err != nil {
			t.Fatal(err)
		}
		if i == 51 {
			target = accountID
		}
	}
	query := func() []rankingEntry {
		t.Helper()
		body, _ := json.Marshal(map[string]any{"metric": "achievements", "accountId": target})
		res, err := h.Call(t, "POST /rankings", plugintest.Request{Body: string(body)})
		if err != nil {
			t.Fatal(err)
		}
		result := res.(map[string]any)
		if result["hasMore"] != false {
			t.Fatal("account lookup must not page through other users")
		}
		return result["entries"].([]rankingEntry)
	}
	entries := query()
	if len(entries) != 1 || entries[0].AccountID != target || entries[0].Rank != 52 {
		t.Fatalf("lookup beyond first page: %+v", entries)
	}
	if _, err := db.Exec(`INSERT INTO user_preferences(user_id,publish_uid,publish_signature,ranking_enabled) VALUES('u51',false,false,true)`); err != nil {
		t.Fatal(err)
	}
	entries = query()
	if len(entries) != 1 || entries[0].UID != "" || entries[0].Rank != 52 {
		t.Fatalf("private UID lookup: %+v", entries)
	}
	if _, err := db.Exec(`UPDATE user_preferences SET ranking_enabled=false WHERE user_id='u51'`); err != nil {
		t.Fatal(err)
	}
	if entries = query(); len(entries) != 0 {
		t.Fatalf("opted-out account: %+v", entries)
	}
	if _, err := db.Exec(`DELETE FROM user_preferences WHERE user_id='u51'`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE accounts SET user_id='suspended' WHERE public_id=$1`, target); err != nil {
		t.Fatal(err)
	}
	if entries = query(); len(entries) != 0 {
		t.Fatalf("suspended account: %+v", entries)
	}
}
