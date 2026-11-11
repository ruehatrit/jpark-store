package main

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"sort"
	"sync"
	"time"
)

type CameraIdentity struct {
	Channel int    `json:"channel" bson:"channel"`
	IP      string `json:"ip" bson:"ip"`
}
type cameraInventoryDoc struct {
	ID      string           `bson:"_id"`
	Cameras []CameraIdentity `bson:"cameras"`
	Updated time.Time        `bson:"updated"`
}

var cameraInventoryMu sync.Mutex
var cameraInventorySaved string

func saveCameraInventory(cameras []CameraStatus) {
	if mongoClient == nil {
		return
	}
	entries := []CameraIdentity{}
	for _, c := range cameras {
		entries = append(entries, CameraIdentity{c.Channel, c.IP})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Channel < entries[j].Channel })
	b, _ := json.Marshal(entries)
	cameraInventoryMu.Lock()
	defer cameraInventoryMu.Unlock()
	if cameraInventorySaved == string(b) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, err := mongoClient.Database("jpark_store").Collection("cctv_inventory").UpdateOne(ctx, bson.M{"_id": "main"}, bson.M{"$set": bson.M{"cameras": entries, "updated": time.Now().UTC()}}, options.UpdateOne().SetUpsert(true))
	if err == nil {
		cameraInventorySaved = string(b)
	}
}
func registeredCameras(ctx context.Context) ([]CameraIdentity, error) {
	byChannel := map[int]CameraIdentity{}
	var loadErr error
	if mongoClient != nil {
		var doc cameraInventoryDoc
		loadErr = mongoClient.Database("jpark_store").Collection("cctv_inventory").FindOne(ctx, bson.M{"_id": "main"}).Decode(&doc)
		if loadErr == nil {
			for _, c := range doc.Cameras {
				byChannel[c.Channel] = c
			}
		} else if loadErr == mongo.ErrNoDocuments {
			loadErr = nil
		}
	}
	cctvStore.RLock()
	if len(cctvStore.payload.Cameras) > 0 {
		byChannel = map[int]CameraIdentity{}
	}
	for _, c := range cctvStore.payload.Cameras {
		byChannel[c.Channel] = CameraIdentity{c.Channel, c.IP}
	}
	cctvStore.RUnlock()
	if len(byChannel) == 0 && loadErr != nil {
		return nil, loadErr
	}
	out := []CameraIdentity{}
	for _, c := range byChannel {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Channel < out[j].Channel })
	return out, nil
}
func cctvCatalogHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 8*time.Second)
	defer cancel()
	cameras, err := registeredCameras(ctx)
	if err != nil {
		http.Error(w, "โหลดทะเบียนกล้องไม่สำเร็จ", 503)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]interface{}{"cameras": cameras})
}
