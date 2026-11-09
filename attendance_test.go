package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
)

func TestEmployeeIdentityAndCutoff(t *testing.T) {
	profiles := initialEmployeeProfiles()
	if len(profiles) != 16 {
		t.Fatal("missing employee profiles")
	}
	seen := map[string]EmployeeProfile{}
	for _, p := range profiles {
		if _, ok := seen[p.Code]; ok {
			t.Fatal("duplicate employee code")
		}
		seen[p.Code] = p
		if p.Phone != "" {
			t.Fatal("phone numbers embedded in website source")
		}
	}
	if seen["000520"].Name != "นางสาวรัชฎาภรณ์ ศรีกุล" || seen["000562"].Name != "นายเอกชน หนูน้อย" {
		t.Fatal("520/562 identity mismatch")
	}
	for _, test := range []struct{ shift, stamp, want string }{{"day", "2026-11-09 06:59:59", "ontime"}, {"day", "2026-11-09 07:00:00", "late"}, {"night", "2026-11-09 18:59:59", "ontime"}, {"night", "2026-11-09 19:00:00", "late"}, {"night", "2026-11-10 00:05:00", "late"}, {"unassigned", "2026-11-09 07:30:00", "unassigned"}, {"day", "", "missing"}, {"off", "", "off"}} {
		if got := arrivalStatus("2026-11-09", test.shift, test.stamp); got != test.want {
			t.Fatal(test, got)
		}
	}
	day, _ := time.ParseInLocation("2006-01-02", "2026-11-09", attendanceZone)
	events := []FingerEvent{{Code: "000526", Time: "2026-11-09 07:00:00"}, {Code: "000526", Time: "2026-11-10 00:05:00"}, {Code: "000526", Time: "2026-11-09 18:50:00"}, {Code: "000526", Time: "2026-11-10 07:00:00"}}
	first, last, count := scanSummary(day, "000526", "night", events)
	if first != "2026-11-09 18:50:00" || last != "2026-11-10 00:05:00" || count != 2 {
		t.Fatal("night arrival mixed with morning exit", first, last, count)
	}
}
func TestFingerprintIngestAuthenticatedAndIdempotent(t *testing.T) {
	mock, commands := mockAccounts(t)
	oldE, oldD, oldP := attendanceEvents, attendanceDevices, attendanceEmployees
	defer func() { attendanceEvents = oldE; attendanceDevices = oldD; attendanceEmployees = oldP }()
	db := memberCol.Database()
	attendanceEvents = db.Collection("attendance_events")
	attendanceDevices = db.Collection("attendance_devices")
	attendanceEmployees = db.Collection("attendance_employees")
	token, _ := randomMemberToken()
	sum := sha256.Sum256([]byte(token))
	device := FingerDevice{ID: attendanceDeviceID, Enabled: true, KeyHash: hex.EncodeToString(sum[:])}
	person := EmployeeProfile{Code: "000520", Name: "นางสาวรัชฎาภรณ์ ศรีกุล", Active: true}
	body := `{"device_id":"thai01-main","read_ok":true,"machine_count":14889,"records":[{"code":"520","time":"2026-11-09 06:55:00","status":1,"punch":0}]}`
	ids := []string{}
	for i := 0; i < 2; i++ {
		mock.AddResponses(accountCursor(device), accountCursor(person), writeOK(), writeOK())
		r := httptest.NewRequest("POST", "/api/attendance/ingest", strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		fingerIngest(rec, r)
		if rec.Code != 200 {
			t.Fatal("ingest failed", rec.Code, rec.Body.String())
		}
		var result struct {
			Accepted []string `json:"accepted_ids"`
		}
		_ = json.Unmarshal(rec.Body.Bytes(), &result)
		if len(result.Accepted) != 1 {
			t.Fatal("missing acknowledgement")
		}
		ids = append(ids, result.Accepted[0])
	}
	if ids[0] != ids[1] || ids[0] != fingerEventID(attendanceDeviceID, "000520", "2026-11-09 06:55:00", 1, 0) {
		t.Fatal("retry is not idempotent")
	}
	var writes []bson.Raw
	for _, cmd := range *commands {
		if cmd.Lookup("update").Type != 0 && cmd.Lookup("update").StringValue() == "attendance_events" {
			values, _ := cmd.Lookup("updates").Array().Values()
			for _, v := range values {
				writes = append(writes, v.Document())
			}
		}
	}
	if len(writes) != 2 {
		t.Fatal("missing upserts")
	}
	for _, w := range writes {
		if !w.Lookup("upsert").Boolean() || w.Lookup("u").Document().Lookup("$setOnInsert").Type == 0 {
			t.Fatal("retry could overwrite existing event")
		}
	}
	rec := httptest.NewRecorder()
	fingerIngest(rec, httptest.NewRequest("POST", "/api/attendance/ingest", strings.NewReader(body)))
	if rec.Code != 401 {
		t.Fatal("anonymous ingestion allowed")
	}
	// A collector key grants no employee-directory or administrator session.
	r := httptest.NewRequest("GET", "/api/attendance", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	called := false
	requireAuth(func(w http.ResponseWriter, r *http.Request) { called = true })(rec, r)
	if called || rec.Code != 401 {
		t.Fatal("collector key escalated to website access")
	}
}
func TestCollectorSecretsNotInPublicStatus(t *testing.T) {
	text, _ := json.Marshal(publicFingerDevice(FingerDevice{KeyHash: "SECRET-HASH", Enabled: true}))
	if strings.Contains(string(text), "SECRET") {
		t.Fatal("collector verifier exposed")
	}
}

func TestCollectorConfigurationSecretIsOneTime(t *testing.T) {
	mock, commands := mockAccounts(t)
	oldD, oldP := attendanceDevices, attendanceEmployees
	defer func() { attendanceDevices = oldD; attendanceEmployees = oldP }()
	attendanceDevices = memberCol.Database().Collection("attendance_devices")
	attendanceEmployees = memberCol.Database().Collection("attendance_employees")
	profiles := []interface{}{}
	for _, p := range initialEmployeeProfiles() {
		profiles = append(profiles, p)
	}
	mock.AddResponses(accountCursor(), writeOK(), writeOK(), writeOK(), accountCursor(profiles...))
	rec := httptest.NewRecorder()
	requireMemberAdmin(attendanceDeviceHandler)(rec, rootRequest("POST", "/api/attendance/device", `{"action":"issue_key"}`))
	if rec.Code != 200 {
		t.Fatal("issue key failed", rec.Code, rec.Body.String())
	}
	var response struct {
		Config struct {
			APIKey    string            `json:"api_key"`
			Employees map[string]string `json:"employees"`
		} `json:"config"`
	}
	_ = json.Unmarshal(rec.Body.Bytes(), &response)
	if len(response.Config.APIKey) != 43 || len(response.Config.Employees) != 16 || response.Config.Employees["000520"] != "นางสาวรัชฎาภรณ์ ศรีกุล" {
		t.Fatal("wrong collector config")
	}
	for _, cmd := range *commands {
		raw, _ := bson.MarshalExtJSON(cmd, false, false)
		if strings.Contains(string(raw), response.Config.APIKey) {
			t.Fatal("raw agent key persisted to Mongo or audit")
		}
	}
	if fingerEventID(attendanceDeviceID, "000520", "2026-11-09 06:55:00", 1, 0) != "84731633214a76e2ccc54029d25009db55063e2edb472ec1243ef05b471ef3e7" {
		t.Fatal("Python/Go fingerprint event ID mismatch")
	}
}
