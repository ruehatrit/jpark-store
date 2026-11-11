package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type PMInternetTest struct {
	Download *float64 `json:"download,omitempty" bson:"download,omitempty"`
	Upload   *float64 `json:"upload,omitempty" bson:"upload,omitempty"`
	Ping     *float64 `json:"ping,omitempty" bson:"ping,omitempty"`
	TestedAt string   `json:"testedAt" bson:"testedAt"`
	Provider string   `json:"provider" bson:"provider"`
}
type PMResult struct {
	Location   string          `json:"location,omitempty" bson:"location,omitempty"`
	Internet   *PMInternetTest `json:"internet,omitempty" bson:"internet,omitempty"`
	Key        string          `json:"key" bson:"key"`
	Name       string          `json:"name" bson:"name"`
	Group      string          `json:"group" bson:"group"`
	Status     string          `json:"status" bson:"status"`
	Note       string          `json:"note" bson:"note"`
	Inspector  string          `json:"inspector" bson:"inspector"`
	Inspectors []string        `json:"inspectors" bson:"inspectors"`
	Updated    string          `json:"updated" bson:"updated"`
}
type PMDoc struct {
	ID      string              `bson:"_id"`
	Results map[string]PMResult `bson:"results"`
}

var pmMu sync.Mutex
var pmCol, pmMonthCol, pmFileCol *mongo.Collection
var pmMemory = map[string]map[string]PMResult{}

func initPM() {
	if useMongo && mongoClient != nil {
		db := mongoClient.Database("jpark_store")
		pmCol = db.Collection("pm_quarters")
		pmMonthCol = db.Collection("pm_months")
		pmFileCol = db.Collection("pm_attachments")
	}
}
func pmQuarter(t time.Time) string {
	return t.Format("2006") + "-Q" + string(rune('0'+(int(t.Month())-1)/3+1))
}
func pmPeriod(r *http.Request) (string, bool, error) {
	if month := r.URL.Query().Get("month"); month != "" {
		d, err := time.Parse("2006-01", month)
		if err != nil || d.Format("2006-01") != month {
			return "", false, errors.New("เดือนต้องเป็น YYYY-MM")
		}
		return month, false, nil
	}
	if q := r.URL.Query().Get("quarter"); q != "" {
		if len(q) != 7 || q[4:6] != "-Q" || q[6] < '1' || q[6] > '4' {
			return "", true, errors.New("ไตรมาสไม่ถูกต้อง")
		}
		if _, err := time.Parse("2006", q[:4]); err != nil {
			return "", true, err
		}
		return q, true, nil
	}
	return time.Now().In(time.FixedZone("ICT", 7*3600)).Format("2006-01"), false, nil
}
func pmLoad(period string) (map[string]PMResult, error) {
	col := pmMonthCol
	if strings.Contains(period, "-Q") {
		col = pmCol
	}
	if col == nil {
		if v, ok := pmMemory[period]; ok {
			return v, nil
		}
		return map[string]PMResult{}, nil
	}
	var d PMDoc
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	err := col.FindOne(ctx, bson.M{"_id": period}).Decode(&d)
	if err == mongo.ErrNoDocuments {
		return map[string]PMResult{}, nil
	}
	if err != nil {
		return nil, err
	}
	if d.Results == nil {
		d.Results = map[string]PMResult{}
	}
	return d.Results, nil
}
func normalizePMResult(x *PMResult) bool {
	if len(x.Location) > 200 {
		return false
	}
	if t := x.Internet; t != nil {
		if len(t.Provider) > 200 {
			return false
		}
		if t.TestedAt != "" {
			if _, err := time.Parse("2006-01-02T15:04", t.TestedAt); err != nil {
				return false
			}
		}
		for _, v := range []*float64{t.Download, t.Upload, t.Ping} {
			if v != nil && (*v < 0 || *v > 1000000) {
				return false
			}
		}
	}
	if x.Inspectors == nil && strings.TrimSpace(x.Inspector) != "" {
		x.Inspectors = strings.Split(x.Inspector, " / ")
	}
	seen := map[string]bool{}
	people := []string{}
	for _, name := range x.Inspectors {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if len(name) > 200 {
			return false
		}
		if !seen[name] {
			people = append(people, name)
			seen[name] = true
		}
	}
	if len(people) > 20 {
		return false
	}
	x.Inspectors = people
	x.Inspector = strings.Join(people, " / ")
	return len(x.Key) > 0 && len(x.Key) <= 160 && strings.Contains(x.Key, ":") && len(x.Name) > 0 && len(x.Name) <= 200 && len(x.Group) <= 80 && len(x.Note) <= 2000 && (x.Status == "normal" || x.Status == "abnormal" || x.Status == "pending") && (x.Status == "pending" || len(people) > 0)
}
func pmHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	period, legacy, err := pmPeriod(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if legacy && r.Method != http.MethodGet {
		http.Error(w, "ประวัติไตรมาสเดิมอ่านได้อย่างเดียว กรุณาเลือกเดือนเพื่อบันทึก PM ใหม่", 400)
		return
	}
	pmMu.Lock()
	defer pmMu.Unlock()
	results, err := pmLoad(period)
	if err != nil {
		http.Error(w, "โหลดผล PM ไม่สำเร็จ", 503)
		return
	}
	if r.Method == http.MethodGet {
		out := map[string]interface{}{"period": period, "legacy": legacy, "results": results, "durable": pmMonthCol != nil}
		if legacy {
			out["quarter"] = period
		} else {
			out["month"] = period
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16384)
	var x PMResult
	if json.NewDecoder(r.Body).Decode(&x) != nil || !normalizePMResult(&x) {
		http.Error(w, "กรุณาตรวจข้อมูลและเลือกผู้ทำ PM อย่างน้อยหนึ่งคน", 400)
		return
	}
	x.Updated = time.Now().In(time.FixedZone("ICT", 7*3600)).Format(time.RFC3339)
	next := map[string]PMResult{}
	for k, v := range results {
		next[k] = v
	}
	next[x.Key] = x
	if pmMonthCol != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
		defer cancel()
		_, err = pmMonthCol.UpdateOne(ctx, bson.M{"_id": period}, bson.M{"$set": bson.M{"results": next}}, options.UpdateOne().SetUpsert(true))
		if err != nil {
			http.Error(w, "บันทึก PM ไม่สำเร็จ", 503)
			return
		}
	} else {
		pmMemory[period] = next
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "result": x})
}
