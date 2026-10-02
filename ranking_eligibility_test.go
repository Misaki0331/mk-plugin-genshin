package genshin

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/shiroha-a/mk/plugin"
	"github.com/shiroha-a/mk/plugin/plugintest"
)

type rankingEligibilityAPI struct {
	linkingAPI
	afterCandidates func()
}

func (a *rankingEligibilityAPI) Anonymous() plugin.Caller { return a }
func (a *rankingEligibilityAPI) Call(c context.Context, endpoint string, params any) (json.RawMessage, error) {
	if endpoint == "users/show" {
		if a.afterCandidates != nil {
			hook := a.afterCandidates
			a.afterCandidates = nil
			hook()
		}
		if ids, ok := params.(map[string]any)["userIds"].([]string); ok {
			users := []map[string]any{}
			for _, id := range ids {
				if id == "deleted" {
					continue
				}
				u := map[string]any{"id": id, "host": nil, "isSuspended": id == "suspended"}
				if id == "remote" {
					u["host"] = "remote.example"
				}
				users = append(users, u)
			}
			raw, err := json.Marshal(users)
			return raw, err
		}
		switch params.(map[string]any)["userId"] {
		case "suspended":
			return json.RawMessage(`{"host":null,"isSuspended":true}`), nil
		case "remote":
			return json.RawMessage(`{"host":"remote.example"}`), nil
		case "deleted":
			return nil, &plugin.APIError{Status: 404}
		default:
			return json.RawMessage(`{"host":null,"isSuspended":false}`), nil
		}
	}
	return a.linkingAPI.Call(c, endpoint, params)
}

func TestRankingEligibilityPrecedesRankPaginationAndScheduleSelection(t *testing.T) {
	db := testDB(t)
	h := plugintest.New(t).WithName("genshin").WithDB(db).WithAPI(&rankingEligibilityAPI{}).Routes(Plugin)
	for i, user := range []string{"suspended", "remote", "deleted", "u1", "u2", "u3"} {
		uid := string(rune('a' + i))
		if _, err := db.Exec(`INSERT INTO accounts(user_id,uid) VALUES($1,$2)`, user, uid); err != nil {
			t.Fatal(err)
		}
		schedule := 100
		if i < 3 {
			schedule = 999
		}
		if _, err := db.Exec(`INSERT INTO snapshots(uid,nickname,level,world_level,signature,expires_at,achievements,stygian_id,stygian_difficulty,stygian_seconds)
			VALUES($1,'Traveler',60,9,'',now()+interval '5 minutes',$2,$3,6,$4)`, uid, 100-i, schedule, 60+i); err != nil {
			t.Fatal(err)
		}
	}
	res, err := h.Call(t, "POST /rankings", plugintest.Request{Body: `{"metric":"achievements","limit":2}`})
	if err != nil {
		t.Fatal(err)
	}
	result := res.(map[string]any)
	entries := result["entries"].([]rankingEntry)
	if len(entries) != 2 || entries[0].UserID != "u1" || entries[0].Rank != 1 || entries[1].Rank != 2 || result["hasMore"] != true {
		t.Fatalf("first page: %+v", result)
	}
	res, err = h.Call(t, "POST /rankings", plugintest.Request{Body: `{"metric":"achievements","limit":2,"offset":2}`})
	if err != nil {
		t.Fatal(err)
	}
	result = res.(map[string]any)
	entries = result["entries"].([]rankingEntry)
	if len(entries) != 1 || entries[0].UserID != "u3" || entries[0].Rank != 3 || result["hasMore"] != false {
		t.Fatalf("second page: %+v", result)
	}
	res, err = h.Call(t, "POST /rankings", plugintest.Request{Body: `{"metric":"stygian","limit":2}`})
	if err != nil {
		t.Fatal(err)
	}
	result = res.(map[string]any)
	if result["scheduleId"] != 100 || len(result["entries"].([]rankingEntry)) != 2 {
		t.Fatalf("schedule: %+v", result)
	}
}
