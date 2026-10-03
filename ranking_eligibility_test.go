package genshin

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiroha-a/mk/plugin"
	"github.com/shiroha-a/mk/plugin/plugintest"
)

type rankingEligibilityAPI struct {
	linkingAPI
	afterCandidates func()
	calls           atomic.Int64
	rejectUserID    string
}

func rankingTestPlugin() plugin.Definition {
	definition := Plugin
	definition.Routes = func(ctx plugin.Context, router plugin.Router) error {
		return routesWithRankingScanInterval(ctx, router, 0)
	}
	return definition
}

func (a *rankingEligibilityAPI) Anonymous() plugin.Caller { return a }
func (a *rankingEligibilityAPI) Call(c context.Context, endpoint string, params any) (json.RawMessage, error) {
	if endpoint == "users/show" {
		a.calls.Add(1)
		if a.afterCandidates != nil {
			hook := a.afterCandidates
			a.afterCandidates = nil
			hook()
		}
		if ids, ok := params.(map[string]any)["userIds"].([]string); ok {
			users := []map[string]any{}
			for _, id := range ids {
				if id == a.rejectUserID {
					return nil, errors.New("ineligible user was scanned")
				}
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

func TestProfileRankingsShareOneEligibilityScan(t *testing.T) {
	db := testDB(t)
	api := &rankingEligibilityAPI{rejectUserID: "opted-out"}
	h := plugintest.New(t).WithName("genshin").WithDB(db).WithAPI(api).Routes(rankingTestPlugin())
	var accountID string
	if err := db.QueryRow(`INSERT INTO accounts(user_id,uid) VALUES('u1','800000001') RETURNING public_id`).Scan(&accountID); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO snapshots(uid,nickname,level,world_level,signature,expires_at,achievements,tower_star,fetter_count,stygian_id,stygian_difficulty,stygian_seconds)
		VALUES('800000001','Traveler',60,9,'',now()+interval '5 minutes',100,36,10,1,6,60)`); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO accounts(user_id,uid) VALUES('opted-out','800000002');
		INSERT INTO snapshots(uid,nickname,level,world_level,signature,expires_at,achievements) VALUES('800000002','Hidden',60,9,'',now()+interval '5 minutes',999);
		INSERT INTO user_preferences(user_id,ranking_enabled) VALUES('opted-out',false)`); err != nil {
		t.Fatal(err)
	}
	body, _ := json.Marshal(map[string]any{"accountId": accountID})
	res, err := h.Call(t, "POST /rankings/profile", plugintest.Request{Body: string(body)})
	if err != nil {
		t.Fatal(err)
	}
	rankings := res.(map[string]any)["rankings"].(map[string]any)
	for _, metric := range []string{"spiral", "achievements", "friendship", "stygian"} {
		entries := rankings[metric].(map[string]any)["entries"].([]rankingEntry)
		if len(entries) != 1 || entries[0].AccountID != accountID || entries[0].Rank != 1 {
			t.Fatalf("%s ranking: %+v", metric, entries)
		}
	}
	if api.calls.Load() != 1 {
		t.Fatalf("profile rankings performed %d eligibility scans, want 1", api.calls.Load())
	}
}

func TestRankingScanGuardBoundsWaitersAndFrequency(t *testing.T) {
	guard := newRankingScanGuard()
	guard.minInterval = 0
	guard.timeout = time.Second
	started := make(chan struct{})
	release := make(chan struct{})
	var scans atomic.Int64
	results := make(chan error, rankingScanRequestLimit)
	scan := func(context.Context) (map[string]bool, error) {
		if scans.Add(1) == 1 {
			close(started)
			<-release
		}
		return map[string]bool{"u1": true}, nil
	}
	go func() {
		_, err := guard.run(context.Background(), "same-users", scan)
		results <- err
	}()
	<-started
	go func() {
		_, err := guard.run(context.Background(), "different-users", scan)
		results <- err
	}()
	for range rankingScanRequestLimit - 2 {
		go func() {
			_, err := guard.run(context.Background(), "same-users", scan)
			results <- err
		}()
	}
	deadline := time.Now().Add(time.Second)
	for len(guard.requests) != rankingScanRequestLimit && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if _, err := guard.run(context.Background(), "same-users", scan); !errors.Is(err, errRankingScanBusy) {
		t.Fatalf("overflow error = %v, want busy", err)
	}
	close(release)
	for range rankingScanRequestLimit {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
	if scans.Load() != 2 {
		t.Fatalf("serialized scans = %d, want 2", scans.Load())
	}

	guard = newRankingScanGuard()
	guard.minInterval = 30 * time.Millisecond
	guard.timeout = time.Second
	quickScan := func(context.Context) (map[string]bool, error) { return nil, nil }
	if _, err := guard.run(context.Background(), "first", quickScan); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	if _, err := guard.run(context.Background(), "second", quickScan); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 20*time.Millisecond {
		t.Fatalf("second scan started after %s, want interval wait", elapsed)
	}
}

func TestRankingScanSurvivesLeaderCancellation(t *testing.T) {
	guard := newRankingScanGuard()
	guard.minInterval = 0
	guard.timeout = time.Second
	started := make(chan struct{})
	release := make(chan struct{})
	scan := func(context.Context) (map[string]bool, error) {
		close(started)
		<-release
		return map[string]bool{"u1": true}, nil
	}
	leaderContext, cancelLeader := context.WithCancel(context.Background())
	leaderResult := make(chan error, 1)
	go func() {
		_, err := guard.run(leaderContext, "same-users", scan)
		leaderResult <- err
	}()
	<-started
	followerResult := make(chan error, 1)
	go func() {
		_, err := guard.run(context.Background(), "same-users", scan)
		followerResult <- err
	}()
	deadline := time.Now().Add(time.Second)
	for len(guard.requests) != 2 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(guard.requests) != 2 {
		t.Fatal("follower did not join the shared scan")
	}
	cancelLeader()
	if err := <-leaderResult; !errors.Is(err, context.Canceled) {
		t.Fatalf("leader error = %v, want canceled", err)
	}
	if len(guard.requests) != 2 {
		t.Fatal("canceled leader released its queue slot before shared work ended")
	}
	close(release)
	if err := <-followerResult; err != nil {
		t.Fatalf("follower error = %v", err)
	}
}

func TestRankingEligibilityPrecedesRankPaginationAndScheduleSelection(t *testing.T) {
	db := testDB(t)
	h := plugintest.New(t).WithName("genshin").WithDB(db).WithAPI(&rankingEligibilityAPI{}).Routes(rankingTestPlugin())
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
