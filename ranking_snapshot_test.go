package genshin

import (
	"testing"

	"github.com/shiroha-a/mk/plugin/plugintest"
)

func TestRankingCandidateSnapshotSurvivesConcurrentValueChange(t *testing.T) {
	db := testDB(t)
	api := &rankingEligibilityAPI{}
	h := plugintest.New(t).WithName("genshin").WithDB(db).WithAPI(api).Routes(Plugin)
	if _, err := db.Exec(`INSERT INTO accounts(user_id,uid) SELECT 'u'||n,'800'||lpad(n::text,6,'0') FROM generate_series(1,205) n;
		INSERT INTO snapshots(uid,nickname,level,world_level,signature,expires_at,achievements)
		SELECT '800'||lpad(n::text,6,'0'),'Traveler',60,9,'',now()+interval '5 minutes',n FROM generate_series(1,205) n`); err != nil {
		t.Fatal(err)
	}
	api.afterCandidates = func() {
		if _, err := db.Exec(`UPDATE snapshots SET achievements=CASE WHEN uid='800000205' THEN 0 ELSE 1000 END WHERE uid IN ('800000001','800000205')`); err != nil {
			t.Fatal(err)
		}
	}
	res, err := h.Call(t, "POST /rankings", plugintest.Request{Body: `{"metric":"achievements","limit":100}`})
	if err != nil {
		t.Fatal(err)
	}
	entries := res.(map[string]any)["entries"].([]rankingEntry)
	if len(entries) != 100 || entries[0].UserID != "u205" || entries[0].Value != 205 || entries[99].Value != 106 {
		t.Fatalf("ranking changed during retrieval: %+v", entries)
	}
}
