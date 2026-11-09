package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestMemberSecretsNeverSerialized(t *testing.T) {
	a := MemberAccount{Username: "user", PasswordHash: "HASH-DO-NOT-EXPOSE", Version: "SESSION-REVISION"}
	b, err := json.Marshal(a)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "HASH") || strings.Contains(string(b), "REVISION") {
		t.Fatal("account secrets serialized")
	}
	b, _ = json.Marshal(memberPublic(a))
	if strings.Contains(string(b), "Hash") || strings.Contains(string(b), "version") {
		t.Fatal("account audit contains secrets")
	}
}
func TestPasswordVerifier(t *testing.T) {
	hash, err := bcrypt.GenerateFromPassword([]byte("test-password-123"), 12)
	if err != nil {
		t.Fatal(err)
	}
	if bcrypt.CompareHashAndPassword(hash, []byte("test-password-123")) != nil {
		t.Fatal("correct password rejected")
	}
	if bcrypt.CompareHashAndPassword(hash, []byte("wrong-password")) == nil {
		t.Fatal("incorrect password accepted")
	}
}
func TestSessionExpiryAndLogout(t *testing.T) {
	token, err := randomMemberToken()
	if err != nil {
		t.Fatal(err)
	}
	if len(token) != 43 {
		t.Fatal("unexpected token entropy")
	}
	request := httptest.NewRequest("GET", "http://example.test/api/session", nil)
	request.AddCookie(&http.Cookie{Name: "jpark_session", Value: token})
	memberSessions = map[string]memberSession{memberTokenKey(token): {Username: adminID(), Version: "root", Expires: time.Now().Add(time.Hour)}}
	a, ok := currentMember(request)
	if !ok || a.Role != "admin" {
		t.Fatal("root session missing")
	}
	memberSessions[memberTokenKey(token)] = memberSession{adminID(), "root", time.Now().Add(-time.Second)}
	if _, ok = currentMember(request); ok {
		t.Fatal("expired session accepted")
	}
	memberSessions[memberTokenKey(token)] = memberSession{adminID(), "root", time.Now().Add(time.Hour)}
	post := httptest.NewRequest("POST", "http://example.test/api/logout", nil)
	post.AddCookie(&http.Cookie{Name: "jpark_session", Value: token})
	rec := httptest.NewRecorder()
	logout(rec, post)
	if _, ok = currentMember(request); ok {
		t.Fatal("logged out session accepted")
	}
	// The old shared server-wide cookie must never become a root session.
	request = httptest.NewRequest("GET", "http://example.test/", nil)
	request.AddCookie(&http.Cookie{Name: "jpark_session", Value: "old-shared-session"})
	if isAuthed(request) {
		t.Fatal("legacy cookie accepted")
	}
}
func TestCrossSiteMutation(t *testing.T) {
	r := httptest.NewRequest("POST", "https://example.test/api/action", nil)
	r.Header.Set("Origin", "https://evil.test")
	if sameSiteMutation(r) {
		t.Fatal("cross-site origin accepted")
	}
	r.Header.Set("Origin", "https://example.test")
	if !sameSiteMutation(r) {
		t.Fatal("same-site request rejected")
	}
	r.Header.Set("Sec-Fetch-Site", "cross-site")
	if sameSiteMutation(r) {
		t.Fatal("cross-site fetch accepted")
	}
}
func TestMemberAdminUnauthenticated(t *testing.T) {
	called := false
	handler := requireMemberAdmin(func(w http.ResponseWriter, r *http.Request) { called = true })
	rec := httptest.NewRecorder()
	handler(rec, httptest.NewRequest("POST", "/api/members", nil))
	if called || rec.Code != 401 {
		t.Fatal("unauthenticated membership access")
	}
}
func TestAuditBlocksMutationWithoutDurableLog(t *testing.T) {
	old := auditCol
	auditCol = nil
	defer func() { auditCol = old }()
	called := false
	rec := httptest.NewRecorder()
	runAuditedMutation(rec, httptest.NewRequest("POST", "/api/action", strings.NewReader(`{"action":"delTx","id":5}`)), rootMember(), func(w http.ResponseWriter, r *http.Request) { called = true })
	if called || rec.Code != 503 {
		t.Fatalf("mutation ran without audit: %v %d", called, rec.Code)
	}
}
func TestAuditRetainsDeletedAndChangedRecords(t *testing.T) {
	before := map[string]interface{}{"txs": []interface{}{map[string]interface{}{"id": float64(1), "name": "deleted", "qty": float64(2)}, map[string]interface{}{"id": float64(2), "name": "edited", "qty": float64(4)}, map[string]interface{}{"id": float64(3), "name": "same"}}}
	after := map[string]interface{}{"txs": []interface{}{map[string]interface{}{"id": float64(2), "name": "edited", "qty": float64(5)}, map[string]interface{}{"id": float64(3), "name": "same"}, map[string]interface{}{"id": float64(4), "name": "created"}}}
	b, a := auditDifference(before, after)
	bs, _ := json.Marshal(b)
	as, _ := json.Marshal(a)
	if !strings.Contains(string(bs), "deleted") || !strings.Contains(string(bs), `"qty":4`) || !strings.Contains(string(as), `"qty":5`) || !strings.Contains(string(as), "created") {
		t.Fatal("missing changes", string(bs), string(as))
	}
	if strings.Contains(string(bs), "same") || strings.Contains(string(as), "same") {
		t.Fatal("unchanged record copied")
	}
	b, a = auditDifference(before, before)
	if b != nil || a != nil {
		t.Fatal("no-op reported as change")
	}
	if !reflect.DeepEqual(before["txs"].([]interface{})[0].(map[string]interface{})["name"], "deleted") {
		t.Fatal("snapshot modified")
	}
}
func TestLoginRateLimit(t *testing.T) {
	loginLimits = map[string]loginLimit{}
	for i := 0; i < 10; i++ {
		if !loginAllowed("test") {
			t.Fatal("limited too early")
		}
	}
	if loginAllowed("test") {
		t.Fatal("limit not enforced")
	}
	loginLimits["test"] = loginLimit{10, time.Now().Add(-time.Second)}
	if !loginAllowed("test") {
		t.Fatal("expired rate limit kept")
	}
}
