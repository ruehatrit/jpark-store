package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type AuditEvent struct {
	ID       string    `json:"id" bson:"_id"`
	At       time.Time `json:"at" bson:"at"`
	Username string    `json:"username" bson:"username"`
	Name     string    `json:"name" bson:"name"`
	Role     string    `json:"role" bson:"role"`
	Module   string    `json:"module" bson:"module"`
	Action   string    `json:"action" bson:"action"`
	Target   string    `json:"target" bson:"target"`
	Outcome  string    `json:"outcome" bson:"outcome"`
	Before   string    `json:"before" bson:"before"`
	After    string    `json:"after" bson:"after"`
	Request  string    `json:"request" bson:"request"`
}

var auditCol *mongo.Collection
var auditMutationMu sync.Mutex

func initAudit() {
	auditCol = mongoClient.Database("jpark_store").Collection("activity_log")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Index failures do not discard logs; insertion errors block the mutation.
	_, _ = auditCol.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "at", Value: -1}, {Key: "_id", Value: -1}}})
}
func auditJSON(value interface{}) (string, error) {
	if value == nil {
		return "", nil
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(b) > 4<<20 {
		return "", errors.New("audit record too large")
	}
	return string(b), nil
}
func beginAudit(actor MemberAccount, module, action, target string, before, request interface{}) (string, error) {
	if auditCol == nil {
		return "", errors.New("audit database unavailable")
	}
	b, err := auditJSON(before)
	if err != nil {
		return "", err
	}
	q, err := auditJSON(request)
	if err != nil {
		return "", err
	}
	id, err := randomMemberToken()
	if err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_, err = auditCol.InsertOne(ctx, AuditEvent{ID: id, At: time.Now().UTC(), Username: actor.Username, Name: actor.Name, Role: actor.Role, Module: module, Action: action, Target: target, Outcome: "pending", Before: b, Request: q})
	return id, err
}
func finishAudit(id, outcome string, before, after interface{}) error {
	b, err := auditJSON(before)
	if err != nil {
		return err
	}
	a, err := auditJSON(after)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	_, err = auditCol.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$set": bson.M{"outcome": outcome, "before": b, "after": a}})
	return err
}
func writeMemberEvent(username, name, action, outcome string, before, after interface{}) error {
	id, err := beginAudit(MemberAccount{Username: username, Name: name}, "เข้าสู่ระบบ", action, "", before, nil)
	if err != nil {
		return err
	}
	return finishAudit(id, outcome, before, after)
}
func isAuditedPath(path string) bool {
	switch path {
	case "/api/action", "/api/worklog", "/api/daily/action", "/api/asset/action", "/api/issues-extra/action", "/api/pm", "/api/cctv/names":
		return true
	}
	return false
}
func auditModule(path string) string {
	switch path {
	case "/api/action":
		return "คลังอุปกรณ์"
	case "/api/worklog":
		return "บันทึกการทำงาน"
	case "/api/daily/action":
		return "Daily Check"
	case "/api/asset/action":
		return "Asset Register"
	case "/api/issues-extra/action":
		return "งานผิดปกติ / งานค้าง"
	case "/api/pm":
		return "PM"
	case "/api/cctv/names":
		return "ชื่อกล้อง CCTV"
	}
	return path
}
func snapshotAudit(r *http.Request) (interface{}, error) {
	request := r.Clone(r.Context())
	request.Method = http.MethodGet
	request.Body = http.NoBody
	rec := httptest.NewRecorder()
	switch r.URL.Path {
	case "/api/action":
		mu.Lock()
		_ = json.NewEncoder(rec).Encode(st)
		mu.Unlock()
	case "/api/worklog":
		worklogHandler(rec, request)
	case "/api/daily/action":
		dailyStateHandler(rec, request)
	case "/api/asset/action":
		assetStateHandler(rec, request)
	case "/api/issues-extra/action":
		extraState(rec, request)
	case "/api/pm":
		pmHandler(rec, request)
	case "/api/cctv/names":
		cctvNamesHandler(rec, request)
	default:
		return nil, errors.New("unknown audit module")
	}
	if rec.Code >= 400 || rec.Body.Len() > 4<<20 {
		return nil, errors.New("snapshot unavailable")
	}
	var data interface{}
	err := json.Unmarshal(rec.Body.Bytes(), &data)
	return data, err
}

// Keep complete changed records in arrays, and only changed keys in maps.
func auditDifference(before, after interface{}) (interface{}, interface{}) {
	if reflect.DeepEqual(before, after) {
		return nil, nil
	}
	b, bok := before.(map[string]interface{})
	a, aok := after.(map[string]interface{})
	if bok && aok {
		old, new := map[string]interface{}{}, map[string]interface{}{}
		keys := map[string]bool{}
		for k := range b {
			keys[k] = true
		}
		for k := range a {
			keys[k] = true
		}
		for k := range keys {
			left, right := auditDifference(b[k], a[k])
			if left != nil || right != nil {
				old[k] = left
				new[k] = right
			}
		}
		return old, new
	}
	bl, bok := before.([]interface{})
	al, aok := after.([]interface{})
	if bok && aok {
		keyed := func(rows []interface{}) (map[string]interface{}, bool) {
			out := map[string]interface{}{}
			for _, v := range rows {
				m, ok := v.(map[string]interface{})
				if !ok {
					return nil, false
				}
				id, ok := m["id"]
				if !ok {
					return nil, false
				}
				encoded, _ := json.Marshal(id)
				k := string(encoded)
				if _, found := out[k]; found {
					return nil, false
				}
				out[k] = v
			}
			return out, true
		}
		bm, bo := keyed(bl)
		am, ao := keyed(al)
		if bo && ao {
			old, new := map[string]interface{}{}, map[string]interface{}{}
			for k, v := range bm {
				if !reflect.DeepEqual(v, am[k]) {
					old[k] = v
					new[k] = am[k]
				}
			}
			for k, v := range am {
				if _, ok := bm[k]; !ok {
					old[k] = nil
					new[k] = v
				}
			}
			return old, new
		}
	}
	return before, after
}
func runAuditedMutation(w http.ResponseWriter, r *http.Request, actor MemberAccount, next http.HandlerFunc) {
	// Serializing web mutations keeps before/after snapshots paired in this process.
	auditMutationMu.Lock()
	defer auditMutationMu.Unlock()
	body, err := io.ReadAll(io.LimitReader(r.Body, (2<<20)+1))
	r.Body.Close()
	if err != nil || len(body) > 2<<20 {
		http.Error(w, "ข้อมูลมากเกินไป", 413)
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(body))
	var payload map[string]interface{}
	if json.Unmarshal(body, &payload) != nil {
		http.Error(w, "ข้อมูลไม่ถูกต้อง", 400)
		return
	}
	action := r.Method
	if v, ok := payload["action"].(string); ok {
		action = v
	}
	target := ""
	if id, ok := payload["id"]; ok {
		b, _ := json.Marshal(id)
		target = string(b)
	}
	// Membership endpoints have a separate redacted audit path. These business
	// endpoints never need login credentials; strip accidental secret keys.
	request := map[string]interface{}{}
	for k, v := range payload {
		lower := strings.ToLower(k)
		if !strings.Contains(lower, "password") && !strings.Contains(lower, "token") && !strings.Contains(lower, "secret") {
			request[k] = v
		}
	}
	before, err := snapshotAudit(r)
	if err != nil {
		http.Error(w, "อ่านข้อมูลก่อนทำรายการไม่สำเร็จ ยังไม่เปลี่ยนข้อมูล", 503)
		return
	}
	id, err := beginAudit(actor, auditModule(r.URL.Path), action, target, before, request)
	if err != nil {
		http.Error(w, "บันทึก Log ไม่สำเร็จ ยังไม่เปลี่ยนข้อมูล", 503)
		return
	}
	rec := httptest.NewRecorder()
	next(rec, r)
	outcome := "success"
	if rec.Code >= 400 {
		outcome = "failed"
	}
	var result struct {
		OK *bool `json:"ok"`
	}
	if json.Unmarshal(rec.Body.Bytes(), &result) == nil && result.OK != nil && !*result.OK {
		outcome = "failed"
	}
	after, snapshotErr := snapshotAudit(r)
	var auditErr error
	if snapshotErr == nil {
		left, right := auditDifference(before, after)
		auditErr = finishAudit(id, outcome, left, right)
	} else {
		auditErr = snapshotErr
	}
	for k, v := range rec.Header() {
		w.Header()[k] = v
	}
	if auditErr != nil {
		w.Header().Set("X-Audit-Warning", "pending")
	}
	w.WriteHeader(rec.Code)
	_, _ = w.Write(rec.Body.Bytes())
}
func auditHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	if auditCol == nil {
		http.Error(w, "ต้องเชื่อม MongoDB ก่อนดู Log", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	query := bson.M{}
	if user := r.URL.Query().Get("username"); user != "" {
		query["username"] = user
	}
	if module := r.URL.Query().Get("module"); module != "" {
		query["module"] = module
	}
	if outcome := r.URL.Query().Get("outcome"); outcome != "" {
		query["outcome"] = outcome
	}
	dates := bson.M{}
	zone := time.FixedZone("ICT", 7*3600)
	for _, key := range []string{"from", "to"} {
		value := r.URL.Query().Get(key)
		if value == "" {
			continue
		}
		d, err := time.ParseInLocation("2006-01-02", value, zone)
		if err != nil {
			http.Error(w, "วันที่ไม่ถูกต้อง", 400)
			return
		}
		if key == "from" {
			dates["$gte"] = d
		} else {
			dates["$lt"] = d.AddDate(0, 0, 1)
		}
	}
	if len(dates) > 0 {
		query["at"] = dates
	}
	if r.URL.Query().Get("from") != "" && r.URL.Query().Get("to") != "" && r.URL.Query().Get("from") > r.URL.Query().Get("to") {
		http.Error(w, "ช่วงวันที่ไม่ถูกต้อง", 400)
		return
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 || offset > 100000 {
		http.Error(w, "เลขหน้าไม่ถูกต้อง", 400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	cur, err := auditCol.Find(ctx, query, options.Find().SetSort(bson.D{{Key: "at", Value: -1}, {Key: "_id", Value: -1}}).SetSkip(int64(offset)).SetLimit(51))
	if err != nil {
		http.Error(w, "โหลด Log ไม่สำเร็จ", 503)
		return
	}
	defer cur.Close(ctx)
	list := []AuditEvent{}
	if cur.All(ctx, &list) != nil {
		http.Error(w, "โหลด Log ไม่สำเร็จ", 503)
		return
	}
	more := len(list) > 50
	if more {
		list = list[:50]
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"events": list, "hasMore": more, "offset": offset})
}
