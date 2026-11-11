package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"time"
)

type pmMoveState struct {
	Results map[string]PMResult `json:"results"`
	Files   []PMFile            `json:"files"`
}

func validPMMonth(s string) bool {
	d, e := time.Parse("2006-01", s)
	return e == nil && d.Format("2006-01") == s
}
func readPMMoveState(ctx context.Context, month string) (pmMoveState, error) {
	out := pmMoveState{Results: map[string]PMResult{}, Files: []PMFile{}}
	var doc PMDoc
	err := pmMonthCol.FindOne(ctx, bson.M{"_id": month}).Decode(&doc)
	if err != nil && err != mongo.ErrNoDocuments {
		return out, err
	}
	if doc.Results != nil {
		out.Results = doc.Results
	}
	cur, err := pmFileCol.Find(ctx, bson.M{"month": month}, options.Find().SetProjection(bson.M{"data": 0, "digest": 0}).SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return out, err
	}
	err = cur.All(ctx, &out.Files)
	return out, err
}
func pmMoveToken(from, to string, state pmMoveState) string {
	b, _ := json.Marshal(struct {
		From, To string
		State    pmMoveState
	}{from, to, state})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func pmMoveConflict(source, dest pmMoveState) error {
	if len(source.Results) == 0 && len(source.Files) == 0 {
		return errors.New("เดือนต้นทางไม่มีข้อมูลให้ย้าย")
	}
	if len(dest.Results) > 0 || len(dest.Files) > 0 {
		return errors.New("เดือนปลายทางมีผลตรวจหรือไฟล์แนบอยู่แล้ว ยังไม่ย้ายเพื่อป้องกันข้อมูลทับกัน")
	}
	return nil
}
func pmMoveHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if pmMonthCol == nil || pmFileCol == nil {
		http.Error(w, "ต้องเชื่อมฐานข้อมูลเพื่อย้ายเดือนอย่างปลอดภัย", 503)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var q struct {
		From   string `json:"from"`
		To     string `json:"to"`
		Action string `json:"action"`
		Token  string `json:"token"`
	}
	if json.NewDecoder(r.Body).Decode(&q) != nil || !validPMMonth(q.From) || !validPMMonth(q.To) || q.From == q.To || (q.Action != "preview" && q.Action != "commit") {
		http.Error(w, "เลือกเดือนต้นทางและปลายทางให้ต่างกัน", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()
	pmMu.Lock()
	defer pmMu.Unlock()
	source, err := readPMMoveState(ctx, q.From)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	dest, err := readPMMoveState(ctx, q.To)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	if err = pmMoveConflict(source, dest); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	token := pmMoveToken(q.From, q.To, source)
	w.Header().Set("Cache-Control", "no-store")
	if q.Action == "preview" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]interface{}{"from": q.From, "to": q.To, "results": len(source.Results), "files": len(source.Files), "token": token})
		return
	}
	if q.Token != token {
		http.Error(w, "ข้อมูลเปลี่ยนไป กรุณาตรวจตัวอย่างการย้ายใหม่", 409)
		return
	}
	actor, _ := currentMember(r)
	audit, err := beginAudit(actor, "PM", "moveMonth", q.From+" → "+q.To, source, map[string]interface{}{"from": q.From, "to": q.To})
	if err != nil {
		http.Error(w, "เก็บ Log ไม่สำเร็จ ยังไม่ย้ายข้อมูล", 503)
		return
	}
	session, err := pmMonthCol.Database().Client().StartSession()
	if err == nil {
		defer session.EndSession(context.Background())
		_, err = session.WithTransaction(ctx, func(tx context.Context) (interface{}, error) {
			current, e := readPMMoveState(tx, q.From)
			if e != nil {
				return nil, e
			}
			target, e := readPMMoveState(tx, q.To)
			if e != nil {
				return nil, e
			}
			if e = pmMoveConflict(current, target); e != nil {
				return nil, e
			}
			if pmMoveToken(q.From, q.To, current) != q.Token {
				return nil, errors.New("ข้อมูลเปลี่ยนไป กรุณาตรวจตัวอย่างใหม่")
			}
			_, e = pmMonthCol.UpdateOne(tx, bson.M{"_id": q.To}, bson.M{"$set": bson.M{"results": current.Results}}, options.UpdateOne().SetUpsert(true))
			if e != nil {
				return nil, e
			}
			update, e := pmFileCol.UpdateMany(tx, bson.M{"month": q.From}, bson.M{"$set": bson.M{"month": q.To}})
			if e != nil {
				return nil, e
			}
			if update.MatchedCount != int64(len(current.Files)) {
				return nil, errors.New("จำนวนไฟล์เปลี่ยนไป ยกเลิกการย้าย")
			}
			_, e = pmMonthCol.DeleteOne(tx, bson.M{"_id": q.From})
			return nil, e
		})
	}
	outcome := "success"
	if err != nil {
		outcome = "failed"
	}
	logErr := finishAudit(audit, outcome, source, map[string]interface{}{"from": q.From, "to": q.To, "outcome": outcome})
	if err != nil {
		http.Error(w, "ย้ายเดือนไม่สำเร็จ กรุณาโหลดข้อมูลใหม่เพื่อตรวจผล (ฐานข้อมูลต้องรองรับ Transaction)", 503)
		return
	}
	if logErr != nil {
		w.Header().Set("X-Audit-Warning", "pending")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "month": q.To, "results": len(source.Results), "files": len(source.Files)})
}
