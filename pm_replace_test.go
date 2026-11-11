package main

import (
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPMReplacementBindsDestinationToPreview(t *testing.T) {
	source := pmMoveState{Results: map[string]PMResult{"new": {Name: "New"}}}
	dest := pmMoveState{Results: map[string]PMResult{"old": {Name: "Old"}}}
	if pmMoveModeConflict(source, dest, true) != nil || pmMoveModeConflict(source, dest, false) == nil {
		t.Fatal("replacement mode not explicit")
	}
	token := pmReplaceToken("2026-11", "2026-10", source, dest)
	dest.Files = []PMFile{{ID: "a-new-destination-file"}}
	if token == pmReplaceToken("2026-11", "2026-10", source, dest) {
		t.Fatal("destination changes ignored by preview token")
	}
	if token == pmMoveToken("2026-11", "2026-10", source) {
		t.Fatal("move preview reusable for replacement")
	}
}
func TestPMReplacementArchivesOldMonthWithinTransaction(t *testing.T) {
	for _, fail := range []bool{false, true} {
		t.Run(map[bool]string{false: "commit", true: "rollback"}[fail], func(t *testing.T) {
			mock, commands := mockAccounts(t)
			oldM, oldF := pmMonthCol, pmFileCol
			defer func() { pmMonthCol = oldM; pmFileCol = oldF }()
			pmMonthCol = memberCol.Database().Collection("pm_months")
			pmFileCol = memberCol.Database().Collection("pm_attachments")
			source := pmMoveState{Results: map[string]PMResult{"source-key": {Key: "source-key", Name: "New PM", Status: "normal", Inspectors: []string{"IT"}}}, Files: []PMFile{{ID: "11111111-2222-4333-8444-555555555555", Month: "2026-11", Key: "source-key", Name: "new.jpg", EvidenceDate: "2026-10-15"}}}
			dest := pmMoveState{Results: map[string]PMResult{"destination-key": {Key: "destination-key", Name: "Old PM", Status: "pending"}}, Files: []PMFile{{ID: "11111111-2222-4333-8444-555555555556", Month: "2026-10", Key: "destination-key", Name: "old.jpg"}}}
			fromDoc, toDoc := PMDoc{ID: "2026-11", Results: source.Results}, PMDoc{ID: "2026-10", Results: dest.Results}
			mock.AddResponses(accountCursor(fromDoc), accountCursor(source.Files[0]), accountCursor(toDoc), accountCursor(dest.Files[0]), writeOK(), accountCursor(fromDoc), accountCursor(source.Files[0]), accountCursor(toDoc), accountCursor(dest.Files[0]), writeOK(), writeOK())
			if fail {
				mock.AddResponses(bson.D{{Key: "ok", Value: 0}, {Key: "code", Value: 121}, {Key: "errmsg", Value: "destination write failed"}}, writeOK(), writeOK())
			} else {
				mock.AddResponses(writeOK(), writeOK(), writeOK(), writeOK(), writeOK())
			}
			body, _ := json.Marshal(map[string]interface{}{"action": "commit", "from": "2026-11", "to": "2026-10", "replace": true, "token": pmReplaceToken("2026-11", "2026-10", source, dest)})
			rec := httptest.NewRecorder()
			requireMemberAdmin(pmMoveHandler)(rec, rootRequest("POST", "/api/pm/move", string(body)))
			want := 200
			if fail {
				want = 503
			}
			if rec.Code != want {
				t.Fatal(rec.Code, rec.Body.String())
			}
			archived, oldFilesMoved, targetReplaced, newFilesMoved, deleted, committed, aborted := false, false, false, false, false, false, false
			for _, cmd := range *commands {
				if v := cmd.Lookup("insert"); v.Type != 0 && v.StringValue() == "pm_month_replacements" {
					archived = true
					b, _ := bson.MarshalExtJSON(cmd, false, false)
					if !strings.Contains(string(b), "destination-key") || !strings.Contains(string(b), "old.jpg") {
						t.Fatal("old destination not retained")
					}
				}
				if v := cmd.Lookup("update"); v.Type != 0 && v.StringValue() == "pm_attachments" {
					b, _ := bson.MarshalExtJSON(cmd, false, false)
					s := string(b)
					if strings.Contains(s, "archive:pm-") {
						if !archived {
							t.Fatal("old files moved before archive saved")
						}
						oldFilesMoved = true
					} else {
						if !targetReplaced {
							t.Fatal("source files moved before replacement")
						}
						newFilesMoved = true
					}
					if strings.Contains(s, "uploadedAt") || strings.Contains(s, "evidenceDate") || strings.Contains(s, "\"data\"") {
						t.Fatal("replacement changes file content or actual dates")
					}
				}
				if v := cmd.Lookup("update"); v.Type != 0 && v.StringValue() == "pm_months" {
					if !oldFilesMoved {
						t.Fatal("overwrite without preserving old files")
					}
					targetReplaced = true
					b, _ := bson.MarshalExtJSON(cmd, false, false)
					if strings.Contains(string(b), "destination-key") || !strings.Contains(string(b), "source-key") {
						t.Fatal("destination merged instead of replaced")
					}
				}
				if v := cmd.Lookup("delete"); v.Type != 0 && v.StringValue() == "pm_months" {
					deleted = true
				}
				if cmd.Lookup("commitTransaction").Type != 0 {
					committed = true
				}
				if cmd.Lookup("abortTransaction").Type != 0 {
					aborted = true
				}
			}
			if !archived || !oldFilesMoved {
				t.Fatal("old destination not archived")
			}
			if fail {
				if !aborted || committed || deleted || newFilesMoved {
					t.Fatal("failed replacement not rolled back")
				}
			} else {
				if !committed || aborted || !deleted || !newFilesMoved {
					t.Fatal("replacement did not move complete source")
				}
				if !strings.Contains(rec.Body.String(), "archiveID") {
					t.Fatal("missing archive identity")
				}
			}
		})
	}
}
