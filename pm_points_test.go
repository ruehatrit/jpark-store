package main

import "testing"

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
