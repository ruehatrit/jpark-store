package main

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"strings"
	"sync"
	"time"
)

type PMResult struct {
	Key       string `json:"key" bson:"key"`
	Name      string `json:"name" bson:"name"`
	Group     string `json:"group" bson:"group"`
	Status    string `json:"status" bson:"status"`
	Note      string `json:"note" bson:"note"`
	Inspector string `json:"inspector" bson:"inspector"`
	Updated   string `json:"updated" bson:"updated"`
}
type PMDoc struct {
	ID      string              `bson:"_id"`
	Results map[string]PMResult `bson:"results"`
}

var pmMu sync.Mutex
var pmCol *mongo.Collection
var pmMemory = map[string]map[string]PMResult{}

func initPM() {
	if useMongo && mongoClient != nil {
		pmCol = mongoClient.Database("jpark_store").Collection("pm_quarters")
	}
}
func pmQuarter(t time.Time) string {
	return t.Format("2006") + "-Q" + string(rune('0'+(int(t.Month())-1)/3+1))
}
func pmLoad(q string) (map[string]PMResult, error) {
	if pmCol == nil {
		if x, ok := pmMemory[q]; ok {
			return x, nil
		}
		return map[string]PMResult{}, nil
	}
	var d PMDoc
	ctx, c := context.WithTimeout(context.Background(), 8*time.Second)
	defer c()
	err := pmCol.FindOne(ctx, bson.M{"_id": q}).Decode(&d)
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
func pmHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	q := r.URL.Query().Get("quarter")
	if len(q) != 7 || q[4:6] != "-Q" || q[6] < '1' || q[6] > '4' {
		q = pmQuarter(time.Now().In(time.FixedZone("ICT", 7*3600)))
	}
	pmMu.Lock()
	defer pmMu.Unlock()
	results, err := pmLoad(q)
	if err != nil {
		http.Error(w, "database unavailable", 503)
		return
	}
	if r.Method == http.MethodGet {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"quarter": q, "results": results})
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", 405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 8192)
	var x PMResult
	if json.NewDecoder(r.Body).Decode(&x) != nil || len(x.Key) > 160 || len(x.Key) == 0 || len(x.Name) > 200 || len(x.Note) > 2000 || len(x.Inspector) > 120 || !(x.Status == "normal" || x.Status == "abnormal" || x.Status == "pending") || !strings.Contains(x.Key, ":") {
		http.Error(w, "invalid data", 400)
		return
	}
	x.Updated = time.Now().In(time.FixedZone("ICT", 7*3600)).Format(time.RFC3339)
	results[x.Key] = x
	if pmCol != nil {
		ctx, c := context.WithTimeout(context.Background(), 8*time.Second)
		defer c()
		_, err = pmCol.UpdateOne(ctx, bson.M{"_id": q}, bson.M{"$set": bson.M{"results": results}}, options.UpdateOne().SetUpsert(true))
		if err != nil {
			http.Error(w, "save failed", 503)
			return
		}
	} else {
		pmMemory[q] = results
	}
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
