package main

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/event"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"go.mongodb.org/mongo-driver/v2/x/mongo/driver/drivertest"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func accountCursor(docs ...interface{}) bson.D {
	if docs == nil {
		docs = []interface{}{}
	}
	return bson.D{{Key: "ok", Value: 1}, {Key: "cursor", Value: bson.D{{Key: "id", Value: int64(0)}, {Key: "ns", Value: "jpark_store.login_accounts"}, {Key: "firstBatch", Value: docs}}}}
}
func writeOK() bson.D {
	return bson.D{{Key: "ok", Value: 1}, {Key: "n", Value: 1}, {Key: "nModified", Value: 1}}
}
func mockAccounts(t *testing.T) (*drivertest.MockDeployment, *[]bson.Raw) {
	t.Helper()
	mock := drivertest.NewMockDeployment()
	commands := []bson.Raw{}
	opts := options.Client()
	opts.Deployment = mock
	opts.SetRetryWrites(false)
	opts.SetMonitor(&event.CommandMonitor{Started: func(ctx context.Context, e *event.CommandStartedEvent) {
		commands = append(commands, append(bson.Raw(nil), e.Command...))
	}})
	client, err := mongo.Connect(opts)
	if err != nil {
		t.Fatal(err)
	}
	oldM, oldA := memberCol, auditCol
	memberCol = client.Database("jpark_store").Collection("login_accounts")
	auditCol = client.Database("jpark_store").Collection("activity_log")
	t.Cleanup(func() { memberCol = oldM; auditCol = oldA; _ = client.Disconnect(context.Background()) })
	return mock, &commands
}
func rootRequest(method, path, body string) *http.Request {
	token, _ := randomMemberToken()
	memberSessionMu.Lock()
	memberSessions[memberTokenKey(token)] = memberSession{adminID(), "root", time.Now().Add(time.Hour)}
	memberSessionMu.Unlock()
	r := httptest.NewRequest(method, path, strings.NewReader(body))
	r.AddCookie(&http.Cookie{Name: "jpark_session", Value: token})
	return r
}
func TestCreateMemberLoginAndAdminRestrictions(t *testing.T) {
	mock, commands := mockAccounts(t)
	memberSessions = map[string]memberSession{}
	loginLimits = map[string]loginLimit{}
	mock.AddResponses(accountCursor(), writeOK(), writeOK(), writeOK())
	rec := httptest.NewRecorder()
	requireMemberAdmin(membersHandler)(rec, rootRequest("POST", "/api/members", `{"username":"staff.test","name":"พนักงานทดสอบ","role":"staff","active":true,"password":"member-test-1234"}`))
	if rec.Code != 200 {
		t.Fatal("create failed", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "passwordHash") || strings.Contains(rec.Body.String(), "member-test-1234") {
		t.Fatal("secret in response")
	}
	var stored MemberAccount
	for _, cmd := range *commands {
		collection := cmd.Lookup("insert")
		if collection.Type == 0 {
			continue
		}
		values, _ := cmd.Lookup("documents").Array().Values()
		if collection.StringValue() == "login_accounts" {
			_ = bson.Unmarshal(values[0].Document(), &stored)
		}
		if collection.StringValue() == "activity_log" {
			raw, _ := bson.MarshalExtJSON(cmd, false, false)
			if strings.Contains(string(raw), "member-test-1234") || strings.Contains(string(raw), "passwordHash") {
				t.Fatal("secret in audit")
			}
		}
	}
	if stored.PasswordHash == "" || stored.Version == "" {
		t.Fatal("unsafe account storage")
	}
	mock.AddResponses(accountCursor(stored), writeOK(), writeOK())
	rec = httptest.NewRecorder()
	login(rec, httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"id":"staff.test","password":"member-test-1234"}`)))
	if rec.Code != 200 {
		t.Fatal("login failed", rec.Code, rec.Body.String())
	}
	cookies := rec.Result().Cookies()
	if len(cookies) != 1 || !cookies[0].HttpOnly || cookies[0].SameSite != http.SameSiteLaxMode {
		t.Fatal("unsafe cookie")
	}
	staffRequest := func(path string) *http.Request {
		r := httptest.NewRequest("GET", path, nil)
		r.AddCookie(cookies[0])
		return r
	}
	for _, path := range []string{"/api/members", "/api/audit"} {
		mock.AddResponses(accountCursor(stored))
		rec = httptest.NewRecorder()
		called := false
		requireMemberAdmin(func(w http.ResponseWriter, r *http.Request) { called = true })(rec, staffRequest(path))
		if rec.Code != 403 || called {
			t.Fatal("staff accessed admin API", path, rec.Code)
		}
	}
	mock.AddResponses(accountCursor(stored))
	rec = httptest.NewRecorder()
	called := false
	requireAuth(func(w http.ResponseWriter, r *http.Request) { called = true })(rec, staffRequest("/api/state"))
	if !called {
		t.Fatal("staff denied normal read")
	}
	disabled := stored
	disabled.Active = false
	mock.AddResponses(accountCursor(disabled))
	if isAuthed(staffRequest("/api/state")) {
		t.Fatal("disabled account kept session")
	}
	reset := stored
	reset.Version = "new-revision"
	mock.AddResponses(accountCursor(reset))
	if isAuthed(staffRequest("/api/state")) {
		t.Fatal("reset account kept session")
	}
	mock.AddResponses(accountCursor(stored), writeOK(), writeOK(), writeOK())
	rec = httptest.NewRecorder()
	requireMemberAdmin(membersHandler)(rec, rootRequest("PUT", "/api/members", `{"username":"staff.test","name":"พนักงานทดสอบ","role":"staff","active":false,"password":"reset-test-1234"}`))
	if rec.Code != 200 {
		t.Fatal("disable/reset failed", rec.Code, rec.Body.String())
	}
	var response struct {
		Account MemberAccount `json:"account"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &response)
	if response.Account.Active {
		t.Fatal("disable not saved")
	}
	for _, cmd := range *commands {
		if cmd.Lookup("insert").Type != 0 && cmd.Lookup("insert").StringValue() == "activity_log" {
			raw, _ := bson.MarshalExtJSON(cmd, false, false)
			if strings.Contains(string(raw), "reset-test-1234") {
				t.Fatal("reset password leaked")
			}
		}
	}
}
func TestAuditDeletedContentAndActor(t *testing.T) {
	mock, commands := mockAccounts(t)
	old := st
	defer func() { st = old }()
	st = State{Txs: []Tx{{ID: 88, Name: "deleted-battery-record", Person: "Other Person", Qty: 1}}}
	mock.AddResponses(writeOK(), writeOK())
	rec := httptest.NewRecorder()
	runAuditedMutation(rec, httptest.NewRequest("POST", "/api/action", strings.NewReader(`{"action":"delTx","id":88}`)), MemberAccount{Username: "actual.login", Name: "Actual Actor", Role: "staff"}, func(w http.ResponseWriter, r *http.Request) { st.Txs = []Tx{}; _, _ = w.Write([]byte(`{"ok":true}`)) })
	if rec.Code != 200 || rec.Header().Get("X-Audit-Warning") != "" {
		t.Fatal("audit failed", rec.Code, rec.Header())
	}
	var intent AuditEvent
	var completion bson.M
	for _, cmd := range *commands {
		if cmd.Lookup("insert").Type != 0 {
			values, _ := cmd.Lookup("documents").Array().Values()
			_ = bson.Unmarshal(values[0].Document(), &intent)
		}
		if cmd.Lookup("update").Type != 0 {
			values, _ := cmd.Lookup("updates").Array().Values()
			u := values[0].Document().Lookup("u").Document().Lookup("$set").Document()
			_ = bson.Unmarshal(u, &completion)
		}
	}
	if intent.Username != "actual.login" || intent.Outcome != "pending" || !strings.Contains(intent.Before, "deleted-battery-record") {
		t.Fatal("missing actor or deleted content", intent)
	}
	if completion["outcome"] != "success" || !strings.Contains(completion["before"].(string), "deleted-battery-record") || strings.Contains(completion["after"].(string), "deleted-battery-record") {
		t.Fatal("incorrect deletion log", completion)
	}
}
func TestAuditMongoFailureStopsMutation(t *testing.T) {
	mock, _ := mockAccounts(t)
	mock.AddResponses(bson.D{{Key: "ok", Value: 0}, {Key: "code", Value: 13}, {Key: "errmsg", Value: "not authorized"}})
	called := false
	rec := httptest.NewRecorder()
	runAuditedMutation(rec, httptest.NewRequest("POST", "/api/action", strings.NewReader(`{"action":"delTx","id":88}`)), rootMember(), func(w http.ResponseWriter, r *http.Request) { called = true })
	if rec.Code != 503 || called {
		t.Fatal("audit failure allowed mutation")
	}
}

func TestPendingAuditSurvivesCompletionFailure(t *testing.T) {
	mock, commands := mockAccounts(t)
	old := st
	defer func() { st = old }()
	st = State{Txs: []Tx{{ID: 77, Name: "pending-delete", Person: "Someone"}}}
	mock.AddResponses(writeOK(), bson.D{{Key: "ok", Value: 0}, {Key: "code", Value: 13}, {Key: "errmsg", Value: "write rejected"}})
	rec := httptest.NewRecorder()
	runAuditedMutation(rec, httptest.NewRequest("POST", "/api/action", strings.NewReader(`{"action":"delTx","id":77}`)), rootMember(), func(w http.ResponseWriter, r *http.Request) { st.Txs = []Tx{}; _, _ = w.Write([]byte(`{"ok":true}`)) })
	if rec.Code != 200 || rec.Header().Get("X-Audit-Warning") != "pending" {
		t.Fatal("missing pending warning", rec.Code, rec.Header())
	}
	var intent AuditEvent
	values, _ := (*commands)[0].Lookup("documents").Array().Values()
	_ = bson.Unmarshal(values[0].Document(), &intent)
	if intent.Outcome != "pending" || !strings.Contains(intent.Before, "pending-delete") {
		t.Fatal("intent did not preserve pre-delete state")
	}
}

func TestCannotChangePrimaryOrDisableCurrentAdmin(t *testing.T) {
	t.Setenv("JPARK_ADMIN_ID", "admin")
	mock, _ := mockAccounts(t)
	_ = mock
	rec := httptest.NewRecorder()
	membersHandler(rec, rootRequest("PUT", "/api/members", `{"username":"admin","name":"Root","role":"staff","active":false}`))
	if rec.Code != 400 {
		t.Fatal("primary account mutable", rec.Code)
	}
}
