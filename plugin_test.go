package genshin

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// fakeEnka serves a canned Enka.Network response.
func fakeEnka(t *testing.T, status int, body string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") == "" {
			t.Error("User-Agent を送っていない (enka が明示的に求めている)")
		}
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testClient(url string) *enkaClient {
	return &enkaClient{
		set:  settings{Endpoint: url, UserAgent: "test/1.0", TimeoutSeconds: 5},
		http: &http.Client{Timeout: 5 * time.Second},
	}
}

func TestFetch_ParsesPlayerInfo(t *testing.T) {
	srv := fakeEnka(t, http.StatusOK,
		`{"playerInfo":{"nickname":"Traveler","level":60,"worldLevel":8,"signature":"hi"},"ttl":300,"uid":"800000000"}`)

	got, err := testClient(srv.URL).fetch(context.Background(), "800000000")
	if err != nil {
		t.Fatal(err)
	}
	if got.nickname != "Traveler" || got.level != 60 || got.worldLevel != 8 || got.ttl != 300 {
		t.Fatalf("想定と違う: %+v", got)
	}
}

// ttl が無い応答でも、間を置かずに再取得しないこと。
func TestFetch_DefaultsTTL(t *testing.T) {
	srv := fakeEnka(t, http.StatusOK, `{"playerInfo":{"nickname":"x","level":1}}`)

	got, err := testClient(srv.URL).fetch(context.Background(), "800000000")
	if err != nil {
		t.Fatal(err)
	}
	if got.ttl <= 0 {
		t.Fatalf("ttl が既定値にならない: %d", got.ttl)
	}
}

// **利用者が直せるものだけ見せる。** 400/404 は UID の問題なので伝える。
func TestFetch_UserFacingErrors(t *testing.T) {
	for _, tt := range []struct {
		status int
		want   string
	}{
		{http.StatusBadRequest, "UID の形式が正しくありません"},
		{http.StatusNotFound, "その UID のプレイヤーが見つかりません"},
	} {
		srv := fakeEnka(t, tt.status, `{}`)
		_, err := testClient(srv.URL).fetch(context.Background(), "800000000")

		ue, ok := err.(*upstreamError)
		if !ok {
			t.Fatalf("status %d: upstreamError ではない: %v", tt.status, err)
		}
		if ue.userFacing != tt.want {
			t.Fatalf("status %d: %q", tt.status, ue.userFacing)
		}
	}
}

// 424 (ゲーム側に届かない) / 429 (レート制限) はこちらの都合ではないので
// 利用者には見せない。実際に enka は 424 を返すことがある。
func TestFetch_UpstreamFailuresAreNotUserFacing(t *testing.T) {
	for _, status := range []int{http.StatusFailedDependency, http.StatusTooManyRequests, http.StatusInternalServerError} {
		srv := fakeEnka(t, status, `{"message":"..."}`)
		_, err := testClient(srv.URL).fetch(context.Background(), "800000000")

		ue, ok := err.(*upstreamError)
		if !ok {
			t.Fatalf("status %d: upstreamError ではない: %v", status, err)
		}
		if ue.userFacing != "" {
			t.Fatalf("status %d: 利用者に見せてはいけない: %q", status, ue.userFacing)
		}
	}
}

func TestFetch_BrokenJSON(t *testing.T) {
	srv := fakeEnka(t, http.StatusOK, `not json`)
	if _, err := testClient(srv.URL).fetch(context.Background(), "800000000"); err == nil {
		t.Fatal("エラーにならない")
	}
}

// 形式が違う UID は upstream に投げる前に弾く (向こうのレート制限を無駄に
// 消費しない)。
func TestUIDPattern(t *testing.T) {
	ok := []string{"800000000", "1234567890", "618285856"}
	for _, s := range ok {
		if !uidPattern.MatchString(s) {
			t.Errorf("%q は許可される", s)
		}
	}
	ng := []string{"", "0123456789", "12345678", "12345678901", "abcdefghi", "800-000-000"}
	for _, s := range ng {
		if uidPattern.MatchString(s) {
			t.Errorf("%q は拒否される", s)
		}
	}
}

// ショーケースのビルドまで取り込むこと。`?info` を落として全部取るように
// なったので、avatarInfoList が来たら characters に入る。
func TestFetch_ParsesAvatarInfoList(t *testing.T) {
	srv := fakeEnka(t, http.StatusOK, `{
		"playerInfo":{"nickname":"x","level":60,"towerStarIndex":36,
			"theaterActIndex":10,"theaterModeIndex":3,"theaterStarIndex":10,"fetterCount":42},
		"avatarInfoList":[`+sampleAvatar+`],
		"ttl":300,"uid":"800000000"}`)

	got, err := testClient(srv.URL).fetch(context.Background(), "800000000")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.characters) != 1 {
		t.Fatalf("characters の件数: %d", len(got.characters))
	}
	if got.characters[0].Level != 90 {
		t.Errorf("キャラのレベルが取れていない: %d", got.characters[0].Level)
	}
	if got.towerStar != 36 || got.fetterCount != 42 {
		t.Errorf("螺旋の星数 / 好感度カウント: %d / %d", got.towerStar, got.fetterCount)
	}
	if got.theaterAct != 10 || got.theaterMode != 3 || got.theaterStar != 10 {
		t.Errorf("幻想シアター: %d / %d / %d", got.theaterAct, got.theaterMode, got.theaterStar)
	}
}

// 12体まで保持し、取得元が想定外の数を返しても保存が膨らまないよう切る。
func TestFetch_CapsCharacters(t *testing.T) {
	for _, count := range []int{8, 12, 13} {
		t.Run(fmt.Sprintf("%d", count), func(t *testing.T) {
			list, showcase := "", ""
			for i := range count {
				if i > 0 {
					list += ","
					showcase += ","
				}
				list += sampleAvatar
				showcase += fmt.Sprintf(`{"avatarId":%d,"level":90}`, i+1)
			}
			srv := fakeEnka(t, http.StatusOK,
				`{"playerInfo":{"nickname":"x","level":1,"showAvatarInfoList":[`+showcase+`]},"avatarInfoList":[`+list+`],"ttl":300}`)
			got, err := testClient(srv.URL).fetch(context.Background(), "800000000")
			if err != nil {
				t.Fatal(err)
			}
			want := min(count, 12)
			if len(got.characters) != want || len(got.showcase) != want {
				t.Fatalf("expected %d characters and previews: details=%d previews=%d", want, len(got.characters), len(got.showcase))
			}
			if got.showcase[want-1].AvatarID != want {
				t.Fatal("showcase order changed")
			}
		})
	}
}

// 詳細を公開していないアカウントでは avatarInfoList が来ない。
// そのときも playerInfo だけで成立すること。
func TestFetch_WithoutAvatarInfoList(t *testing.T) {
	srv := fakeEnka(t, http.StatusOK, `{"playerInfo":{"nickname":"x","level":60},"ttl":300}`)

	got, err := testClient(srv.URL).fetch(context.Background(), "800000000")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.characters) != 0 {
		t.Errorf("characters が空でない: %d", len(got.characters))
	}
}

// プロフィール画像の id 空間が 2 つあることを保存形式で区別する。
//
// **Enka は `profilePicture` を `{"id": 9100}` で返すようになった。** 旧形式の
// `{"avatarId": 10000046}` はキャラ id なので characters で引けるが、新しい id は
// プロフィール画像専用の id 空間で引く先が違う。区別せず数値だけを保存していた
// 頃は `0` が入り、**アイコンが一切出なかった** (本番の DB で実測)。
func TestProfileIconKey(t *testing.T) {
	for _, tt := range []struct {
		name            string
		pfpID, avatarID int
		want            string
	}{
		{"現行形式を優先する", 9100, 10000046, "pfp:9100"},
		{"現行形式のみ", 9100, 0, "pfp:9100"},
		{"旧形式のみ", 0, 10000046, "10000046"},
		{"どちらも無ければ空", 0, 0, ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := profileIconKey(tt.pfpID, tt.avatarID); got != tt.want {
				t.Errorf("profileIconKey(%d, %d) = %q, want %q", tt.pfpID, tt.avatarID, got, tt.want)
			}
		})
	}
}

// 保存形式からアイコンを引く。**引く先を間違えないこと**が要点で、
// `pfp:` の付いていない値をプロフィール画像の master で引くと別人が出る。
func TestProfileIconURL(t *testing.T) {
	pfps := &characterStore{
		byID:    map[string]characterInfo{"9100": {IconPath: "/ui/UI_AvatarIcon_Citlali_Circle.png"}},
		fetched: time.Now(),
	}
	chars := &characterStore{
		byID:    map[string]characterInfo{"10000046": {SideIconName: "UI_AvatarIcon_Side_Hutao"}},
		fetched: time.Now(),
	}
	ctx := context.Background()

	for _, tt := range []struct {
		name, stored, want string
	}{
		{"現行形式", "pfp:9100", "/api/plugin/genshin/asset/UI_AvatarIcon_Citlali_Circle.png"},
		{"旧形式", "10000046", "/api/plugin/genshin/asset/UI_AvatarIcon_Hutao"},
		// **id 空間を跨いで引かないこと。** 同じ数値でも master が違う。
		{"現行形式の id を旧形式として渡しても引けない", "9100", ""},
		{"旧形式の id を現行形式として渡しても引けない", "pfp:10000046", ""},
		{"未設定 (旧形式のゼロ)", "0", ""},
		{"未設定 (空)", "", ""},
		{"現行形式のゼロ", "pfp:0", ""},
		{"数値でない", "pfp:zzz", ""},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := profileIconURL(ctx, pfps, chars, tt.stored); got != tt.want {
				t.Errorf("profileIconURL(%q) = %q, want %q", tt.stored, got, tt.want)
			}
		})
	}
}
