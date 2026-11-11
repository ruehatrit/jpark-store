package main

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestCCTVCatalogPersistsIdentityWithoutChangingLiveStatus(t *testing.T) {
	mock, commands := mockAccounts(t)
	oldClient := mongoClient
	mongoClient = memberCol.Database().Client()
	defer func() { mongoClient = oldClient }()
	cctvStore.Lock()
	oldPayload, oldReceived := cctvStore.payload, cctvStore.received
	cctvStore.payload = CCTVPayload{}
	cctvStore.received = time.Time{}
	cctvStore.Unlock()
	defer func() {
		cctvStore.Lock()
		cctvStore.payload = oldPayload
		cctvStore.received = oldReceived
		cctvStore.Unlock()
	}()
	cameraInventoryMu.Lock()
	saved := cameraInventorySaved
	cameraInventorySaved = ""
	cameraInventoryMu.Unlock()
	defer func() { cameraInventoryMu.Lock(); cameraInventorySaved = saved; cameraInventoryMu.Unlock() }()
	mock.AddResponses(writeOK())
	saveCameraInventory([]CameraStatus{{Channel: 1, IP: "10.10.32.40", Online: true}})
	if len(*commands) != 1 {
		t.Fatal("identity was not persisted")
	}
	raw, _ := bson.MarshalExtJSON((*commands)[0], false, false)
	if strings.Contains(string(raw), "online") {
		t.Fatal("live state persisted as registry")
	}
	doc := cameraInventoryDoc{ID: "main", Cameras: []CameraIdentity{{Channel: 1, IP: "10.10.32.40"}}}
	mock.AddResponses(accountCursor(doc))
	rec := httptest.NewRecorder()
	cctvCatalogHandler(rec, httptest.NewRequest("GET", "/api/cctv/catalog", nil))
	if rec.Code != 200 || !strings.Contains(rec.Body.String(), "10.10.32.40") {
		t.Fatal("inventory unavailable after restart", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	cctvStatus(rec, httptest.NewRequest("GET", "/api/cctv/status", nil))
	var status map[string]interface{}
	_ = json.Unmarshal(rec.Body.Bytes(), &status)
	if status["stale"] != true {
		t.Fatal("registry fabricated live status")
	}
	cctvStore.Lock()
	cctvStore.payload = CCTVPayload{Cameras: []CameraStatus{{Channel: 1, IP: "10.10.32.41"}}}
	cctvStore.Unlock()
	mock.AddResponses(accountCursor(doc))
	cameras, e := registeredCameras(context.Background())
	if e != nil || len(cameras) != 1 || cameras[0].IP != "10.10.32.41" {
		t.Fatal("live inventory must override old identity")
	}
}
func TestPMCameraPointsSavedWithoutChangingMonthlyResults(t *testing.T) {
	mock, commands := mockAccounts(t)
	oldClient := mongoClient
	mongoClient = memberCol.Database().Client()
	defer func() { mongoClient = oldClient }()
	cctvStore.Lock()
	oldPayload := cctvStore.payload
	cctvStore.payload = CCTVPayload{}
	cctvStore.Unlock()
	defer func() { cctvStore.Lock(); cctvStore.payload = oldPayload; cctvStore.Unlock() }()
	mock.AddResponses(accountCursor(cameraInventoryDoc{ID: "main", Cameras: []CameraIdentity{{Channel: 1, IP: "10.10.32.40"}}}), accountCursor(), writeOK(), writeOK(), writeOK())
	req := rootRequest("POST", "/api/pm/camera-points", `{"points":[{"key":"ip:10.10.32.40","location":"Member B3C ทางออก 1","scope":"point"}]}`)
	rec := httptest.NewRecorder()
	requireAuth(cameraPointHandler)(rec, req)
	if rec.Code != 200 {
		t.Fatal("camera assignment failed", rec.Code, rec.Body.String())
	}
	wrote := false
	for _, cmd := range *commands {
		if v := cmd.Lookup("update"); v.Type != 0 {
			if v.StringValue() == "pm_months" {
				t.Fatal("camera assignment changed monthly PM")
			}
			if v.StringValue() == "pm_camera_points" {
				wrote = true
			}
		}
	}
	if !wrote {
		t.Fatal("point assignment not saved")
	}
}
