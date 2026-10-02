package genshin

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/shiroha-a/mk/plugin/plugintest"
)

func TestPeerOmitsPrivateUIDAndSignature(t *testing.T) {
	db := testDB(t)
	api := &fakeAPI{resp: json.RawMessage(`{"id":"u1","host":null}`)}
	h := plugintest.New(t).WithName("genshin").WithDB(db).WithAPI(api).WithPeers("other.example")
	h.Routes(Plugin)
	h.Peer(Plugin)
	if _, err := db.Exec(`INSERT INTO accounts(user_id,uid) VALUES('u1','800000901');
		INSERT INTO snapshots(uid,nickname,level,world_level,signature,expires_at) VALUES('800000901','Traveler',60,9,'private signature',now()+interval '5 minutes');
		INSERT INTO user_preferences(user_id,publish_uid,publish_signature) VALUES('u1',false,false)`); err != nil {
		t.Fatal(err)
	}
	res, err := h.DeliverPeer("other.example", peerRequest{Username: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	profile := res.(peerResponse)
	if !profile.Linked || strings.Contains(string(profile.Profile), "800000901") || strings.Contains(string(profile.Profile), "private signature") {
		t.Fatalf("private peer response: %s", profile.Profile)
	}
}

func TestFetchStygianFieldsAndSaveSnapshot(t *testing.T) {
	srv := fakeEnka(t, 200, `{"playerInfo":{"nickname":"Traveler","level":60,"stygianId":100,"stygianIndex":6,"stygianSeconds":90},"ttl":300}`)
	snap, err := testClient(srv.URL).fetch(context.Background(), "800000902")
	if err != nil {
		t.Fatal(err)
	}
	if snap.stygianID != 100 || snap.stygianDifficulty != 6 || snap.stygianSeconds != 90 {
		t.Fatalf("stygian fields: %+v", snap)
	}
	db := testDB(t)
	plugintest.New(t).WithName("genshin").WithDB(db).Routes(Plugin)
	if err := saveSnapshot(context.Background(), db, snap); err != nil {
		t.Fatal(err)
	}
	var schedule, difficulty, seconds int
	if err := db.QueryRow(`SELECT stygian_id,stygian_difficulty,stygian_seconds FROM snapshots WHERE uid='800000902'`).Scan(&schedule, &difficulty, &seconds); err != nil {
		t.Fatal(err)
	}
	if schedule != 100 || difficulty != 6 || seconds != 90 {
		t.Fatalf("saved stygian: %d %d %d", schedule, difficulty, seconds)
	}
}
