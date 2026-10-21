package main

import (
	"context"
	"encoding/json"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net"
	"net/http"
	"strings"
	"time"
)

type cameraNameDoc struct {
	ID   string `bson:"_id"`
	Name string `bson:"name"`
}

func cctvNamesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != "GET" && r.Method != "PUT" {
		http.Error(w, "method not allowed", 405)
		return
	}
	if mongoClient == nil {
		http.Error(w, "MongoDB unavailable", 503)
		return
	}
	col := mongoClient.Database("jpark_store").Collection("cctv_camera_names")
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	if r.Method == "GET" {
		cur, err := col.Find(ctx, bson.D{})
		if err != nil {
			http.Error(w, "database error", 500)
			return
		}
		defer cur.Close(ctx)
		result := map[string]string{}
		for cur.Next(ctx) {
			var d cameraNameDoc
			if cur.Decode(&d) == nil {
				result[d.ID] = d.Name
			}
		}
		if err := cur.Err(); err != nil {
			http.Error(w, "database error", 500)
			return
		}
		_ = json.NewEncoder(w).Encode(result)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	var req struct {
		IP   string `json:"ip"`
		Name string `json:"name"`
	}
	if json.NewDecoder(r.Body).Decode(&req) != nil {
		http.Error(w, "invalid json", 400)
		return
	}
	req.IP = strings.TrimSpace(req.IP)
	req.Name = strings.TrimSpace(req.Name)
	// Restrict keys to private IPv4 addresses; names are labels only.
	var a, b, c, d int
	if _, err := fmt.Sscanf(req.IP, "%d.%d.%d.%d", &a, &b, &c, &d); err != nil || net.ParseIP(req.IP) == nil || a > 255 || b > 255 || c > 255 || d > 255 || !(a == 10 || (a == 172 && b >= 16 && b <= 31) || (a == 192 && b == 168)) {
		http.Error(w, "invalid IP", 400)
		return
	}
	if len([]rune(req.Name)) > 80 || strings.ContainsAny(req.Name, "\r\n\x00") {
		http.Error(w, "invalid name", 400)
		return
	}
	if req.Name == "" {
		_, err := col.DeleteOne(ctx, bson.M{"_id": req.IP})
		if err != nil {
			http.Error(w, "database error", 500)
			return
		}
	}
	if req.Name != "" {
		_, err := col.UpdateOne(ctx, bson.M{"_id": req.IP}, bson.M{"$set": bson.M{"name": req.Name}}, options.UpdateOne().SetUpsert(true))
		if err != nil {
			http.Error(w, "database error", 500)
			return
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
