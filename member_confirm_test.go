package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestMemberManagementRequiresPrimaryAdminPassword(t *testing.T) {
	t.Setenv("JPARK_ADMIN_PASSWORD", "primary-secret-test-only")
	mock, commands := mockAccounts(t)
	base := rootRequest("POST", "/api/admin/confirm", `{"password":"primary-secret-test-only"}`)
	mock.AddResponses(writeOK(), writeOK())
	rec := httptest.NewRecorder()
	requireMemberAdmin(memberConfirmHandler)(rec, base)
	if rec.Code != 200 {
		t.Fatal("confirm failed", rec.Code, rec.Body.String())
	}
	var proof struct {
		Confirmation string `json:"confirmation"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &proof)
	if len(proof.Confirmation) != 43 {
		t.Fatal("invalid proof")
	}
	call := func(cookie *http.Cookie, token string) int {
		req := httptest.NewRequest("GET", "/api/members", nil)
		req.AddCookie(cookie)
		if token != "" {
			req.Header.Set("X-Member-Confirmation", token)
		}
		rr := httptest.NewRecorder()
		requireMemberManagement(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) })(rr, req)
		return rr.Code
	}
	cookie, _ := base.Cookie("jpark_session")
	if call(cookie, "") != 428 || call(cookie, proof.Confirmation) != 204 {
		t.Fatal("API confirmation gate not enforced")
	}
	other := rootRequest("GET", "/api/members", "")
	otherCookie, _ := other.Cookie("jpark_session")
	if call(otherCookie, proof.Confirmation) != 428 {
		t.Fatal("proof transferable to different login session")
	}
	managementGrantMu.Lock()
	g := managementGrants[memberTokenKey(proof.Confirmation)]
	g.Expires = time.Now().Add(-time.Second)
	managementGrants[memberTokenKey(proof.Confirmation)] = g
	managementGrantMu.Unlock()
	if call(cookie, proof.Confirmation) != 428 {
		t.Fatal("expired proof accepted")
	}
	for _, cmd := range *commands {
		if strings.Contains(cmd.String(), "primary-secret-test-only") {
			t.Fatal("password persisted in audit")
		}
	}
}
func TestAdditionalAdminCannotUseOwnPassword(t *testing.T) {
	t.Setenv("JPARK_ADMIN_PASSWORD", "primary-secret-test-only")
	mock, _ := mockAccounts(t)
	a := MemberAccount{Username: "additional.admin", Name: "Other Admin", Role: "admin", Active: true, Version: "v1", PasswordHash: "unused-own-password-hash"}
	req := rootRequest("POST", "/api/admin/confirm", `{"password":"own-admin-password"}`)
	cookie, _ := req.Cookie("jpark_session")
	memberSessionMu.Lock()
	s := memberSessions[memberTokenKey(cookie.Value)]
	s.Username = a.Username
	s.Version = a.Version
	memberSessions[memberTokenKey(cookie.Value)] = s
	memberSessionMu.Unlock()
	mock.AddResponses(accountCursor(a), accountCursor(a), writeOK(), writeOK())
	rec := httptest.NewRecorder()
	requireMemberAdmin(memberConfirmHandler)(rec, req)
	if rec.Code != 401 {
		t.Fatal("additional admin own password accepted", rec.Code, rec.Body.String())
	}
	req = rootRequest("POST", "/api/admin/confirm", `{"password":"primary-secret-test-only"}`)
	cookie, _ = req.Cookie("jpark_session")
	memberSessionMu.Lock()
	s = memberSessions[memberTokenKey(cookie.Value)]
	s.Username = a.Username
	s.Version = a.Version
	memberSessions[memberTokenKey(cookie.Value)] = s
	memberSessionMu.Unlock()
	mock.AddResponses(accountCursor(a), accountCursor(a), writeOK(), writeOK())
	rec = httptest.NewRecorder()
	requireMemberAdmin(memberConfirmHandler)(rec, req)
	if rec.Code != 200 {
		t.Fatal("additional admin with primary password rejected", rec.Code)
	}
}
func TestStaffAndCrossSiteCannotConfirmAdmin(t *testing.T) {
	mock, _ := mockAccounts(t)
	staff := MemberAccount{Username: "staff.test", Role: "staff", Active: true, Version: "v1"}
	req := rootRequest("POST", "/api/admin/confirm", `{"password":"anything"}`)
	cookie, _ := req.Cookie("jpark_session")
	memberSessionMu.Lock()
	s := memberSessions[memberTokenKey(cookie.Value)]
	s.Username = staff.Username
	s.Version = staff.Version
	memberSessions[memberTokenKey(cookie.Value)] = s
	memberSessionMu.Unlock()
	mock.AddResponses(accountCursor(staff))
	rec := httptest.NewRecorder()
	requireMemberAdmin(memberConfirmHandler)(rec, req)
	if rec.Code != 403 {
		t.Fatal("staff can reach confirmation")
	}
	req = rootRequest("POST", "/api/admin/confirm", `{"password":"anything"}`)
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec = httptest.NewRecorder()
	requireMemberAdmin(memberConfirmHandler)(rec, req)
	if rec.Code != 403 {
		t.Fatal("cross-site confirmation allowed")
	}
}
