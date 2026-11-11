package main

import (
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestPMEvidenceDatesAndMoveGuards(t *testing.T) {
	now := time.Date(2026, 11, 11, 18, 0, 0, 0, time.UTC)
	if !validPMEvidenceDate("2026-10-15", now) || !validPMEvidenceDate("2026-11-12", now) {
		t.Fatal("backdate / Bangkok date rejected")
	}
	for _, s := range []string{"2026-11-13", "2026-02-30", "11/10/2026", "2026-1-01", ""} {
		if validPMEvidenceDate(s, now) {
			t.Fatal("invalid/future evidence date accepted", s)
		}
	}
	for _, s := range []string{"2026-1", "2026-13", "invalid"} {
		if validPMMonth(s) {
			t.Fatal("invalid month accepted", s)
		}
	}
	source := pmMoveState{Results: map[string]PMResult{"ip:1": {Name: "NVR"}}, Files: []PMFile{{ID: "file1"}}}
	if pmMoveConflict(source, pmMoveState{}) != nil {
		t.Fatal("empty target blocked")
	}
	if pmMoveConflict(source, pmMoveState{Files: []PMFile{{ID: "other"}}}) == nil || pmMoveConflict(pmMoveState{}, pmMoveState{}) == nil {
		t.Fatal("overwrite/empty source permitted")
	}
	hash := pmMoveToken("2026-11", "2026-10", source)
	source.Files[0].EvidenceDate = "2026-10-15"
	if hash == pmMoveToken("2026-11", "2026-10", source) {
		t.Fatal("stale preview token accepted")
	}
}

func TestPMInternetValuesSurviveSaveAndLoad(t *testing.T) {
	old := pmMonthCol
	pmMonthCol = nil
	defer func() { pmMonthCol = old; delete(pmMemory, "2026-08") }()
	rec := httptest.NewRecorder()
	pmHandler(rec, httptest.NewRequest("POST", "/api/pm?month=2026-08", strings.NewReader(`{"key":"internet:office","name":"ทดสอบอินเทอร์เน็ต","group":"อุปกรณ์ลานจอด","location":"ออฟฟิศ","status":"normal","inspectors":["IT"],"internet":{"download":250.5,"upload":150,"ping":0,"testedAt":"2026-08-15T10:30","provider":"LAN"}}`)))
	if rec.Code != 200 {
		t.Fatal("save internet result failed", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	pmHandler(rec, httptest.NewRequest("GET", "/api/pm?month=2026-08", nil))
	var out struct {
		Results map[string]PMResult `json:"results"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	x := out.Results["internet:office"].Internet
	if x == nil || x.Download == nil || *x.Download != 250.5 || x.Ping == nil || *x.Ping != 0 || x.Provider != "LAN" {
		t.Fatal("saved internet fields lost", rec.Body.String())
	}
}
func TestPMUpdateDatePreservesUploadAndBody(t *testing.T) {
	mock, commands := mockAccounts(t)
	old := pmFileCol
	defer func() { pmFileCol = old }()
	pmFileCol = memberCol.Database().Collection("pm_attachments")
	uploaded := time.Date(2026, 11, 11, 3, 0, 0, 0, time.UTC)
	f := PMFile{ID: "11111111-2222-4333-8444-555555555555", Month: "2026-11", Key: "point:B1 A15", Name: "photo.jpg", UploadedAt: uploaded}
	mock.AddResponses(accountCursor(f), writeOK(), writeOK(), writeOK())
	rec := httptest.NewRecorder()
	requireAuth(pmFilesHandler)(rec, rootRequest("PATCH", "/api/pm/files?month=2026-11", `{"ids":["11111111-2222-4333-8444-555555555555"],"date":"2026-10-15"}`))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var out struct {
		Files []PMFile `json:"files"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if len(out.Files) != 1 || out.Files[0].EvidenceDate != "2026-10-15" || !out.Files[0].UploadedAt.Equal(uploaded) {
		t.Fatal("dates not preserved", out)
	}
	for _, cmd := range *commands {
		if v := cmd.Lookup("update"); v.Type != 0 && v.StringValue() == "pm_attachments" {
			b, _ := bson.MarshalExtJSON(cmd, false, false)
			s := string(b)
			if strings.Contains(s, "uploadedAt") || strings.Contains(s, "\"data\"") || !strings.Contains(s, "evidenceDate") {
				t.Fatal("date patch changes file content/upload stamp", s)
			}
		}
	}
}
func TestPMMoveMonthTransaction(t *testing.T) {
	mock, commands := mockAccounts(t)
	oldM, oldF := pmMonthCol, pmFileCol
	defer func() { pmMonthCol = oldM; pmFileCol = oldF }()
	pmMonthCol = memberCol.Database().Collection("pm_months")
	pmFileCol = memberCol.Database().Collection("pm_attachments")
	state := pmMoveState{Results: map[string]PMResult{"asset:1": {Key: "asset:1", Name: "NVR", Status: "normal", Inspectors: []string{"IT"}}}, Files: []PMFile{{ID: "11111111-2222-4333-8444-555555555555", Month: "2026-11", Key: "asset:1", Name: "result.txt", EvidenceDate: "2026-10-15"}}}
	doc := PMDoc{ID: "2026-11", Results: state.Results}
	// Inspect the same source/target first outside and then inside the transaction.
	mock.AddResponses(accountCursor(doc), accountCursor(state.Files[0]), accountCursor(), accountCursor(), writeOK(), accountCursor(doc), accountCursor(state.Files[0]), accountCursor(), accountCursor(), writeOK(), writeOK(), writeOK(), writeOK(), writeOK())
	body, _ := json.Marshal(map[string]string{"action": "commit", "from": "2026-11", "to": "2026-10", "token": pmMoveToken("2026-11", "2026-10", state)})
	rec := httptest.NewRecorder()
	requireMemberAdmin(pmMoveHandler)(rec, rootRequest("POST", "/api/pm/move", string(body)))
	if rec.Code != 200 {
		t.Fatal("move failed", rec.Code, rec.Body.String())
	}
	commit, updateFiles, deleteSource := false, false, false
	for _, cmd := range *commands {
		if cmd.Lookup("commitTransaction").Type != 0 {
			commit = true
		}
		if v := cmd.Lookup("update"); v.Type != 0 && v.StringValue() == "pm_attachments" {
			b, _ := bson.MarshalExtJSON(cmd, false, false)
			s := string(b)
			if !strings.Contains(s, "2026-10") || strings.Contains(s, "uploadedAt") || strings.Contains(s, "evidenceDate") {
				t.Fatal("move changes evidence", s)
			}
			updateFiles = true
		}
		if v := cmd.Lookup("delete"); v.Type != 0 && v.StringValue() == "pm_months" {
			deleteSource = true
		}
	}
	if !commit || !updateFiles || !deleteSource {
		t.Fatal("move not transactional", commit, updateFiles, deleteSource)
	}
}

func TestPMMoveAbortsWithoutDeletingSource(t *testing.T) {
	mock, commands := mockAccounts(t)
	oldM, oldF := pmMonthCol, pmFileCol
	defer func() { pmMonthCol = oldM; pmFileCol = oldF }()
	pmMonthCol = memberCol.Database().Collection("pm_months")
	pmFileCol = memberCol.Database().Collection("pm_attachments")
	source := pmMoveState{Results: map[string]PMResult{"asset:1": {Key: "asset:1", Name: "NVR", Status: "pending"}}, Files: []PMFile{}}
	doc := PMDoc{ID: "2026-11", Results: source.Results}
	failure := bson.D{{Key: "ok", Value: 0}, {Key: "code", Value: 121}, {Key: "errmsg", Value: "test destination write failure"}}
	mock.AddResponses(accountCursor(doc), accountCursor(), accountCursor(), accountCursor(), writeOK(), accountCursor(doc), accountCursor(), accountCursor(), accountCursor(), failure, writeOK(), writeOK())
	body, _ := json.Marshal(map[string]string{"action": "commit", "from": "2026-11", "to": "2026-10", "token": pmMoveToken("2026-11", "2026-10", source)})
	rec := httptest.NewRecorder()
	requireMemberAdmin(pmMoveHandler)(rec, rootRequest("POST", "/api/pm/move", string(body)))
	if rec.Code != 503 {
		t.Fatal("failed transaction not reported", rec.Code, rec.Body.String())
	}
	aborted := false
	for _, cmd := range *commands {
		if cmd.Lookup("abortTransaction").Type != 0 {
			aborted = true
		}
		if cmd.Lookup("delete").Type != 0 {
			t.Fatal("source deletion attempted after destination failed")
		}
		if cmd.Lookup("commitTransaction").Type != 0 {
			t.Fatal("failed transaction committed")
		}
	}
	if !aborted {
		t.Fatal("transaction not aborted")
	}
}

func TestPMMoveRefusesOccupiedMonth(t *testing.T) {
	mock, commands := mockAccounts(t)
	oldM, oldF := pmMonthCol, pmFileCol
	defer func() { pmMonthCol = oldM; pmFileCol = oldF }()
	pmMonthCol = memberCol.Database().Collection("pm_months")
	pmFileCol = memberCol.Database().Collection("pm_attachments")
	mock.AddResponses(accountCursor(PMDoc{ID: "2026-11", Results: map[string]PMResult{"ip:1": {Name: "NVR"}}}), accountCursor(), accountCursor(), accountCursor(PMFile{ID: "existing"}))
	rec := httptest.NewRecorder()
	requireMemberAdmin(pmMoveHandler)(rec, rootRequest("POST", "/api/pm/move", `{"action":"preview","from":"2026-11","to":"2026-10"}`))
	if rec.Code != 409 {
		t.Fatal("occupied target accepted", rec.Code)
	}
	for _, cmd := range *commands {
		for _, op := range []string{"update", "delete", "insert"} {
			if cmd.Lookup(op).Type != 0 {
				t.Fatal("preview modified data")
			}
		}
	}
}
