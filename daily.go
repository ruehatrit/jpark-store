package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"sync"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
)

type DailyCheckItem struct {
	ID       int    `json:"id" bson:"id"`
	Group    string `json:"group" bson:"group"`
	Name     string `json:"name" bson:"name"`
	Location string `json:"location" bson:"location"`
	Active   bool   `json:"active" bson:"active"`
}

type DailyCheckResult struct {
	ItemID   int    `json:"itemId" bson:"itemId"`
	Group    string `json:"group" bson:"group"`
	Name     string `json:"name" bson:"name"`
	Location string `json:"location" bson:"location"`
	Status   string `json:"status" bson:"status"`
	Note     string `json:"note" bson:"note"`
}

type DailyCheckRecord struct {
	ID          int                `json:"id" bson:"id"`
	Date        string             `json:"date" bson:"date"`
	Shift       string             `json:"shift" bson:"shift"`
	Inspector   string             `json:"inspector" bson:"inspector"`
	OverallNote string             `json:"overallNote" bson:"overallNote"`
	CreatedAt   string             `json:"createdAt" bson:"createdAt"`
	Results     []DailyCheckResult `json:"results" bson:"results"`
}

type DailyCheckState struct {
	Zones   []string           `json:"zones" bson:"zones"`
	Items   []DailyCheckItem   `json:"items" bson:"items"`
	Records []DailyCheckRecord `json:"records" bson:"records"`
}

type dailyStateDoc struct {
	ID      string             `bson:"_id"`
	Zones   []string           `bson:"zones"`
	Items   []DailyCheckItem   `bson:"items"`
	Records []DailyCheckRecord `bson:"records"`
}

var dailyMu sync.Mutex
var dailyCol *mongo.Collection
var dailyUseMongo bool

const dailyDataFile = "/tmp/jpark-daily-check.json"

var dailySt = DailyCheckState{
	Zones: []string{"ทางเข้า 1", "ทางเข้า 2", "ทางออก 1", "ทางออก 2", "ทางออก 3", "ทางออก 4", "จุดชำระเงิน", "ห้องควบคุม", "พื้นที่ลานจอด"},
	Items: []DailyCheckItem{
		{1, "ระบบทางเข้า", "ไม้กั้นรถยนต์", "ทางเข้า 1", true},
		{2, "ระบบทางเข้า", "กล้องอ่านป้ายทะเบียน (LPR)", "ทางเข้า 1", true},
		{3, "ระบบทางเข้า", "จอแสดงทะเบียนรถ", "ทางเข้า 1", true},
		{4, "ระบบทางเข้า", "ไม้กั้นรถยนต์", "ทางเข้า 2", true},
		{5, "ระบบทางเข้า", "กล้องอ่านป้ายทะเบียน (LPR)", "ทางเข้า 2", true},
		{6, "ระบบทางเข้า", "จอแสดงทะเบียนรถ", "ทางเข้า 2", true},
		{7, "ระบบทางออก", "ไม้กั้นรถยนต์", "ทางออก 1", true},
		{8, "ระบบทางออก", "กล้องอ่านป้ายทะเบียน (LPR)", "ทางออก 1", true},
		{9, "ระบบทางออก", "จอแสดงทะเบียนรถ", "ทางออก 1", true},
		{10, "ระบบทางออก", "ไม้กั้นรถยนต์", "ทางออก 2", true},
		{11, "ระบบทางออก", "กล้องอ่านป้ายทะเบียน (LPR)", "ทางออก 2", true},
		{12, "ระบบทางออก", "จอแสดงทะเบียนรถ", "ทางออก 2", true},
		{13, "ระบบทางออก", "ไม้กั้นรถยนต์", "ทางออก 3", true},
		{14, "ระบบทางออก", "กล้องอ่านป้ายทะเบียน (LPR)", "ทางออก 3", true},
		{15, "ระบบทางออก", "จอแสดงทะเบียนรถ", "ทางออก 3", true},
		{16, "ระบบทางออก", "ไม้กั้นรถยนต์", "ทางออก 4", true},
		{17, "ระบบทางออก", "กล้องอ่านป้ายทะเบียน (LPR)", "ทางออก 4", true},
		{18, "ระบบทางออก", "จอแสดงทะเบียนรถ", "ทางออก 4", true},
		{19, "ส่วนกลาง", "Payment / Kiosk", "จุดชำระเงิน", true},
		{20, "ส่วนกลาง", "NVR / ระบบบันทึกภาพ", "ห้องควบคุม", true},
		{21, "ส่วนกลาง", "Network / Switch", "ห้องควบคุม", true},
		{22, "ส่วนกลาง", "ระบบ Intercom / SOS", "พื้นที่ลานจอด", true},
	},
	Records: []DailyCheckRecord{},
}

func normalizeDailyZones() {
	seen := map[string]bool{}
	out := []string{}
	for _, z := range dailySt.Zones {
		if z != "" && !seen[z] {
			seen[z] = true
			out = append(out, z)
		}
	}
	for _, it := range dailySt.Items {
		if it.Location != "" && !seen[it.Location] {
			seen[it.Location] = true
			out = append(out, it.Location)
		}
	}
	dailySt.Zones = out
}

func initDailyCheck() {
	if useMongo && mongoClient != nil {
		dailyCol = mongoClient.Database("jpark_store").Collection("daily_check_state")
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var d dailyStateDoc
		err := dailyCol.FindOne(ctx, bson.M{"_id": "main"}).Decode(&d)
		if err == mongo.ErrNoDocuments {
			d = dailyStateDoc{ID: "main", Zones: dailySt.Zones, Items: dailySt.Items, Records: dailySt.Records}
			if _, err = dailyCol.InsertOne(ctx, d); err == nil {
				dailyUseMongo = true
				fmt.Println("JPARK Daily Check persistence: MongoDB Atlas")
				return
			}
			fmt.Println("Daily Check MongoDB seed error:", err)
		} else if err == nil {
			dailySt = DailyCheckState{Zones: d.Zones, Items: d.Items, Records: d.Records}
			if dailySt.Items == nil {
				dailySt.Items = []DailyCheckItem{}
			}
			if dailySt.Records == nil {
				dailySt.Records = []DailyCheckRecord{}
			}
			dailyUseMongo = true
			normalizeDailyZones()
			fmt.Println("JPARK Daily Check persistence: MongoDB Atlas")
			return
		} else {
			fmt.Println("Daily Check MongoDB load error:", err)
		}
	}

	if b, err := os.ReadFile(dailyDataFile); err == nil {
		_ = json.Unmarshal(b, &dailySt)
	}
	normalizeDailyZones()
	fmt.Println("JPARK Daily Check persistence: /tmp fallback (NOT permanent)")
}

func saveDaily() error {
	if dailyUseMongo && dailyCol != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		d := dailyStateDoc{ID: "main", Zones: dailySt.Zones, Items: dailySt.Items, Records: dailySt.Records}
		res, err := dailyCol.ReplaceOne(ctx, bson.M{"_id": "main"}, d)
		if err != nil {
			return err
		}
		if res.MatchedCount == 0 {
			_, err = dailyCol.InsertOne(ctx, d)
		}
		return err
	}
	b, _ := json.MarshalIndent(dailySt, "", "  ")
	return os.WriteFile(dailyDataFile, b, 0644)
}

func nextDailyItemID() int {
	m := 0
	for _, x := range dailySt.Items {
		if x.ID > m {
			m = x.ID
		}
	}
	return m + 1
}

func nextDailyRecordID() int {
	m := 0
	for _, x := range dailySt.Records {
		if x.ID > m {
			m = x.ID
		}
	}
	return m + 1
}

func dailyStateHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	dailyMu.Lock()
	defer dailyMu.Unlock()
	_ = json.NewEncoder(w).Encode(dailySt)
}

func dailyActionHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var a map[string]interface{}
	if json.NewDecoder(r.Body).Decode(&a) != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
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

	dailyMu.Lock()
	defer dailyMu.Unlock()
	ok := true
	errMsg := ""

	switch s("action") {
	case "addZone":
		zone := s("zone")
		if zone == "" {
			ok = false
			errMsg = "กรุณาระบุชื่อโซน"
			break
		}
		for _, z := range dailySt.Zones {
			if z == zone {
				ok = false
				errMsg = "มีโซนนี้อยู่แล้ว"
				break
			}
		}
		if ok {
			dailySt.Zones = append(dailySt.Zones, zone)
		}
	case "delZone":
		zone := s("zone")
		for _, it := range dailySt.Items {
			if it.Location == zone {
				ok = false
				errMsg = "โซนนี้ยังถูกใช้อยู่ในรายการตรวจ กรุณาแก้/ลบรายการตรวจก่อน"
				break
			}
		}
		if ok {
			z2 := dailySt.Zones[:0]
			for _, z := range dailySt.Zones {
				if z != zone {
					z2 = append(z2, z)
				}
			}
			dailySt.Zones = z2
		}
	case "addItem":
		name := s("name")
		if name == "" {
			ok = false
			errMsg = "กรุณาระบุชื่ออุปกรณ์"
			break
		}
		dailySt.Items = append(dailySt.Items, DailyCheckItem{
			ID: nextDailyItemID(), Group: s("group"), Name: name,
			Location: s("location"), Active: true,
		})
	case "delItem":
		id := n("id")
		z := dailySt.Items[:0]
		for _, x := range dailySt.Items {
			if x.ID != id {
				z = append(z, x)
			}
		}
		dailySt.Items = z
	case "saveRecord":
		var results []DailyCheckResult
		if raw, exists := a["results"]; exists {
			b, _ := json.Marshal(raw)
			_ = json.Unmarshal(b, &results)
		}
		if s("date") == "" || s("inspector") == "" {
			ok = false
			errMsg = "กรุณาระบุวันที่และผู้ตรวจ"
			break
		}
		if len(results) == 0 {
			ok = false
			errMsg = "ไม่มีรายการตรวจสอบ"
			break
		}
		dailySt.Records = append(dailySt.Records, DailyCheckRecord{
			ID: nextDailyRecordID(), Date: s("date"), Shift: s("shift"), Inspector: s("inspector"),
			OverallNote: s("overallNote"), CreatedAt: time.Now().Format(time.RFC3339), Results: results,
		})
	case "delRecord":
		id := n("id")
		z := dailySt.Records[:0]
		for _, x := range dailySt.Records {
			if x.ID != id {
				z = append(z, x)
			}
		}
		dailySt.Records = z
	default:
		ok = false
		errMsg = "unknown action"
	}

	if ok {
		if err := saveDaily(); err != nil {
			ok = false
			errMsg = "บันทึกฐานข้อมูลไม่สำเร็จ: " + err.Error()
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": ok, "error": errMsg})
}

func dailyExportCSV(w http.ResponseWriter, r *http.Request) {
	month := r.URL.Query().Get("month")
	status := r.URL.Query().Get("status")
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=JPARK_Daily_Check_Report_%s.csv", month))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"วันที่", "รอบตรวจ", "ผู้ตรวจ", "หมวด", "อุปกรณ์", "จุดติดตั้ง", "ผลตรวจ", "หมายเหตุรายการ", "หมายเหตุรวม"})

	dailyMu.Lock()
	defer dailyMu.Unlock()
	for _, rec := range dailySt.Records {
		if month != "" && (len(rec.Date) < 7 || rec.Date[:7] != month) {
			continue
		}
		for _, x := range rec.Results {
			if status != "" && status != "ทั้งหมด" && x.Status != status {
				continue
			}
			_ = cw.Write([]string{rec.Date, rec.Shift, rec.Inspector, x.Group, x.Name, x.Location, x.Status, x.Note, rec.OverallNote})
		}
	}
}
