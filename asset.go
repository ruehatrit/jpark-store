package main

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

type Asset struct {
	ID          int    `json:"id" bson:"id"`
	AssetID     string `json:"assetId" bson:"assetId"`
	Category    string `json:"category" bson:"category"`
	Name        string `json:"name" bson:"name"`
	Brand       string `json:"brand" bson:"brand"`
	Model       string `json:"model" bson:"model"`
	Serial      string `json:"serial" bson:"serial"`
	IP          string `json:"ip" bson:"ip"`
	Location    string `json:"location" bson:"location"`
	InstallDate string `json:"installDate" bson:"installDate"`
	Note        string `json:"note" bson:"note"`
	Active      bool   `json:"active" bson:"active"`
}
type AssetState struct {
	Assets []Asset `json:"assets" bson:"assets"`
}
type assetDoc struct {
	ID     string  `bson:"_id"`
	Assets []Asset `bson:"assets"`
}

var assetMu sync.Mutex
var assetCol *mongo.Collection
var assetSt = AssetState{Assets: []Asset{}}

func normalizeAssetCategory(x *Asset) bool {
	old := x.Category
	switch x.Category {
	case "Barrier":
		x.Category = "ไม้กั้น"
	case "Network / Switch":
		x.Category = "Switch"
	case "ตู้ไฟ / Control":
		x.Category = "ตู้ไฟ"
	case "Display":
		x.Category = "จอแสดงทะเบียน"
	case "Kiosk / Payment":
		n := strings.ToLower(x.Name)
		if strings.Contains(n, "payment") || strings.Contains(x.Name, "เพเมน") || strings.Contains(x.Name, "ชำระ") {
			x.Category = "Payment"
		} else {
			x.Category = "Kiosk"
		}
	}
	return old != x.Category
}

func initAssetRegister() {
	if useMongo && mongoClient != nil {
		assetCol = mongoClient.Database("jpark_store").Collection("asset_register_state")
		ctx, c := context.WithTimeout(context.Background(), 15*time.Second)
		defer c()
		var d assetDoc
		e := assetCol.FindOne(ctx, bson.M{"_id": "main"}).Decode(&d)
		if e == mongo.ErrNoDocuments {
			_, e = assetCol.InsertOne(ctx, assetDoc{ID: "main", Assets: assetSt.Assets})
		} else if e == nil {
			assetSt.Assets = d.Assets
			changed := false
			for i := range assetSt.Assets {
				if normalizeAssetCategory(&assetSt.Assets[i]) {
					changed = true
				}
			}
			if changed {
				_ = saveAssets()
			}
		}
		if e == nil {
			return
		}
	}
}
func saveAssets() error {
	if assetCol == nil {
		return nil
	}
	ctx, c := context.WithTimeout(context.Background(), 15*time.Second)
	defer c()
	d := assetDoc{ID: "main", Assets: assetSt.Assets}
	r, e := assetCol.ReplaceOne(ctx, bson.M{"_id": "main"}, d)
	if e == nil && r.MatchedCount == 0 {
		_, e = assetCol.InsertOne(ctx, d)
	}
	return e
}
func nextAssetID() int {
	m := 0
	for _, x := range assetSt.Assets {
		if x.ID > m {
			m = x.ID
		}
	}
	return m + 1
}
func assetStateHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	assetMu.Lock()
	defer assetMu.Unlock()
	_ = json.NewEncoder(w).Encode(assetSt)
}
func assetActionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var a map[string]interface{}
	if json.NewDecoder(r.Body).Decode(&a) != nil {
		http.Error(w, "bad json", 400)
		return
	}
	s := func(k string) string { v, _ := a[k].(string); return v }
	n := func(k string) int {
		switch x := a[k].(type) {
		case float64:
			return int(x)
		case string:
			i, _ := strconv.Atoi(x)
			return i
		}
		return 0
	}
	assetMu.Lock()
	defer assetMu.Unlock()
	ok := true
	errMsg := ""
	switch s("action") {
	case "add":
		assetSt.Assets = append(assetSt.Assets, Asset{ID: nextAssetID(), AssetID: s("assetId"), Category: s("category"), Name: s("name"), Brand: s("brand"), Model: s("model"), Serial: s("serial"), IP: s("ip"), Location: s("location"), InstallDate: s("installDate"), Note: s("note"), Active: true})
	case "update":
		id := n("id")
		found := false
		for i := range assetSt.Assets {
			if assetSt.Assets[i].ID == id {
				x := &assetSt.Assets[i]
				x.AssetID = s("assetId")
				x.Category = s("category")
				x.Name = s("name")
				x.Brand = s("brand")
				x.Model = s("model")
				x.Serial = s("serial")
				x.IP = s("ip")
				x.Location = s("location")
				x.InstallDate = s("installDate")
				x.Note = s("note")
				found = true
				break
			}
		}
		if !found {
			ok = false
			errMsg = "ไม่พบ Asset"
		}
	case "delete":
		id := n("id")
		z := assetSt.Assets[:0]
		for _, x := range assetSt.Assets {
			if x.ID != id {
				z = append(z, x)
			}
		}
		assetSt.Assets = z
	default:
		ok = false
		errMsg = "unknown action"
	}
	if ok {
		if e := saveAssets(); e != nil {
			ok = false
			errMsg = e.Error()
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": ok, "error": errMsg})
}
