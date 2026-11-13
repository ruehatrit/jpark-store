package main

import (
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestITLateMinuteBoundary(t *testing.T) {
	now, _ := time.ParseInLocation("2006-01-02 15:04:05", "2026-11-11 12:00:00", attendanceZone)
	for _, test := range []struct {
		shift, stamp string
		late         bool
	}{
		{"day", "2026-11-09 07:00:00", false}, {"day", "2026-11-09 07:00:32", false}, {"day", "2026-11-09 07:00:59", false}, {"day", "2026-11-09 07:01:00", true},
		{"night", "2026-11-09 19:00:00", false}, {"night", "2026-11-09 19:00:59", false}, {"night", "2026-11-09 19:01:00", true}, {"night", "2026-11-10 00:05:00", true},
	} {
		row := evaluateITDay("2026-11-09", "000549", test.shift, []FingerEvent{{Code: "000549", Time: test.stamp}}, now, now, now.AddDate(0, 0, -10))
		if row.Late != test.late {
			t.Errorf("%s %s late=%v", test.shift, test.stamp, row.Late)
		}
		if (arrivalStatus("2026-11-09", test.shift, test.stamp) == "late") != test.late {
			t.Errorf("arrivalStatus inconsistent: %s", test.stamp)
		}
	}
}
func TestManualLeavePreservesScansAndRoster(t *testing.T) {
	for _, status := range []string{"absent", "late", "present", "future"} {
		row := ITDay{Date: "2026-11-09", Code: "000549", Shift: "night", Status: status, In: "2026-11-09 19:03:00", Late: true, Incomplete: true}
		applyITLeave(&row, EmployeeShift{ManualLeave: true, LeaveNote: "ลาป่วย"})
		if row.Status != "leave" || row.Late || row.Incomplete || row.In == "" || row.Shift != "night" || row.LeaveNote != "ลาป่วย" {
			t.Fatal(row)
		}
	}
	row := ITDay{Status: "absent"}
	applyITLeave(&row, EmployeeShift{})
	if row.Status != "absent" {
		t.Fatal(row)
	}
	// Roster imports use $set with this struct. Omitted fields must not erase saved leave.
	encoded, err := bson.Marshal(EmployeeShift{Shift: "day"})
	if err != nil {
		t.Fatal(err)
	}
	var doc bson.M
	if err = bson.Unmarshal(encoded, &doc); err != nil {
		t.Fatal(err)
	}
	if _, ok := doc["manualLeave"]; ok {
		t.Fatal("roster import erases saved leave")
	}
	if _, ok := doc["leaveNote"]; ok {
		t.Fatal("roster import erases leave note")
	}
	b, _ := json.Marshal(ITDay{ManualLeave: true})
	if !strings.Contains(string(b), `"manualLeave":true`) {
		t.Fatal(string(b))
	}
}
func TestLeaveRequiresAdmin(t *testing.T) {
	req := httptest.NewRequest("POST", "/api/attendance/it-leave", strings.NewReader(`{}`))
	out := httptest.NewRecorder()
	requireMemberAdmin(itLeaveHandler)(out, req)
	if out.Code != 401 && out.Code != 403 {
		t.Fatalf("unauthorized leave mutation: %d", out.Code)
	}
}

func TestLeaveAPISavesOnlyOverrideAndAudits(t *testing.T) {
	mock, commands := mockAccounts(t)
	oldE, oldS := attendanceEmployees, attendanceShifts
	attendanceEmployees = memberCol.Database().Collection("attendance_employees")
	attendanceShifts = memberCol.Database().Collection("attendance_shifts")
	defer func() { attendanceEmployees = oldE; attendanceShifts = oldS }()
	for _, leave := range []bool{true, false} {
		before := EmployeeShift{ID: "2026-11-09:000549", Date: "2026-11-09", Code: "000549", Shift: "day", Source: "roster.xlsx", ManualLeave: !leave}
		mock.AddResponses(accountCursor(EmployeeProfile{Code: "000549", Active: true}), accountCursor(before), writeOK(), writeOK(), writeOK())
		body := `{"date":"2026-11-09","code":"000549","leave":` + map[bool]string{true: "true", false: "false"}[leave] + `,"note":"ลาป่วย"}`
		out := httptest.NewRecorder()
		requireMemberAdmin(itLeaveHandler)(out, rootRequest("POST", "/api/attendance/it-leave", body))
		if out.Code != 200 {
			t.Fatal(out.Code, out.Body.String())
		}
	}
	count := 0
	for _, cmd := range *commands {
		if cmd.Lookup("update").Type == 0 || cmd.Lookup("update").StringValue() != "attendance_shifts" {
			continue
		}
		values, _ := cmd.Lookup("updates").Array().Values()
		set := values[0].Document().Lookup("u").Document().Lookup("$set").Document()
		if set.Lookup("manualLeave").Type == 0 || set.Lookup("shift").Type != 0 || set.Lookup("source").Type != 0 {
			t.Fatalf("unsafe leave update: %v", set)
		}
		count++
	}
	if count != 2 {
		t.Fatal("missing leave/cancel writes", count)
	}
}
