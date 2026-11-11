package main

import (
	"context"
	"testing"
)

func TestPMSharedPointAndNetworkEvidence(t *testing.T) {
	old := pmMonthCol
	pmMonthCol = nil
	defer func() { pmMonthCol = old; delete(pmMemory, "2026-12") }()
	pmMemory["2026-12"] = map[string]PMResult{"asset:custom": {Location: "จุดทดสอบ", PhotoScope: "point"}}
	for _, key := range []string{"point:B1 A15", "point:B2 C18", "point:B3 N8", "network:ping", "point:จุดทดสอบ"} {
		ok, err := pmSharedAttachmentKey(context.Background(), "2026-12", key)
		if err != nil || !ok {
			t.Fatal("valid shared evidence rejected", key, err)
		}
	}
	for _, key := range []string{"point:", "point:unknown", "point:B1  A15", "point:ออฟฟิศ"} {
		ok, err := pmSharedAttachmentKey(context.Background(), "2026-12", key)
		if err != nil || ok {
			t.Fatal("unregistered point accepted", key, err)
		}
	}
	x := PMResult{Key: "ip:1", Name: "Kiosk", Status: "pending", PhotoScope: "point"}
	if normalizePMResult(&x) {
		t.Fatal("shared photo scope without point accepted")
	}
	x.PhotoScope = "unsupported"
	if normalizePMResult(&x) {
		t.Fatal("invalid scope accepted")
	}
}

func TestPMPhotosAllowedForEveryRegisteredDevice(t *testing.T) {
	old := assetSt
	defer func() { assetSt = old }()
	assetSt = AssetState{Assets: []Asset{{ID: 42, AssetID: "K1", Category: "Kiosk", Name: "Kiosk", Active: true}, {ID: 43, AssetID: "OLD", Active: false}}}
	if !pmEquipmentKey("asset:K1-42") || !pmEquipmentKey("ip:10.10.36.28") || !pmEquipmentKey("cctv:channel-1") {
		t.Fatal("non-NVR equipment cannot attach evidence")
	}
	if pmEquipmentKey("asset:OLD-43") || pmEquipmentKey("asset:missing-9") {
		t.Fatal("unknown/inactive equipment accepted")
	}
}
func TestPMInternetResultsValidateAndPreserveZero(t *testing.T) {
	zero, negative := 0.0, -1.0
	x := PMResult{Key: "internet:office", Name: "Internet", Location: "ออฟฟิศ", Status: "normal", Inspectors: []string{"IT"}, Internet: &PMInternetTest{Download: &zero, Upload: &zero, Ping: &zero, TestedAt: "2026-11-11T08:30", Provider: "LAN"}}
	if !normalizePMResult(&x) || x.Internet.Download == nil {
		t.Fatal("zero measured result lost")
	}
	x.Internet.Ping = &negative
	if normalizePMResult(&x) {
		t.Fatal("negative measurement accepted")
	}
	x.Internet.Ping = &zero
	x.Internet.TestedAt = "invalid"
	if normalizePMResult(&x) {
		t.Fatal("bad test time accepted")
	}
}
