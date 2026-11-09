package main

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestPMMonthlyIsolationAndLegacyHistory(t *testing.T) {
	oldMonth, oldQuarter, oldMemory := pmMonthCol, pmCol, pmMemory
	defer func() { pmMonthCol = oldMonth; pmCol = oldQuarter; pmMemory = oldMemory }()
	pmMonthCol = nil
	pmCol = nil
	pmMemory = map[string]map[string]PMResult{"2026-Q3": {"ip:1": {Name: "Legacy NVR", Status: "normal", Inspector: "Old Inspector"}}}
	rec := httptest.NewRecorder()
	pmHandler(rec, httptest.NewRequest("POST", "/api/pm?month=2026-09", strings.NewReader(`{"key":"ip:1","name":"NVR","group":"NVR","status":"normal","inspectors":["Person A","Person B"," Person A "],"note":"checked"}`)))
	if rec.Code != 200 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	saved := pmMemory["2026-09"]["ip:1"]
	if len(saved.Inspectors) != 2 || saved.Inspector != "Person A / Person B" {
		t.Fatal("multiple inspectors lost", saved)
	}
	if len(pmMemory["2026-10"]) != 0 || pmMemory["2026-Q3"]["ip:1"].Inspector != "Old Inspector" {
		t.Fatal("month or legacy overwritten")
	}
	rec = httptest.NewRecorder()
	pmHandler(rec, httptest.NewRequest("POST", "/api/pm?quarter=2026-Q3", strings.NewReader(`{}`)))
	if rec.Code != 400 {
		t.Fatal("legacy history mutable")
	}
	rec = httptest.NewRecorder()
	pmHandler(rec, httptest.NewRequest("GET", "/api/pm?quarter=2026-Q3", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "Legacy NVR") {
		t.Fatal("legacy history missing")
	}
	for _, query := range []string{"month=2026-13", "month=2026-9", "quarter=2026-Q9"} {
		rec = httptest.NewRecorder()
		pmHandler(rec, httptest.NewRequest("GET", "/api/pm?"+query, nil))
		if rec.Code != 400 {
			t.Fatal("invalid period accepted", query)
		}
	}
	rec = httptest.NewRecorder()
	pmHandler(rec, httptest.NewRequest("POST", "/api/pm?month=2026-09", strings.NewReader(`{"key":"ip:2","name":"CCTV","status":"normal","inspectors":[]}`)))
	if rec.Code != 400 {
		t.Fatal("normal result accepted without inspector")
	}
}
func TestPMNVRFileTypeAndMetadata(t *testing.T) {
	for _, f := range []struct {
		name, data string
		valid      bool
	}{{"test.pdf", "%PDF-1.4 test", true}, {"test.txt", "S.M.A.R.T. OK", true}, {"test.csv", "test,result\nHDD,OK", true}, {"test.exe", "%PDF-1.4", false}, {"test.pdf", "MZ-executable", false}, {"test.png", "not-a-picture", false}} {
		_, ok := pmFileType(f.name, []byte(f.data))
		if ok != f.valid {
			t.Fatal(f.name, "validation mismatch")
		}
	}
	b, _ := json.Marshal(PMFile{Data: []byte("private-file-body"), Digest: "private-digest", Name: "test.pdf"})
	if strings.Contains(string(b), "private-") {
		t.Fatal("binary or digest leaked")
	}
}
func TestPMNVRAttachmentUploadAndPrivateDownload(t *testing.T) {
	mock, commands := mockAccounts(t)
	oldFiles, oldAssets := pmFileCol, assetSt
	defer func() { pmFileCol = oldFiles; assetSt = oldAssets }()
	pmFileCol = memberCol.Database().Collection("pm_attachments")
	assetSt = AssetState{Assets: []Asset{{ID: 1, AssetID: "NVR-01", Category: "NVR", Name: "NVR", IP: "10.10.33.3", Active: true}}}
	body := bytes.NewBuffer(nil)
	form := multipart.NewWriter(body)
	_ = form.WriteField("uploadId", "11111111-2222-4333-8444-555555555555")
	part, _ := form.CreateFormFile("file", "test-result.pdf")
	_, _ = part.Write([]byte("%PDF-1.4 private-file-body"))
	_ = form.Close()
	// missing upload ID, file count 0, audit intent, file insert, audit completion
	mock.AddResponses(accountCursor(), accountCursor(bson.M{"n": int32(0)}), writeOK(), writeOK(), writeOK())
	rec := httptest.NewRecorder()
	req := rootRequest("POST", "/api/pm/files?month=2026-09&key=ip%3A10.10.33.3", body.String())
	req.Header.Set("Content-Type", form.FormDataContentType())
	requireAuth(pmFilesHandler)(rec, req)
	if rec.Code != 200 {
		t.Fatal("upload failed", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "private-file-body") {
		t.Fatal("file bytes in metadata response")
	}
	var stored PMFile
	for _, cmd := range *commands {
		if cmd.Lookup("insert").Type == 0 {
			continue
		}
		values, _ := cmd.Lookup("documents").Array().Values()
		if cmd.Lookup("insert").StringValue() == "pm_attachments" {
			_ = bson.Unmarshal(values[0].Document(), &stored)
		} else if cmd.Lookup("insert").StringValue() == "activity_log" {
			raw, _ := bson.MarshalExtJSON(cmd, false, false)
			if strings.Contains(string(raw), "private-file-body") {
				t.Fatal("file bytes copied into audit")
			}
		}
	}
	if stored.Month != "2026-09" || stored.Key != "ip:10.10.33.3" || stored.UploadedBy != adminID() || !bytes.Contains(stored.Data, []byte("private-file-body")) {
		t.Fatal("attachment metadata or content lost", stored)
	}
	rec = httptest.NewRecorder()
	requireAuth(pmFilesHandler)(rec, httptest.NewRequest("GET", "/api/pm/files?id="+stored.ID, nil))
	if rec.Code != 401 {
		t.Fatal("public file download allowed")
	}
	mock.AddResponses(accountCursor(stored))
	rec = httptest.NewRecorder()
	requireAuth(pmFilesHandler)(rec, rootRequest("GET", "/api/pm/files?id="+stored.ID, ""))
	if rec.Code != 200 || !strings.Contains(rec.Header().Get("Content-Disposition"), "attachment") || !bytes.Equal(rec.Body.Bytes(), stored.Data) {
		t.Fatal("private file download failed", rec.Code)
	}
	// A lost upload response can be retried with the same ID without inserting another file.
	mock.AddResponses(accountCursor(stored))
	rec = httptest.NewRecorder()
	req = rootRequest("POST", "/api/pm/files?month=2026-09&key=ip%3A10.10.33.3", body.String())
	req.Header.Set("Content-Type", form.FormDataContentType())
	requireAuth(pmFilesHandler)(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatal("idempotent upload retry failed", rec.Code, rec.Body.String())
	}
}

func TestPMFileQuotaTypeAndDeviceGuards(t *testing.T) {
	mock, commands := mockAccounts(t)
	oldFiles, oldAssets := pmFileCol, assetSt
	defer func() { pmFileCol = oldFiles; assetSt = oldAssets }()
	pmFileCol = memberCol.Database().Collection("pm_attachments")
	assetSt = AssetState{Assets: []Asset{{ID: 1, Category: "NVR", Name: "NVR", IP: "10.1.1.1", Active: true}}}
	upload := func(key string, data []byte) *httptest.ResponseRecorder {
		body := bytes.NewBuffer(nil)
		form := multipart.NewWriter(body)
		_ = form.WriteField("uploadId", "11111111-2222-4333-8444-555555555556")
		part, _ := form.CreateFormFile("file", "result.txt")
		_, _ = part.Write(data)
		_ = form.Close()
		req := rootRequest("POST", "/api/pm/files?month=2026-09&key="+key, body.String())
		req.Header.Set("Content-Type", form.FormDataContentType())
		rec := httptest.NewRecorder()
		requireAuth(pmFilesHandler)(rec, req)
		return rec
	}
	if rec := upload("ip:10.1.1.2", []byte("OK")); rec.Code != 400 {
		t.Fatal("file accepted for non-NVR")
	}
	if rec := upload("ip:10.1.1.1", bytes.Repeat([]byte("a"), pmMaxFile+1)); rec.Code != 400 {
		t.Fatal("oversized file accepted", rec.Code)
	}
	mock.AddResponses(accountCursor(), accountCursor(bson.M{"n": int32(5)}))
	if rec := upload("ip:10.1.1.1", []byte("OK")); rec.Code != 400 {
		t.Fatal("sixth attachment accepted", rec.Code)
	}
	mock.AddResponses(accountCursor(), accountCursor(bson.M{"n": int32(0)}), bson.D{{Key: "ok", Value: 0}, {Key: "code", Value: 13}, {Key: "errmsg", Value: "audit write failed"}})
	if rec := upload("ip:10.1.1.1", []byte("OK")); rec.Code != 503 {
		t.Fatal("upload allowed without audit", rec.Code)
	}
	for _, cmd := range *commands {
		if cmd.Lookup("insert").Type != 0 && cmd.Lookup("insert").StringValue() == "pm_attachments" {
			t.Fatal("attachment inserted despite failed guards")
		}
	}
}
