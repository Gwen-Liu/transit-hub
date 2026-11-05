package upstream

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// newAPIRefreshServer 模拟新版 new-api：/api/user/auth/refresh 用 refresh cookie 换出新 JWT 并轮换 cookie，
// /api/user/self 只认 Bearer fresh-jwt。refreshed 记录兑换次数，seenCookie / seenOrigin 记录最后一次刷新请求。
func newAPIRefreshServer(t *testing.T, refreshed *atomic.Int32, seenCookie *atomic.Value, seenOrigin *atomic.Value) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/user/auth/refresh":
			refreshed.Add(1)
			seenCookie.Store(r.Header.Get("Cookie"))
			seenOrigin.Store(r.Header.Get("Origin"))
			http.SetCookie(w, &http.Cookie{Name: newAPIRefreshCookieName, Value: "rt-2", Path: "/api/user/auth", HttpOnly: true, MaxAge: 3600})
			fmt.Fprintf(w, `{"success":true,"data":{"access_token":"fresh-jwt","token_type":"Bearer","access_expires_at":%d,"user":{"id":7}}}`, time.Now().Add(15*time.Minute).Unix())
		case "/api/user/self":
			if r.Header.Get("Authorization") != "Bearer fresh-jwt" {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			fmt.Fprint(w, `{"success":true,"data":{"id":7,"role":100}}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
}

// 新版 new-api 的 JWT 只有 15 分钟：上游提前作废令牌返回 401 时，也要像 Sub2API 一样强制续期后重试一次，
// 并把轮换后的会话写回，否则旧 refresh token 再被使用会触发 new-api 的重放保护、吊销整个登录会话。
func TestWithSiteSessionForceRefreshesNewAPIOn401(t *testing.T) {
	var refreshed atomic.Int32
	var seenCookie, seenOrigin atomic.Value
	server := newAPIRefreshServer(t, &refreshed, &seenCookie, &seenOrigin)
	defer server.Close()

	notDue := time.Now().Add(10 * time.Minute).UnixMilli()
	cache := newFakeSiteCache()
	cache.add(newTestSite("one", "user", "account", 1, &Session{
		Platform: PlatformNewAPI, BaseURL: server.URL, AccessToken: "revoked-jwt", TokenType: "Bearer",
		RefreshToken: "rt-1", ExpiresAt: &notDue, UserID: "7", QuotaPerUnit: 500000,
	}))
	s := NewService(newTestPlatformService(server.Client()), nil, nil, cache)

	calls := 0
	used, err := s.withSiteSession(context.Background(), "one", func(session Session) error {
		calls++
		_, err := s.platformService.httpClient.requestJSON(session.BaseURL+"/api/user/self", newAPIAuthOptions(session))
		return err
	})
	if err != nil {
		t.Fatalf("retry after forced refresh failed: %v", err)
	}
	if calls != 2 || refreshed.Load() != 1 || used.AccessToken != "fresh-jwt" {
		t.Fatalf("calls=%d refreshed=%d used=%+v", calls, refreshed.Load(), used)
	}
	if cookie, _ := seenCookie.Load().(string); cookie != newAPIRefreshCookieName+"=rt-1" {
		t.Fatalf("refresh cookie = %q", cookie)
	}
	if origin, _ := seenOrigin.Load().(string); origin != server.URL {
		t.Fatalf("refresh origin = %q, want %q", origin, server.URL)
	}
	stored, _ := cache.Get(context.Background(), "one")
	if stored.Session.AccessToken != "fresh-jwt" || stored.Session.RefreshToken != "rt-2" || stored.Session.QuotaPerUnit != 500000 {
		t.Fatalf("rotated session not persisted: %+v", stored.Session)
	}
}

// 同一个旧会话被两条流程先后强制续期时，只能向上游兑换一次；第二次直接拿到本进程已轮换出的会话，不重放旧 token。
func TestForceRefreshSessionDoesNotReplayRotatedToken(t *testing.T) {
	var refreshed atomic.Int32
	var seenCookie, seenOrigin atomic.Value
	server := newAPIRefreshServer(t, &refreshed, &seenCookie, &seenOrigin)
	defer server.Close()

	service := newTestPlatformService(server.Client())
	notDue := time.Now().Add(10 * time.Minute).UnixMilli()
	old := Session{Platform: PlatformNewAPI, BaseURL: server.URL, AccessToken: "revoked-jwt", TokenType: "Bearer", RefreshToken: "rt-1", ExpiresAt: &notDue}

	first, err := service.ForceRefreshSession(old)
	if err != nil {
		t.Fatalf("first forced refresh: %v", err)
	}
	second, err := service.ForceRefreshSession(old)
	if err != nil {
		t.Fatalf("second forced refresh: %v", err)
	}
	if refreshed.Load() != 1 {
		t.Fatalf("refresh exchanged %d times, want 1", refreshed.Load())
	}
	if first.RefreshToken != "rt-2" || second.RefreshToken != "rt-2" || second.AccessToken != "fresh-jwt" {
		t.Fatalf("first=%+v second=%+v", first, second)
	}

	// 没有 refresh 凭据（访问令牌 / 旧版 Cookie 会话）时原样返回，不发起刷新。
	token := Session{Platform: PlatformNewAPI, BaseURL: server.URL, AccessToken: "nap_token", TokenType: "Bearer"}
	same, err := service.ForceRefreshSession(token)
	if err != nil || !same.sameCredentials(token) || refreshed.Load() != 1 {
		t.Fatalf("token session should be returned as-is: same=%+v err=%v refreshed=%d", same, err, refreshed.Load())
	}
}
