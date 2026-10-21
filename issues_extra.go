package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

type ExtraIssue struct {
	ID        string `json:"id" bson:"id"`
	Source    string `json:"source" bson:"source"`
	Title     string `json:"title" bson:"title"`
	Location  string `json:"location" bson:"location"`
	Note      string `json:"note" bson:"note"`
	Status    string `json:"status" bson:"status"`
	Assignee  string `json:"assignee" bson:"assignee"`
	CreatedAt string `json:"createdAt" bson:"createdAt"`
	UpdatedAt string `json:"updatedAt" bson:"updatedAt"`
	Recovered bool   `json:"recovered" bson:"recovered"`
}

var extraMu sync.Mutex
var extraIssues = []ExtraIssue{}
var extraCol *mongo.Collection

const extraFile = "/tmp/jpark-extra-issues.json" // Ephemeral fallback; configure MongoDB for durable records.
func extraInit() {
	if useMongo && mongoClient != nil {
		extraCol = mongoClient.Database("jpark_store").Collection("extra_issues")
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		cur, err := extraCol.Find(ctx, bson.M{})
		if err == nil {
			defer cur.Close(ctx)
			var list []ExtraIssue
			if cur.All(ctx, &list) == nil {
				extraIssues = list
				return
			}
		}
	}
	b, err := os.ReadFile(extraFile)
	if err == nil {
		_ = json.Unmarshal(b, &extraIssues)
	}
}
func extraSave() {
	if extraCol != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
		defer cancel()
		for _, it := range extraIssues {
			_, err := extraCol.UpdateOne(ctx, bson.M{"id": it.ID}, bson.M{"$set": it}, options.UpdateOne().SetUpsert(true))
			_ = err
		}
		return
	}
	b, _ := json.MarshalIndent(extraIssues, "", "  ")
	_ = os.WriteFile(extraFile, b, 0600)
}
func validStatus(s string) bool {
	switch s {
	case "รอดำเนินการ", "รับเรื่องแล้ว", "แจ้ง PM แล้ว - รอดำเนินการ", "PM กำลังดำเนินการ", "รออะไหล่", "ซ่อมเสร็จแล้ว", "ปิดงานแล้ว":
		return true
	}
	return false
}
func isDone(s string) bool {
	return s == "ซ่อมเสร็จแล้ว" || s == "ปิดงานแล้ว"
}
func extraState(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	extraMu.Lock()
	defer extraMu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"issues": extraIssues, "durable": extraCol != nil})
}
func extraAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
	var q struct {
		Action   string `json:"action"`
		ID       string `json:"id"`
		Title    string `json:"title"`
		Location string `json:"location"`
		Note     string `json:"note"`
		Status   string `json:"status"`
		Assignee string `json:"assignee"`
	}
	if json.NewDecoder(r.Body).Decode(&q) != nil {
		http.Error(w, "invalid", 400)
		return
	}
	extraMu.Lock()
	defer extraMu.Unlock()
	now := time.Now().Format(time.RFC3339)
	switch q.Action {
	case "create":
		if len(strings.TrimSpace(q.Title)) < 3 || len(q.Title) > 180 || len(q.Note) > 2000 || len(q.Location) > 180 {
			http.Error(w, "invalid title", 400)
			return
		}
		extraIssues = append(extraIssues, ExtraIssue{ID: fmt.Sprintf("MAN-%d", time.Now().UnixNano()), Source: "ทั่วไป", Title: strings.TrimSpace(q.Title), Location: q.Location, Note: q.Note, Status: "รอดำเนินการ", Assignee: q.Assignee, CreatedAt: now, UpdatedAt: now})
	case "update":
		if !validStatus(q.Status) || len(q.Note) > 2000 || len(q.Assignee) > 180 {
			http.Error(w, "invalid status", 400)
			return
		}
		found := false
		for i := range extraIssues {
			if extraIssues[i].ID == q.ID {
				extraIssues[i].Status = q.Status
				extraIssues[i].Note = q.Note
				extraIssues[i].Assignee = q.Assignee
				extraIssues[i].UpdatedAt = now
				found = true
				break
			}
		}
		if !found {
			http.Error(w, "not found", 404)
			return
		}
	default:
		http.Error(w, "invalid action", 400)
		return
	}
	extraSave()
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}
func extraSyncCCTV(cams []CameraStatus) {
	extraMu.Lock()
	defer extraMu.Unlock()
	now := time.Now().Format(time.RFC3339)
	changed := false
	for _, cam := range cams {
		id := fmt.Sprintf("CCTV-%02d", cam.Channel)
		idx := -1
		for i := range extraIssues {
			if extraIssues[i].ID == id {
				idx = i
				break
			}
		}
		if !cam.Online {
			if idx < 0 {
				extraIssues = append(extraIssues, ExtraIssue{ID: id, Source: "CCTV", Title: fmt.Sprintf("Camera %02d Offline", cam.Channel), Location: cam.IP, Note: "ตรวจพบกล้อง Offline จาก Hikvision", Status: "รอดำเนินการ", CreatedAt: now, UpdatedAt: now})
				changed = true
			} else if isDone(extraIssues[idx].Status) {
				extraIssues[idx].Status = "รอดำเนินการ"
				extraIssues[idx].Recovered = false
				extraIssues[idx].CreatedAt = now
				extraIssues[idx].UpdatedAt = now
				changed = true
			} else if extraIssues[idx].Recovered {
				extraIssues[idx].Recovered = false
				changed = true
			}
		} else if idx >= 0 && !extraIssues[idx].Recovered {
			extraIssues[idx].Recovered = true
			changed = true
		}
	}
	if changed {
		extraSave()
	}
}
