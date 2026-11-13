package main

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http"
	"strings"
	"time"
)

// Leave is separate from the roster and raw scans, so it can be undone.
func applyITLeave(row *ITDay, plan EmployeeShift) {
	if !plan.ManualLeave {
		return
	}
	row.ManualLeave = true
	row.LeaveNote = plan.LeaveNote
	row.Status = "leave"
	row.Late = false
	row.Incomplete = false
}

func itLeaveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if attendanceShifts == nil {
		w.WriteHeader(503)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var q struct {
		Date  string `json:"date"`
		Code  string `json:"code"`
		Leave *bool  `json:"leave"`
		Note  string `json:"note"`
	}
	if json.NewDecoder(r.Body).Decode(&q) != nil {
		w.WriteHeader(400)
		return
	}
	d, err := time.Parse("2006-01-02", q.Date)
	q.Code = canonicalEmployeeCode(q.Code)
	q.Note = strings.TrimSpace(q.Note)
	if err != nil || d.Format("2006-01-02") != q.Date || q.Code == "" || q.Leave == nil || len([]rune(q.Note)) > 200 {
		w.WriteHeader(400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	profiles, err := employeeProfiles(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	found := false
	for _, p := range profiles {
		if p.Code == q.Code && p.Active {
			found = true
		}
	}
	if !found {
		http.Error(w, "ไม่พบพนักงานไอที", 400)
		return
	}
	id := q.Date + ":" + q.Code
	var before EmployeeShift
	if err = attendanceShifts.FindOne(ctx, bson.M{"_id": id}).Decode(&before); err != nil {
		http.Error(w, "ไม่พบตารางกะวันที่เลือก", 409)
		return
	}
	if *q.Leave && before.Shift != "day" && before.Shift != "night" {
		http.Error(w, "บันทึกลาได้เฉพาะวันที่มีกะทำงาน", 400)
		return
	}
	after := before
	after.ManualLeave = *q.Leave
	after.LeaveNote = q.Note
	if !*q.Leave {
		after.LeaveNote = ""
	}
	actor, _ := currentMember(r)
	audit, err := beginAudit(actor, "การลาพนักงานไอที", "setLeave", id, before, after)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	result, err := attendanceShifts.UpdateOne(ctx, bson.M{"_id": id, "shift": before.Shift}, bson.M{"$set": bson.M{"manualLeave": after.ManualLeave, "leaveNote": after.LeaveNote}})
	outcome := "success"
	if err != nil || result.MatchedCount != 1 {
		outcome = "failed"
	}
	_ = finishAudit(audit, outcome, before, after)
	if outcome != "success" {
		http.Error(w, "บันทึกไม่สำเร็จ กรุณาโหลดใหม่แล้วลองอีกครั้ง", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
