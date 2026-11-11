package main

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"sort"
	"strings"
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

type cameraPoint struct {
	Key      string `json:"key" bson:"_id"`
	Location string `json:"location" bson:"location"`
	Scope    string `json:"scope" bson:"scope"`
}

func cameraPointHandler(w http.ResponseWriter, r *http.Request) {
	if mongoClient == nil {
		http.Error(w, "ต้องเชื่อมฐานข้อมูลเพื่อเก็บจุดกล้อง", 503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	col := mongoClient.Database("jpark_store").Collection("pm_camera_points")
	read := func() (map[string]cameraPoint, error) {
		cur, e := col.Find(ctx, bson.D{})
		if e != nil {
			return nil, e
		}
		docs := []cameraPoint{}
		if e = cur.All(ctx, &docs); e != nil {
			return nil, e
		}
		out := map[string]cameraPoint{}
		for _, d := range docs {
			out[d.Key] = d
		}
		return out, nil
	}
	if r.Method == http.MethodGet {
		points, e := read()
		if e != nil {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]interface{}{"points": points})
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 65536)
	var input struct {
		Points []cameraPoint `json:"points"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Points) > 100 || len(input.Points) == 0 {
		w.WriteHeader(400)
		return
	}
	inventory, e := registeredCameras(ctx)
	if e != nil {
		w.WriteHeader(503)
		return
	}
	valid := map[string]bool{}
	for _, c := range inventory {
		valid["ip:"+c.IP] = true
	}
	assetMu.Lock()
	for _, a := range assetSt.Assets {
		if a.Active && a.IP != "" {
			valid["ip:"+a.IP] = true
		}
	}
	assetMu.Unlock()
	seen := map[string]bool{}
	models := []mongo.WriteModel{}
	for i := range input.Points {
		x := &input.Points[i]
		x.Location = strings.Join(strings.Fields(x.Location), " ")
		if !valid[x.Key] || seen[x.Key] || len(x.Location) > 140 || strings.ContainsAny(x.Location, "\x00\r\n") || (x.Scope != "" && x.Scope != "point" && x.Scope != "device") || (x.Scope == "point" && x.Location == "") {
			http.Error(w, "เลือกกล้องและจุดจากทะเบียนให้ถูกต้อง", 400)
			return
		}
		seen[x.Key] = true
		if x.Scope == "" {
			models = append(models, mongo.NewDeleteOneModel().SetFilter(bson.M{"_id": x.Key}))
		} else {
			if x.Scope == "device" {
				x.Location = ""
			}
			models = append(models, mongo.NewUpdateOneModel().SetFilter(bson.M{"_id": x.Key}).SetUpdate(bson.M{"$set": bson.M{"location": x.Location, "scope": x.Scope}}).SetUpsert(true))
		}
	}
	pmMu.Lock()
	defer pmMu.Unlock()
	before, e := read()
	if e != nil {
		w.WriteHeader(503)
		return
	}
	actor, ok := currentMember(r)
	if !ok {
		w.WriteHeader(401)
		return
	}
	audit, e := beginAudit(actor, "PM", "cameraPoints", "จุดติดตั้งกล้อง", before, input.Points)
	if e != nil {
		w.WriteHeader(503)
		return
	}
	_, e = col.BulkWrite(ctx, models)
	outcome := "success"
	if e != nil {
		outcome = "failed"
	}
	logErr := finishAudit(audit, outcome, before, input.Points)
	if e != nil {
		http.Error(w, "บันทึกจุดกล้องไม่สำเร็จ กรุณาโหลดใหม่เพื่อตรวจผล", 503)
		return
	}
	if logErr != nil {
		w.Header().Set("X-Audit-Warning", "pending")
	}
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
