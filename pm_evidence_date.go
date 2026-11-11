package main

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"time"
)

func validPMEvidenceDate(s string, now time.Time) bool {
	d, err := time.Parse("2006-01-02", s)
	return err == nil && d.Format("2006-01-02") == s && s <= now.In(attendanceZone).Format("2006-01-02")
}

// Called while holding pmMu. Dates describe the work/evidence; upload timestamps never change.
func pmUpdateEvidenceDate(w http.ResponseWriter, r *http.Request, ctx context.Context, month string, actor MemberAccount) {
	r.Body = http.MaxBytesReader(w, r.Body, 100000)
	var q struct {
		IDs  []string `json:"ids"`
		Date string   `json:"date"`
	}
	if json.NewDecoder(r.Body).Decode(&q) != nil || len(q.IDs) == 0 || len(q.IDs) > 1000 || !validPMEvidenceDate(q.Date, time.Now()) {
		http.Error(w, "เลือกไฟล์และวันที่จริงที่ไม่เกินวันนี้", 400)
		return
	}
	seen := map[string]bool{}
	for _, id := range q.IDs {
		if !pmFileID.MatchString(id) || seen[id] {
			w.WriteHeader(400)
			return
		}
		seen[id] = true
	}
	filter := bson.M{"month": month, "_id": bson.M{"$in": q.IDs}}
	cur, err := pmFileCol.Find(ctx, filter, options.Find().SetProjection(bson.M{"data": 0, "digest": 0}))
	if err != nil {
		w.WriteHeader(503)
		return
	}
	before := []PMFile{}
	if cur.All(ctx, &before) != nil {
		w.WriteHeader(503)
		return
	}
	if len(before) != len(q.IDs) {
		http.Error(w, "ไฟล์เปลี่ยนไปหรือไม่ได้อยู่ในเดือนนี้ กรุณาโหลดใหม่", 409)
		return
	}
	after := append([]PMFile{}, before...)
	for i := range after {
		after[i].EvidenceDate = q.Date
	}
	audit, err := beginAudit(actor, "PM", "dateAttachment", month, before, after)
	if err != nil {
		http.Error(w, "เก็บ Log ไม่สำเร็จ ยังไม่เปลี่ยนวันที่", 503)
		return
	}
	_, err = pmFileCol.UpdateMany(ctx, filter, bson.M{"$set": bson.M{"evidenceDate": q.Date}})
	outcome := "success"
	if err != nil {
		outcome = "failed"
	}
	logErr := finishAudit(audit, outcome, before, after)
	if err != nil {
		http.Error(w, "เปลี่ยนวันที่ไม่สำเร็จ กรุณาโหลดใหม่เพื่อตรวจผล", 503)
		return
	}
	if logErr != nil {
		w.Header().Set("X-Audit-Warning", "pending")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "files": after})
}
