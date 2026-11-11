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
func pmReplaceToken(from, to string, source, dest pmMoveState) string {
	b, _ := json.Marshal(struct {
		From, To            string
		Source, Destination pmMoveState
	}{from, to, source, dest})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}
func pmMoveModeConflict(source, dest pmMoveState, replace bool) error {
	if replace {
		if len(source.Results) == 0 && len(source.Files) == 0 {
			return errors.New("เดือนต้นทางไม่มีข้อมูลให้ย้าย")
		}
		return nil
	}
	return pmMoveConflict(source, dest)
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
		From    string `json:"from"`
		To      string `json:"to"`
		Action  string `json:"action"`
		Token   string `json:"token"`
		Replace bool   `json:"replace"`
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
	if err = pmMoveModeConflict(source, dest, q.Replace); err != nil {
		http.Error(w, err.Error(), 409)
		return
	}
	token := pmMoveToken(q.From, q.To, source)
	if q.Replace {
		token = pmReplaceToken(q.From, q.To, source, dest)
	}
	w.Header().Set("Cache-Control", "no-store")
	if q.Action == "preview" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]interface{}{"from": q.From, "to": q.To, "results": len(source.Results), "files": len(source.Files), "token": token, "replace": q.Replace, "destinationResults": len(dest.Results), "destinationFiles": len(dest.Files)})
		return
	}
	if q.Token != token {
		http.Error(w, "ข้อมูลเปลี่ยนไป กรุณาตรวจตัวอย่างการย้ายใหม่", 409)
		return
	}
	actor, _ := currentMember(r)
	action := "moveMonth"
	if q.Replace {
		action = "replaceMonth"
	}
	archiveID := ""
	if q.Replace && (len(dest.Results) > 0 || len(dest.Files) > 0) {
		archiveID = "pm-" + bson.NewObjectID().Hex()
	}
	before := map[string]interface{}{"source": source, "destination": dest}
	audit, err := beginAudit(actor, "PM", action, q.From+" → "+q.To, before, map[string]interface{}{"from": q.From, "to": q.To, "replace": q.Replace, "archiveID": archiveID})
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
			if e = pmMoveModeConflict(current, target, q.Replace); e != nil {
				return nil, e
			}
			currentToken := pmMoveToken(q.From, q.To, current)
			if q.Replace {
				currentToken = pmReplaceToken(q.From, q.To, current, target)
			}
			if currentToken != q.Token {
				return nil, errors.New("ข้อมูลเปลี่ยนไป กรุณาตรวจตัวอย่างใหม่")
			}
			if archiveID != "" {
				// Keep the old destination and its original file bytes as an archive within this transaction.
				_, e = pmMonthCol.Database().Collection("pm_month_replacements").InsertOne(tx, bson.M{"_id": archiveID, "from": q.From, "destination": q.To, "savedAt": time.Now().UTC(), "actor": actor.Username, "results": target.Results, "files": target.Files})
				if e != nil {
					return nil, e
				}
				archived, e := pmFileCol.UpdateMany(tx, bson.M{"month": q.To}, bson.M{"$set": bson.M{"month": "archive:" + archiveID}})
				if e != nil {
					return nil, e
				}
				if archived.MatchedCount != int64(len(target.Files)) {
					return nil, errors.New("ข้อมูลปลายทางเปลี่ยนไป ยกเลิกการแทนที่")
				}
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
	logErr := finishAudit(audit, outcome, before, map[string]interface{}{"from": q.From, "to": q.To, "outcome": outcome, "replace": q.Replace, "archiveID": archiveID})
	if err != nil {
		http.Error(w, "ย้ายเดือนไม่สำเร็จ กรุณาโหลดข้อมูลใหม่เพื่อตรวจผล (ฐานข้อมูลต้องรองรับ Transaction)", 503)
		return
	}
	if logErr != nil {
		w.Header().Set("X-Audit-Warning", "pending")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "month": q.To, "results": len(source.Results), "files": len(source.Files), "archiveID": archiveID, "replace": q.Replace})
}
