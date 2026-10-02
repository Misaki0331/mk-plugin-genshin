package genshin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shiroha-a/mk/plugin/plugintest"
)

func TestPrivacyAppliesToEveryPublicProfileAndRanking(t *testing.T) {
	f := newVerificationFixture(t, 2)
	p := f.begin(t, "u1", "800000101")
	if _, err := f.verify(t, "u1", p); err != nil {
		t.Fatal(err)
	}
	_, err := f.db.Exec(`UPDATE snapshots SET signature='private signature', achievements=1200, tower_star=36, fetter_count=50,
		stygian_id=202610,stygian_difficulty=6,stygian_seconds=100 WHERE uid='800000101'`)
	if err != nil {
		t.Fatal(err)
	}
	res, err := f.h.Call(t, "POST /me/preferences", plugintest.Request{UserID: "u1"})
	if err != nil || res.(preferences) != (preferences{true, true, true}) {
		t.Fatalf("default: %v %v", res, err)
	}
	_, err = f.h.Call(t, "POST /me/preferences/update", plugintest.Request{UserID: "u1", Body: `{"publishUid":false,"publishSignature":false,"rankingEnabled":true}`})
	if err != nil {
		t.Fatal(err)
	}
	for _, route := range []string{"POST /profile", "POST /profiles"} {
		res, err := f.h.Call(t, route, plugintest.Request{Body: `{"userId":"u1"}`})
		if err != nil {
			t.Fatal(err)
		}
		raw, _ := json.Marshal(res)
		if strings.Contains(string(raw), "800000101") || strings.Contains(string(raw), "private signature") {
			t.Fatalf("private data in %s: %s", route, raw)
		}
	}
	for _, metric := range []string{"spiral", "achievements", "friendship", "stygian"} {
		res, err := f.h.Call(t, "POST /rankings", plugintest.Request{Body: `{"metric":"` + metric + `"}`})
		if err != nil {
			t.Fatal(err)
		}
		entries := res.(map[string]any)["entries"].([]rankingEntry)
		if len(entries) != 1 || entries[0].UID != "" || entries[0].AccountID == "" {
			t.Fatalf("hidden UID ranking: %+v", entries)
		}
	}
	_, err = f.h.Call(t, "POST /me/preferences/update", plugintest.Request{UserID: "u1", Body: `{"publishUid":false,"publishSignature":false,"rankingEnabled":false}`})
	if err != nil {
		t.Fatal(err)
	}
	res, err = f.h.Call(t, "POST /rankings", plugintest.Request{Body: `{"metric":"achievements"}`})
	if err != nil || len(res.(map[string]any)["entries"].([]rankingEntry)) != 0 {
		t.Fatalf("opt out: %v %v", res, err)
	}
	// preferences survive unlink/relink; refresh never resets them.
	if p, err := loadPreferences(context.Background(), f.db, "u1"); err != nil || p.RankingEnabled {
		t.Fatalf("preferences: %+v %v", p, err)
	}
}

func TestPreferencesRequireAuthenticationAndCompleteBooleanValues(t *testing.T) {
	f := newVerificationFixture(t, 1)
	for _, req := range []plugintest.Request{
		{Body: `{"publishUid":false,"publishSignature":false,"rankingEnabled":false}`},
		{UserID: "u1", Body: `{"publishUid":false}`},
		{UserID: "u1", Body: `{"publishUid":"false","publishSignature":true,"rankingEnabled":true}`},
	} {
		if _, err := f.h.Call(t, "POST /me/preferences/update", req); err == nil {
			t.Fatal("invalid preference update accepted")
		}
	}
}

func TestStygianRankingsDoNotMixSchedulesAndSortByDifficultyThenTime(t *testing.T) {
	f := newVerificationFixture(t, 1)
	for _, entry := range []struct {
		user, uid                     string
		schedule, difficulty, seconds int
	}{
		{"u1", "800000201", 100, 6, 120},
		{"u2", "800000202", 100, 6, 90},
		{"u3", "800000203", 100, 5, 60},
		{"u4", "800000204", 99, 6, 40},
		{"u5", "800000205", 100, 6, 0},
	} {
		p := f.begin(t, entry.user, entry.uid)
		if _, err := f.verify(t, entry.user, p); err != nil {
			t.Fatal(err)
		}
		if _, err := f.db.Exec(`UPDATE snapshots SET stygian_id=$2,stygian_difficulty=$3,stygian_seconds=$4 WHERE uid=$1`, entry.uid, entry.schedule, entry.difficulty, entry.seconds); err != nil {
			t.Fatal(err)
		}
	}
	res, err := f.h.Call(t, "POST /rankings", plugintest.Request{Body: `{"metric":"stygian"}`})
	if err != nil {
		t.Fatal(err)
	}
	result := res.(map[string]any)
	entries := result["entries"].([]rankingEntry)
	if result["scheduleId"] != 100 || len(entries) != 3 || entries[0].UserID != "u2" || entries[1].UserID != "u1" || entries[2].UserID != "u3" {
		t.Fatalf("wrong ranking: %+v", result)
	}
	for _, body := range []string{`{"metric":"unknown"}`, `{"metric":"spiral","limit":101}`, `{"metric":"spiral","offset":-1}`} {
		if _, err := f.h.Call(t, "POST /rankings", plugintest.Request{Body: body}); err == nil {
			t.Fatalf("invalid ranking accepted: %s", body)
		}
	}
}
