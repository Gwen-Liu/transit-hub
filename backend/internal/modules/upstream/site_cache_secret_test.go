package upstream

import (
	"bytes"
	"strings"
	"testing"

	"transithub/backend/internal/shared/secretbox"
)

// 上游站点的 Redis 缓存里也有完整登录状态（含 refresh token、Admin Key），Redis 会落盘，所以同样要加密保存。
func TestRedisSiteCacheEncryptsSessions(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	other, err := secretbox.New(bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	site := newTestSite("s1", "u1", "a1", 1, &Session{Platform: PlatformSub2API, AccessToken: "cache-token-secret", RefreshToken: "cache-refresh-secret", AdminAPIKey: "cache-admin-key-secret"})
	site.Name = "Upstream A"

	cache := NewRedisSiteCache(nil)
	cache.SetSecretBox(box)
	raw, err := cache.encode(site)
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"cache-token-secret", "cache-refresh-secret", "cache-admin-key-secret"} {
		if strings.Contains(string(raw), secret) {
			t.Fatalf("secret %q is visible in the cached JSON: %s", secret, raw)
		}
	}
	if !strings.Contains(string(raw), `"sealedSession":"enc:v1:`) {
		t.Fatalf("expected an encrypted sealedSession field: %s", raw)
	}
	got, err := cache.decode(string(raw))
	if err != nil || got.Session == nil || got.Session.AccessToken != "cache-token-secret" || got.Session.AdminAPIKey != "cache-admin-key-secret" || got.UserID != "u1" {
		t.Fatalf("read back: %+v %v", got, err)
	}

	// 换了密钥：只当作未登录，站点其它字段照常可用。
	reader := NewRedisSiteCache(nil)
	reader.SetSecretBox(other)
	if got, err := reader.decode(string(raw)); err != nil || got.Session != nil || got.Name != "Upstream A" {
		t.Fatalf("other key: expected a site without session, got %+v %v", got, err)
	}

	// 没有密钥：保持原来的明文格式；配置密钥后仍能读出这种旧值。
	plainCache := NewRedisSiteCache(nil)
	plainRaw, err := plainCache.encode(site)
	if err != nil || !strings.Contains(string(plainRaw), "cache-token-secret") || strings.Contains(string(plainRaw), "sealedSession") {
		t.Fatalf("no key should keep the plaintext format: %s %v", plainRaw, err)
	}
	if got, err := cache.decode(string(plainRaw)); err != nil || got.Session == nil || got.Session.RefreshToken != "cache-refresh-secret" {
		t.Fatalf("legacy plaintext cache entry: %+v %v", got, err)
	}

	// 没有登录状态的站点不写 sealedSession。
	empty := newTestSite("s2", "u1", "a1", 1, nil)
	if raw, err := cache.encode(empty); err != nil || strings.Contains(string(raw), "sealedSession") {
		t.Fatalf("site without session: %s %v", raw, err)
	}
}
