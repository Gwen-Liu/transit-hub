// Package credentialstest 是凭据加密的集成测试：用真实的 PostgreSQL 和 Redis 检查各模块保存的登录凭据
// 都是密文、读出来和原来一样、加密前的明文照样能读并能批量改成加密、换了密钥时当作没有保存。
// 测试改编自作者 9d631fd：去掉了 Fork 没有的出站代理，补上了 Fork 独有的对接记录上游 Key。
//
// 需要 TEST_DATABASE_URL（在临时 schema 里只写合成数据，结束后删除）和 TEST_REDIS_URL（只写本测试的 key，结束后删除）。
package credentialstest

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"transithub/backend/internal/modules/dashboard"
	"transithub/backend/internal/modules/my_sites"
	"transithub/backend/internal/modules/settings"
	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/secretbox"
)

func openPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	databaseURL := strings.TrimSpace(os.Getenv("TEST_DATABASE_URL"))
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is required for PostgreSQL tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	adminPool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("connect PostgreSQL: %v", err)
	}
	schema := fmt.Sprintf("credentials_test_%d", time.Now().UnixNano())
	quoted := pgx.Identifier{schema}.Sanitize()
	if _, err := adminPool.Exec(ctx, "CREATE SCHEMA "+quoted); err != nil {
		adminPool.Close()
		t.Fatalf("create schema: %v", err)
	}
	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		adminPool.Close()
		t.Fatalf("parse config: %v", err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		adminPool.Close()
		t.Fatalf("connect schema: %v", err)
	}
	t.Cleanup(func() {
		pool.Close()
		if _, err := adminPool.Exec(context.Background(), "DROP SCHEMA "+quoted+" CASCADE"); err != nil {
			t.Errorf("drop schema: %v", err)
		}
		adminPool.Close()
	})
	return pool
}

func keys(t *testing.T) (*secretbox.Box, *secretbox.Box) {
	t.Helper()
	box, err := secretbox.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	other, err := secretbox.New(bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	return box, other
}

// rawColumn 读出 jsonb 列在数据库里实际保存的内容。
func rawColumn(t *testing.T, pool *pgxpool.Pool, query string, args ...any) string {
	t.Helper()
	var raw string
	if err := pool.QueryRow(context.Background(), query, args...).Scan(&raw); err != nil {
		t.Fatalf("read raw column: %v", err)
	}
	return raw
}

// assertSealed：jsonb 里存的是一个 JSON 字符串，内容是密文，看不到明文的凭据。
func assertSealed(t *testing.T, raw string, secret string) {
	t.Helper()
	var text string
	if err := json.Unmarshal([]byte(raw), &text); err != nil || !secretbox.IsSealed(text) {
		t.Fatalf("expected an encrypted value, got %s", raw)
	}
	if strings.Contains(raw, secret) {
		t.Fatalf("the secret is visible in the database: %s", raw)
	}
}

func TestSiteAdminSessionsAreEncrypted(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()
	box, other := keys(t)
	repo := my_sites.NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	repo.SetSecretBox(box)

	state := my_sites.State{UserID: "u1", AdminAccountID: "a1", BaseURL: "https://relay.example.com", Email: "admin@example.com",
		Session:  upstream.Session{Platform: upstream.PlatformSub2API, AccessToken: "site-token-secret", RefreshToken: "site-refresh-secret"},
		Mappings: []my_sites.GroupMapping{{OwnGroup: "vip"}}}
	if err := repo.Save(ctx, state); err != nil {
		t.Fatal(err)
	}
	assertSealed(t, rawColumn(t, pool, `SELECT session::text FROM my_site_states WHERE user_id = 'u1'`), "site-token-secret")
	got, err := repo.Get(ctx, "u1", "a1")
	if err != nil || got == nil || got.Session.AccessToken != "site-token-secret" || got.Session.RefreshToken != "site-refresh-secret" {
		t.Fatalf("read back: %+v %v", got, err)
	}

	// 加密之前保存的明文：照样能读，批量改成加密后还能读。
	if _, err := pool.Exec(ctx, `INSERT INTO my_site_states (user_id, admin_account_id, base_url, email, session)
		VALUES ('u2', 'a2', 'https://old.example.com', 'old@example.com', '{"AccessToken":"legacy-site-secret"}')`); err != nil {
		t.Fatal(err)
	}
	if legacy, err := repo.Get(ctx, "u2", "a2"); err != nil || legacy == nil || legacy.Session.AccessToken != "legacy-site-secret" {
		t.Fatalf("legacy plaintext: %+v %v", legacy, err)
	}
	if changed, err := repo.EncryptLegacySessions(ctx); err != nil || changed != 1 {
		t.Fatalf("encrypt legacy: %d %v", changed, err)
	}
	assertSealed(t, rawColumn(t, pool, `SELECT session::text FROM my_site_states WHERE user_id = 'u2'`), "legacy-site-secret")
	if legacy, err := repo.Get(ctx, "u2", "a2"); err != nil || legacy == nil || legacy.Session.AccessToken != "legacy-site-secret" {
		t.Fatalf("after encrypting legacy: %+v %v", legacy, err)
	}
	if changed, _ := repo.EncryptLegacySessions(ctx); changed != 0 {
		t.Fatalf("nothing left to encrypt, changed %d", changed)
	}

	// 换了密钥或没有密钥：当作没有保存会话（需要重新登录），不报错；Fork 的自动调价依赖分组映射，映射照常保留。
	for name, wrong := range map[string]*secretbox.Box{"other key": other, "no key": nil} {
		reader := my_sites.NewRepository(pool)
		reader.SetSecretBox(wrong)
		got, err := reader.Get(ctx, "u1", "a1")
		if err != nil || got == nil || got.Session.IsAuthenticated() || len(got.Mappings) != 1 || got.Mappings[0].OwnGroup != "vip" {
			t.Fatalf("%s: expected mappings without a session, got %+v %v", name, got, err)
		}
	}
}

func TestUpstreamSessionsAreEncrypted(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()
	box, other := keys(t)
	// 上游表的建表语句会读 users（给旧数据补用户），保存时检查 admin_accounts：只建用到的列。
	if _, err := pool.Exec(ctx, `CREATE TABLE users (id text PRIMARY KEY); CREATE TABLE admin_accounts (id text, user_id text);
		INSERT INTO admin_accounts VALUES ('a1', 'u1')`); err != nil {
		t.Fatal(err)
	}
	repo := upstream.NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	repo.SetSecretBox(box)
	site := upstream.Site{ID: "s1", UserID: "u1", AdminAccountID: "a1", Name: "Upstream", BaseURL: "https://up.example.com",
		Platform: upstream.PlatformSub2API, RechargeRate: 1, Status: "connected",
		Session: &upstream.Session{AccessToken: "upstream-token-secret", AdminAPIKey: "upstream-admin-key-secret"}}
	if err := repo.SaveSite(ctx, site); err != nil {
		t.Fatal(err)
	}
	noSession := site
	noSession.ID, noSession.Session = "s2", nil
	if err := repo.SaveSite(ctx, noSession); err != nil {
		t.Fatal(err)
	}
	raw := rawColumn(t, pool, `SELECT session::text FROM upstream_sites WHERE id = 's1'`)
	assertSealed(t, raw, "upstream-token-secret")
	assertSealed(t, raw, "upstream-admin-key-secret")

	sessions := func(reader *upstream.Repository) map[string]*upstream.Session {
		t.Helper()
		sites, err := reader.ListSitesForUser(ctx, "u1")
		if err != nil {
			t.Fatalf("list sites: %v", err)
		}
		result := map[string]*upstream.Session{}
		for _, s := range sites {
			result[s.ID] = s.Session
		}
		return result
	}
	got := sessions(repo)
	if got["s1"] == nil || got["s1"].AccessToken != "upstream-token-secret" || got["s1"].AdminAPIKey != "upstream-admin-key-secret" || got["s2"] != nil {
		t.Fatalf("read back: %+v", got)
	}

	if _, err := pool.Exec(ctx, `UPDATE upstream_sites SET session = '{"AccessToken":"legacy-upstream-secret"}'::jsonb WHERE id = 's1'`); err != nil {
		t.Fatal(err)
	}
	if got := sessions(repo); got["s1"] == nil || got["s1"].AccessToken != "legacy-upstream-secret" {
		t.Fatalf("legacy plaintext: %+v", got["s1"])
	}
	if changed, err := repo.EncryptLegacySessions(ctx); err != nil || changed != 1 {
		t.Fatalf("encrypt legacy: %d %v", changed, err)
	}
	assertSealed(t, rawColumn(t, pool, `SELECT session::text FROM upstream_sites WHERE id = 's1'`), "legacy-upstream-secret")
	if got := sessions(repo); got["s1"] == nil || got["s1"].AccessToken != "legacy-upstream-secret" {
		t.Fatalf("after encrypting legacy: %+v", got["s1"])
	}
	if raw := rawColumn(t, pool, `SELECT coalesce(session::text, 'NULL') FROM upstream_sites WHERE id = 's2'`); raw != "NULL" {
		t.Fatalf("a site without a session stays empty, got %s", raw)
	}

	reader := upstream.NewRepository(pool)
	reader.SetSecretBox(other)
	if got := sessions(reader); got["s1"] != nil {
		t.Fatalf("other key: expected no session, got %+v", got["s1"])
	}
}

func TestNotificationChannelsAreEncrypted(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()
	box, other := keys(t)
	repo := settings.NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	repo.SetSecretBox(box)
	channels := settings.DefaultNotificationChannelSettings()
	channels.Telegram = []settings.TelegramChannelSettings{{ID: "tg1", Name: "运营群", Enabled: true, BotToken: "123:bot-token-secret", ChatID: "-100"}}
	channels.Feishu = []settings.WebhookChannelSettings{{ID: "fs1", Enabled: true, Webhook: "https://open.feishu.cn/hook/webhook-secret", Secret: "sign-secret"}}
	if err := repo.SaveNotificationChannels(ctx, "u1", "a1", channels); err != nil {
		t.Fatal(err)
	}
	raw := rawColumn(t, pool, `SELECT settings::text FROM notification_channel_settings WHERE user_id = 'u1'`)
	for _, secret := range []string{"bot-token-secret", "webhook-secret", "sign-secret"} {
		assertSealed(t, raw, secret)
	}
	got, err := repo.GetNotificationChannels(ctx, "u1", "a1")
	if err != nil || len(got.Telegram) != 1 || got.Telegram[0].BotToken != "123:bot-token-secret" || got.Feishu[0].Secret != "sign-secret" {
		t.Fatalf("read back: %+v %v", got, err)
	}

	if _, err := pool.Exec(ctx, `INSERT INTO notification_channel_settings (user_id, admin_account_id, settings)
		VALUES ('u2', 'a2', '{"telegram":[{"id":"old","enabled":true,"botToken":"legacy-bot-secret"}]}')`); err != nil {
		t.Fatal(err)
	}
	if legacy, err := repo.GetNotificationChannels(ctx, "u2", "a2"); err != nil || len(legacy.Telegram) != 1 || legacy.Telegram[0].BotToken != "legacy-bot-secret" {
		t.Fatalf("legacy plaintext: %+v %v", legacy, err)
	}
	if changed, err := repo.EncryptLegacyNotificationChannels(ctx); err != nil || changed != 1 {
		t.Fatalf("encrypt legacy: %d %v", changed, err)
	}
	assertSealed(t, rawColumn(t, pool, `SELECT settings::text FROM notification_channel_settings WHERE user_id = 'u2'`), "legacy-bot-secret")

	reader := settings.NewRepository(pool)
	reader.SetSecretBox(other)
	if got, err := reader.GetNotificationChannels(ctx, "u1", "a1"); err != nil || len(got.Telegram) != 0 {
		t.Fatalf("other key: expected no bots, got %+v %v", got, err)
	}
}

func TestDashboardSessionsInRedisAreEncrypted(t *testing.T) {
	redisURL := strings.TrimSpace(os.Getenv("TEST_REDIS_URL"))
	if redisURL == "" {
		t.Skip("TEST_REDIS_URL is required for Redis tests")
	}
	options, err := redis.ParseURL(redisURL)
	if err != nil {
		t.Fatal(err)
	}
	client := redis.NewClient(options)
	defer client.Close()
	ctx := context.Background()
	box, other := keys(t)
	user := fmt.Sprintf("credentials-test-%d", time.Now().UnixNano())
	keyFor := func(account string) string { return "dashboard:admin:session:" + user + ":" + account }
	t.Cleanup(func() {
		client.Del(ctx, keyFor("a1"), keyFor("a2"))
		client.SRem(ctx, "dashboard:admin:active", user+":a1", user+":a2")
	})

	repo := dashboard.NewRepository(client)
	repo.SetSecretBox(box)
	session := dashboard.AdminSession{Platform: "sub2api", BaseURL: "https://relay.example.com", Identity: "admin@example.com",
		Session: upstream.Session{AccessToken: "redis-token-secret", RefreshToken: "redis-refresh-secret"}}
	if err := repo.Save(ctx, user, "a1", session); err != nil {
		t.Fatal(err)
	}
	raw, err := client.Get(ctx, keyFor("a1")).Result()
	if err != nil || !secretbox.IsSealed(raw) || strings.Contains(raw, "redis-token-secret") {
		t.Fatalf("expected an encrypted value in Redis, got %q %v", raw, err)
	}
	got, err := repo.Get(ctx, user, "a1")
	if err != nil || got == nil || got.Session.AccessToken != "redis-token-secret" || got.Session.RefreshToken != "redis-refresh-secret" {
		t.Fatalf("read back: %+v %v", got, err)
	}

	legacy := `{"platform":"sub2api","session":{"AccessToken":"legacy-redis-secret"}}`
	client.Set(ctx, keyFor("a2"), legacy, 0)
	client.SAdd(ctx, "dashboard:admin:active", user+":a2")
	if got, err := repo.Get(ctx, user, "a2"); err != nil || got == nil || got.Session.AccessToken != "legacy-redis-secret" {
		t.Fatalf("legacy plaintext: %+v %v", got, err)
	}
	if changed, err := repo.EncryptLegacySessions(ctx); err != nil || changed < 1 {
		t.Fatalf("encrypt legacy: %d %v", changed, err)
	}
	if raw, _ := client.Get(ctx, keyFor("a2")).Result(); !secretbox.IsSealed(raw) {
		t.Fatalf("legacy value not encrypted: %q", raw)
	}
	if got, err := repo.Get(ctx, user, "a2"); err != nil || got == nil || got.Session.AccessToken != "legacy-redis-secret" {
		t.Fatalf("after encrypting legacy: %+v %v", got, err)
	}

	reader := dashboard.NewRepository(client)
	reader.SetSecretBox(other)
	if got, err := reader.Get(ctx, user, "a1"); err != nil || got != nil {
		t.Fatalf("other key: expected no session, got %+v %v", got, err)
	}
}

// TestConnectionUpstreamKeysAreEncrypted 覆盖 Fork 独有的 real_connections.upstream_key：完整的上游 API Key 加密保存。
func TestConnectionUpstreamKeysAreEncrypted(t *testing.T) {
	pool := openPool(t)
	ctx := context.Background()
	box, other := keys(t)
	repo := my_sites.NewRepository(pool)
	if err := repo.EnsureSchema(ctx); err != nil {
		t.Fatal(err)
	}
	repo.SetSecretBox(box)
	conn := my_sites.RealConnection{ID: "c1", UserID: "u1", WorkspaceAdminAccountID: "a1", UpstreamSiteID: "s1",
		UpstreamGroupID: "g1", UpstreamGroupName: "vip", UpstreamKeyID: "k1", UpstreamKey: "sk-upstream-key-secret",
		AdminAccountID: "acc1", AdminAccountName: "relay", GroupType: "openai", ProvisioningMode: "managed", Status: "active",
		CreatedAt: time.Now().UTC().Format(time.RFC3339)}
	if err := repo.SaveRealConnection(ctx, conn); err != nil {
		t.Fatal(err)
	}
	raw := rawColumn(t, pool, `SELECT upstream_key FROM real_connections WHERE id = 'c1'`)
	if !secretbox.IsSealed(raw) || strings.Contains(raw, "sk-upstream-key-secret") {
		t.Fatalf("expected an encrypted key, got %s", raw)
	}
	keyOf := func(reader *my_sites.Repository, id string) string {
		t.Helper()
		got, err := reader.GetRealConnection(ctx, id, "u1", "a1")
		if err != nil || got == nil {
			t.Fatalf("read connection %s: %+v %v", id, got, err)
		}
		return got.UpstreamKey
	}
	if key := keyOf(repo, "c1"); key != "sk-upstream-key-secret" {
		t.Fatalf("read back key = %q", key)
	}

	// 加密之前保存的明文：照样能读，批量改成加密后还能读；空 Key 保持为空。
	if _, err := pool.Exec(ctx, `INSERT INTO real_connections (id, user_id, workspace_admin_account_id, upstream_site_id, upstream_group_id,
		upstream_group_name, upstream_key_id, upstream_key, admin_account_id, admin_account_name, group_type)
		VALUES ('c2', 'u1', 'a1', 's1', 'g2', 'old', 'k2', 'sk-legacy-key-secret', 'acc2', 'relay', 'openai'),
		       ('c3', 'u1', 'a1', 's1', 'g3', 'empty', 'k3', '', 'acc3', 'relay', 'openai')`); err != nil {
		t.Fatal(err)
	}
	if key := keyOf(repo, "c2"); key != "sk-legacy-key-secret" {
		t.Fatalf("legacy plaintext key = %q", key)
	}
	if changed, err := repo.EncryptLegacyUpstreamKeys(ctx); err != nil || changed != 1 {
		t.Fatalf("encrypt legacy keys: %d %v", changed, err)
	}
	if raw := rawColumn(t, pool, `SELECT upstream_key FROM real_connections WHERE id = 'c2'`); !secretbox.IsSealed(raw) {
		t.Fatalf("legacy key not encrypted: %s", raw)
	}
	if raw := rawColumn(t, pool, `SELECT upstream_key FROM real_connections WHERE id = 'c3'`); raw != "" {
		t.Fatalf("an empty key stays empty, got %q", raw)
	}
	if key := keyOf(repo, "c2"); key != "sk-legacy-key-secret" {
		t.Fatalf("after encrypting legacy key = %q", key)
	}
	if changed, _ := repo.EncryptLegacyUpstreamKeys(ctx); changed != 0 {
		t.Fatalf("nothing left to encrypt, changed %d", changed)
	}

	// 换了密钥：当作没有保存 Key。
	reader := my_sites.NewRepository(pool)
	reader.SetSecretBox(other)
	if key := keyOf(reader, "c1"); key != "" {
		t.Fatalf("other key: expected no upstream key, got %q", key)
	}
}
