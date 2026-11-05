package my_sites

import (
	"bytes"
	"strings"
	"testing"

	"transithub/backend/internal/modules/upstream"
	"transithub/backend/internal/shared/secretbox"
)

// fakeStateRow 模拟 my_site_states 的一行：按 SELECT 的列顺序把值写进 Scan 的目标。
type fakeStateRow struct {
	userID, adminAccountID, baseURL, email string
	session, mappings, ownGroups           []byte
}

func (r fakeStateRow) Scan(dest ...any) error {
	*dest[0].(*string) = r.userID
	*dest[1].(*string) = r.adminAccountID
	*dest[2].(*string) = r.baseURL
	*dest[3].(*string) = r.email
	*dest[4].(*[]byte) = r.session
	*dest[5].(*[]byte) = r.mappings
	*dest[6].(*[]byte) = r.ownGroups
	return nil
}

// 会话加密保存；换了密钥读不出来时只当作没有会话，分组映射（自动调价依赖它）照常保留。
func TestStateSessionIsSealedAndMappingsSurviveWrongKey(t *testing.T) {
	box, err := secretbox.New(bytes.Repeat([]byte{1}, 32))
	if err != nil {
		t.Fatal(err)
	}
	other, err := secretbox.New(bytes.Repeat([]byte{2}, 32))
	if err != nil {
		t.Fatal(err)
	}
	repo := NewRepository(nil)
	repo.SetSecretBox(box)
	state := State{UserID: "u1", AdminAccountID: "a1", BaseURL: "https://relay.example.com", Email: "admin@example.com",
		Session:  upstream.Session{Platform: upstream.PlatformSub2API, AccessToken: "state-token-secret", RefreshToken: "state-refresh-secret"},
		Mappings: []GroupMapping{{OwnGroup: "vip"}}}
	sessionJSON, mappingsJSON, ownGroupsJSON, err := repo.marshalStateJSON(state)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(sessionJSON), "state-token-secret") || !strings.Contains(string(sessionJSON), "enc:v1:") {
		t.Fatalf("session is not encrypted: %s", sessionJSON)
	}
	if strings.Contains(string(mappingsJSON), "enc:v1:") {
		t.Fatalf("mappings should stay readable JSON: %s", mappingsJSON)
	}
	row := fakeStateRow{userID: "u1", adminAccountID: "a1", baseURL: state.BaseURL, email: state.Email,
		session: sessionJSON, mappings: mappingsJSON, ownGroups: ownGroupsJSON}

	got, err := repo.scanState(row)
	if err != nil || got == nil || got.Session.AccessToken != "state-token-secret" || len(got.Mappings) != 1 {
		t.Fatalf("read back: %+v %v", got, err)
	}

	reader := NewRepository(nil)
	reader.SetSecretBox(other)
	got, err = reader.scanState(row)
	if err != nil || got == nil || got.Session.IsAuthenticated() || len(got.Mappings) != 1 || got.Mappings[0].OwnGroup != "vip" {
		t.Fatalf("other key: expected mappings without a session, got %+v %v", got, err)
	}

	// 没有密钥时保存的明文会话，配置密钥后照样能读。
	plain := NewRepository(nil)
	plainSession, plainMappings, plainOwnGroups, err := plain.marshalStateJSON(state)
	if err != nil || !strings.Contains(string(plainSession), "state-token-secret") {
		t.Fatalf("no key should keep plaintext: %s %v", plainSession, err)
	}
	legacy := fakeStateRow{userID: "u1", adminAccountID: "a1", session: plainSession, mappings: plainMappings, ownGroups: plainOwnGroups}
	if got, err := repo.scanState(legacy); err != nil || got == nil || got.Session.RefreshToken != "state-refresh-secret" {
		t.Fatalf("legacy plaintext: %+v %v", got, err)
	}
}
