package genshin

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/shiroha-a/mk/plugin"
	"github.com/shiroha-a/mk/plugin/plugintest"
)

type linkingAPI struct{ limit atomic.Int64 }

func (a *linkingAPI) Anonymous() plugin.Caller    { return a }
func (a *linkingAPI) AsUser(string) plugin.Caller { return a }
func (a *linkingAPI) Call(_ context.Context, endpoint string, params any) (json.RawMessage, error) {
	if endpoint == "users/show" {
		if ids, ok := params.(map[string]any)["userIds"].([]string); ok {
			users := []map[string]any{}
			for _, id := range ids {
				users = append(users, map[string]any{"id": id, "host": nil, "isSuspended": false})
			}
			return json.Marshal(users)
		}
		return json.RawMessage(`{"host":null,"isSuspended":false}`), nil
	}
	if endpoint != "i" {
		return nil, fmt.Errorf("unexpected endpoint: %s", endpoint)
	}
	return json.RawMessage(fmt.Sprintf(`{"host":null,"policies":{"genshinUidLimit":%d}}`, a.limit.Load())), nil
}

type verificationFixture struct {
	db     *sql.DB
	h      plugintest.Handlers
	api    *linkingAPI
	calls  atomic.Int64
	match  atomic.Bool
	ttl    atomic.Int64
	status atomic.Int64
}

func newVerificationFixture(t *testing.T, limit int64) *verificationFixture {
	t.Helper()
	f := &verificationFixture{db: testDB(t), api: &linkingAPI{}}
	f.api.limit.Store(limit)
	f.match.Store(true)
	f.ttl.Store(300)
	f.status.Store(http.StatusOK)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.calls.Add(1)
		if status := int(f.status.Load()); status != http.StatusOK {
			http.Error(w, "fixture unavailable", status)
			return
		}
		var signature string
		if f.match.Load() {
			if err := f.db.QueryRow(`SELECT string_agg(code,' ') FROM link_challenges WHERE uid=$1`, strings.TrimPrefix(r.URL.Path, "/api/uid/")).Scan(&signature); err != nil {
				http.Error(w, "missing proof", 404)
				return
			}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"playerInfo": map[string]any{"nickname": "Traveler", "signature": "前文" + signature + "後文"}, "ttl": f.ttl.Load()})
	}))
	t.Cleanup(srv.Close)
	f.h = plugintest.New(t).WithName("genshin").WithDB(f.db).WithAPI(f.api).
		WithConfig(map[string]any{"endpoint": srv.URL, "timeoutSeconds": 5}).Routes(rankingTestPlugin())
	return f
}

func (f *verificationFixture) begin(t *testing.T, user, uid string) challenge {
	t.Helper()
	res, err := f.h.Call(t, "POST /me/begin", plugintest.Request{UserID: user, Body: fmt.Sprintf(`{"uid":%q}`, uid)})
	if err != nil {
		t.Fatal(err)
	}
	return res.(challenge)
}
func (f *verificationFixture) verify(t *testing.T, user string, p challenge) (any, error) {
	t.Helper()
	return f.h.Call(t, "POST /me/verify", plugintest.Request{UserID: user, Body: fmt.Sprintf(`{"code":%q}`, p.Code)})
}
func (f *verificationFixture) count(t *testing.T) int {
	t.Helper()
	var n int
	if err := f.db.QueryRow(`SELECT count(*) FROM accounts`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestVerificationSuccessAndReplay(t *testing.T) {
	f := newVerificationFixture(t, 1)
	p := f.begin(t, "u1", "800000001")
	if remaining := time.Until(p.ExpiresAt); remaining < 9*time.Minute || remaining > 10*time.Minute {
		t.Fatalf("expiry: %s", remaining)
	}
	if f.count(t) != 0 {
		t.Fatal("pending request occupies a slot")
	}
	res, err := f.verify(t, "u1", p)
	if err != nil {
		t.Fatal(err)
	}
	if res.(map[string]any)["verified"] != true || f.count(t) != 1 {
		t.Fatalf("not linked: %v", res)
	}
	var snapshots int
	if err := f.db.QueryRow(`SELECT count(*) FROM snapshots`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 1 {
		t.Fatal("verified UID snapshot was not persisted")
	}
	if _, err = f.verify(t, "u1", p); err == nil {
		t.Fatal("used code accepted")
	}
	if _, err = f.h.Call(t, "POST /me/set", plugintest.Request{UserID: "u1", Body: `{"uid":"800000002"}`}); err == nil {
		t.Fatal("legacy bypass accepted")
	}
}

func TestVerificationTTLAndExpiry(t *testing.T) {
	f := newVerificationFixture(t, 1)
	f.match.Store(false)
	p := f.begin(t, "u1", "800000001")
	res, err := f.verify(t, "u1", p)
	if err != nil {
		t.Fatal(err)
	}
	if res.(map[string]any)["verified"] != false {
		t.Fatal("nonmatching signature accepted")
	}
	var snapshots int
	if err := f.db.QueryRow(`SELECT count(*) FROM snapshots`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 0 {
		t.Fatal("unverified UID snapshot was persisted")
	}
	f.match.Store(true)
	_, err = f.verify(t, "u1", p)
	if err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 1 || f.count(t) != 0 {
		t.Fatal("Enka ttl bypassed or unverified UID linked")
	}
	if _, err = f.db.Exec(`UPDATE link_challenges SET expires_at=now()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err = f.verify(t, "u1", p); err == nil {
		t.Fatal("expired code accepted")
	}
}

func TestVerificationMatchingSignatureCachePreservesTTL(t *testing.T) {
	f := newVerificationFixture(t, 1)
	p := f.begin(t, "u1", "800000001")
	if _, err := f.db.Exec(`INSERT INTO verification_cache(uid,signature,expires_at) VALUES($1,$2,now()+interval '5 minutes')`, p.UID, p.Code); err != nil {
		t.Fatal(err)
	}
	res, err := f.verify(t, "u1", p)
	if err != nil {
		t.Fatal(err)
	}
	if res.(map[string]any)["verified"] != true || f.calls.Load() != 0 || f.count(t) != 1 {
		t.Fatalf("cached proof bypassed TTL or failed to link: result=%v calls=%d", res, f.calls.Load())
	}
	var snapshots int
	if err := f.db.QueryRow(`SELECT count(*) FROM snapshots`).Scan(&snapshots); err != nil {
		t.Fatal(err)
	}
	if snapshots != 0 {
		t.Fatal("signature-only cache persisted an unverified snapshot")
	}
}

func TestVerificationReissueInvalidatesOldCode(t *testing.T) {
	f := newVerificationFixture(t, 2)
	p := f.begin(t, "u1", "800000001")
	if _, err := f.db.Exec(`UPDATE link_challenges SET issued_at=now()-interval '1 minute'`); err != nil {
		t.Fatal(err)
	}
	q := f.begin(t, "u1", "800000002")
	if p.Code == q.Code {
		t.Fatal("code reused")
	}
	if _, err := f.verify(t, "u1", p); err == nil {
		t.Fatal("old code accepted")
	}
	if _, err := f.verify(t, "u1", q); err != nil {
		t.Fatal(err)
	}
}

func TestVerificationMultipleUIDsAndPolicyReduction(t *testing.T) {
	f := newVerificationFixture(t, 2)
	for _, uid := range []string{"800000001", "800000002"} {
		p := f.begin(t, "u1", uid)
		if _, err := f.verify(t, "u1", p); err != nil {
			t.Fatal(err)
		}
	}
	if f.count(t) != 2 {
		t.Fatal("multiple verified UIDs not stored")
	}
	f.api.limit.Store(1)
	if _, err := f.h.Call(t, "POST /me/begin", plugintest.Request{UserID: "u1", Body: `{"uid":"800000003"}`}); err == nil {
		t.Fatal("lowered policy ignored")
	}
	if f.count(t) != 2 {
		t.Fatal("existing links removed on policy decrease")
	}
	f.api.limit.Store(2)
	p := f.begin(t, "u2", "800000003")
	f.api.limit.Store(0)
	if _, err := f.verify(t, "u2", p); err == nil {
		t.Fatal("policy not rechecked on verification")
	}
}

func TestVerificationUIDExclusivityAndUnlink(t *testing.T) {
	f := newVerificationFixture(t, 1)
	p := f.begin(t, "u1", "800000001")
	q := f.begin(t, "u2", "800000001")
	if _, err := f.verify(t, "u1", p); err != nil {
		t.Fatal(err)
	}
	if _, err := f.verify(t, "u2", q); err == nil {
		t.Fatal("UID linked to two users")
	}
	if _, err := f.h.Call(t, "POST /me/unlink", plugintest.Request{UserID: "u2", Body: `{"uid":"800000001"}`}); err != nil {
		t.Fatal(err)
	}
	if f.count(t) != 1 {
		t.Fatal("another user's link removed")
	}
	if _, err := f.h.Call(t, "POST /me/unlink", plugintest.Request{UserID: "u1", Body: `{"uid":"800000001"}`}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.verify(t, "u2", q); err != nil {
		t.Fatal(err)
	}
}

func TestLinkCodeFormatAndRandomness(t *testing.T) {
	seen := map[string]bool{}
	for range 100 {
		code, err := newLinkCode()
		if err != nil {
			t.Fatal(err)
		}
		if len(code) != 6 {
			t.Fatal("invalid code length")
		}
		hasSymbol := false
		for _, ch := range code {
			if strings.ContainsRune(linkCodeSymbols, ch) {
				hasSymbol = true
			} else if ch < '0' || ch > '9' {
				t.Fatal("expected decimal digit or allowed symbol")
			}
		}
		if !hasSymbol {
			t.Fatal("digit-only code accepted")
		}
		seen[code] = true
	}
	if len(seen) < 2 {
		t.Fatal("all random codes were identical")
	}
}

func TestSignatureContainsEntireIssuedCode(t *testing.T) {
	const code = "12!345"
	for _, tc := range []struct {
		signature, code string
		want            bool
	}{
		{code, code, true},
		{"前文" + code + "後文", code, true},
		{"x" + code + "y", code, true},
		{code[:len(code)-1], code, false},
		{"123456", code, false},
		{"12!346", code, false},
		{"12?345", code, false},
		{"12! 345", code, false},
		{"", code, false},
		{"anything", "", false},
	} {
		if got := signatureHasCode(tc.signature, tc.code); got != tc.want {
			t.Fatalf("signatureHasCode(%q,%q)=%v", tc.signature, tc.code, got)
		}
	}
}

func TestVerificationWaitsAtLeast60SecondsAfterMismatch(t *testing.T) {
	f := newVerificationFixture(t, 1)
	f.ttl.Store(1)
	f.match.Store(false)
	p := f.begin(t, "u1", "800000001")
	sent := time.Now()
	res, err := f.verify(t, "u1", p)
	if err != nil {
		t.Fatal(err)
	}
	next := res.(map[string]any)["nextCheckAt"].(time.Time)
	if next.Before(sent.Add(60 * time.Second)) {
		t.Fatalf("retry permitted too early: %v", next.Sub(sent))
	}
	f.match.Store(true)
	if _, err := f.verify(t, "u1", p); err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 1 || f.count(t) != 0 {
		t.Fatal("verification bypassed cooldown")
	}
	// Simulate expired Enka cache while still inside the last second of cooldown.
	if _, err := f.db.Exec(`UPDATE snapshots SET expires_at=clock_timestamp()-interval '1 second'; UPDATE verification_cache SET expires_at=clock_timestamp()-interval '1 second'; UPDATE link_challenges SET next_check_at=clock_timestamp()+interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.verify(t, "u1", p); err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 1 {
		t.Fatal("expired cache bypassed cooldown")
	}
	if _, err := f.db.Exec(`UPDATE link_challenges SET next_check_at=clock_timestamp()-interval '1 second'`); err != nil {
		t.Fatal(err)
	}
	res, err = f.verify(t, "u1", p)
	if err != nil || res.(map[string]any)["verified"] != true || f.calls.Load() != 2 {
		t.Fatalf("verification after cooldown: %v %v", res, err)
	}
}

func TestVerificationWaitsAtLeast60SecondsAfterUpstreamFailure(t *testing.T) {
	f := newVerificationFixture(t, 1)
	f.status.Store(http.StatusServiceUnavailable)
	p := f.begin(t, "u1", "800000001")
	sent := time.Now()
	if _, err := f.verify(t, "u1", p); err == nil {
		t.Fatal("upstream failure accepted")
	}
	var next time.Time
	var attempts int
	if err := f.db.QueryRow(`SELECT next_check_at,attempts FROM link_challenges WHERE user_id='u1'`).Scan(&next, &attempts); err != nil {
		t.Fatal(err)
	}
	if next.Before(sent.Add(60*time.Second)) || attempts != 1 {
		t.Fatalf("invalid failure cooldown: %v %d", next.Sub(sent), attempts)
	}
	if _, err := f.verify(t, "u1", p); err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 1 {
		t.Fatal("upstream retried during cooldown")
	}
}

func TestVerificationReissueDoesNotShortenCooldown(t *testing.T) {
	f := newVerificationFixture(t, 2)
	f.match.Store(false)
	f.ttl.Store(1)
	p := f.begin(t, "u1", "800000001")
	res, err := f.verify(t, "u1", p)
	if err != nil {
		t.Fatal(err)
	}
	next := res.(map[string]any)["nextCheckAt"].(time.Time)
	if _, err := f.db.Exec(`UPDATE link_challenges SET issued_at=clock_timestamp()-interval '31 seconds'`); err != nil {
		t.Fatal(err)
	}
	q := f.begin(t, "u1", "800000002")
	if q.NextCheckAt.Before(next) {
		t.Fatal("reissue shortened verification cooldown")
	}
	f.match.Store(true)
	if _, err := f.verify(t, "u1", q); err != nil {
		t.Fatal(err)
	}
	if f.calls.Load() != 1 || f.count(t) != 0 {
		t.Fatal("reissue bypassed verification cooldown")
	}
}

func TestVerificationRemoteCacheDoesNotCount(t *testing.T) {
	f := newVerificationFixture(t, 1)
	if _, err := f.db.Exec(`INSERT INTO peer_cache(host,key,payload,expires_at) VALUES('remote.example','u1','{"uid":"800000001"}',now()+interval '1 hour')`); err != nil {
		t.Fatal(err)
	}
	p := f.begin(t, "u1", "800000001")
	if _, err := f.verify(t, "u1", p); err != nil {
		t.Fatal(err)
	}
	if f.count(t) != 1 {
		t.Fatal("remote cache interfered with local limit")
	}
}

func TestVerificationParallelSameUID(t *testing.T) {
	f := newVerificationFixture(t, 1)
	p := f.begin(t, "u1", "800000001")
	q := f.begin(t, "u2", "800000001")
	errors := make(chan error, 2)
	go func() { _, err := f.verify(t, "u1", p); errors <- err }()
	go func() { _, err := f.verify(t, "u2", q); errors <- err }()
	successes := 0
	for range 2 {
		if <-errors == nil {
			successes++
		}
	}
	if successes != 1 || f.count(t) != 1 {
		t.Fatalf("duplicate UID race: successes=%d", successes)
	}
}

func TestVerificationAttemptLimit(t *testing.T) {
	f := newVerificationFixture(t, 1)
	p := f.begin(t, "u1", "800000001")
	if _, err := f.db.Exec(`UPDATE link_challenges SET attempts=10`); err != nil {
		t.Fatal(err)
	}
	if _, err := f.verify(t, "u1", p); err == nil {
		t.Fatal("attempt limit bypassed")
	}
	if f.calls.Load() != 0 {
		t.Fatal("attempt limit still requests Enka")
	}
}
