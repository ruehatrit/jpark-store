package main

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const attendanceDeviceID = "thai01-main"

var attendanceEmployees, attendanceEvents, attendanceDevices, attendanceShifts *mongo.Collection
var employeeCodePattern = regexp.MustCompile(`^[0-9]{1,9}$`)
var attendanceZone = time.FixedZone("ICT", 7*3600)

type EmployeeProfile struct {
	Code          string `json:"code" bson:"_id"`
	Name          string `json:"name" bson:"name"`
	Position      string `json:"position" bson:"position"`
	Nickname      string `json:"nickname" bson:"nickname"`
	Phone         string `json:"phone" bson:"phone"`
	DefaultShift  string `json:"defaultShift" bson:"defaultShift"`
	Active        bool   `json:"active" bson:"active"`
	AvatarVersion string `json:"avatarVersion,omitempty" bson:"avatarVersion,omitempty"`
}
type FingerEvent struct {
	ID         string    `json:"id" bson:"_id"`
	Device     string    `json:"device" bson:"device"`
	Code       string    `json:"code" bson:"code"`
	Time       string    `json:"time" bson:"time"`
	Status     int       `json:"status" bson:"status"`
	Punch      int       `json:"punch" bson:"punch"`
	ReceivedAt time.Time `json:"receivedAt" bson:"receivedAt"`
}
type FingerDevice struct {
	ID               string    `bson:"_id"`
	KeyHash          string    `json:"-" bson:"keyHash"`
	Enabled          bool      `bson:"enabled"`
	LastSeen         time.Time `bson:"lastSeen"`
	LastRead         time.Time `bson:"lastRead"`
	LastCompleteRead time.Time `bson:"lastCompleteRead"`
	LastError        string    `bson:"lastError"`
	MachineCount     int       `bson:"machineCount"`
}
type EmployeeShift struct {
	ManualLeave bool   `json:"manualLeave,omitempty" bson:"manualLeave,omitempty"`
	LeaveNote   string `json:"leaveNote,omitempty" bson:"leaveNote,omitempty"`
	ID          string `bson:"_id"`
	Date        string `json:"date" bson:"date"`
	Code        string `json:"code" bson:"code"`
	Shift       string `json:"shift" bson:"shift"`
	Source      string `json:"source,omitempty" bson:"source,omitempty"`
}

func canonicalEmployeeCode(value string) string {
	value = strings.TrimSpace(value)
	if !employeeCodePattern.MatchString(value) {
		return ""
	}
	n, err := strconv.Atoi(value)
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%06d", n)
}
func isITProfile(p EmployeeProfile) bool {
	return p.Code == "000526" || p.Code == "000549" || p.Code == "000562" || p.Code == "000571" || strings.EqualFold(strings.TrimSpace(p.Position), "พนักงาน IT")
}
func initialEmployeeProfiles() []EmployeeProfile {
	raw := [][4]string{{"000471", "นายชยากร เอกสินเสริม", "ผู้จัดการหน่วยงาน", "จุ้ย"}, {"000526", "นายพิสุทธิ์ศักดิ์ ปิยะพันธ์", "พนักงาน IT", "ต้อง"}, {"000549", "นายฤหัสฤทธิ์ หงษ์โต", "พนักงาน IT", "เด๊ะ"}, {"000562", "นายเอกชน หนูน้อย", "พนักงาน IT", "ชล"}, {"000571", "นายธนากร ปวงบุผา", "พนักงาน IT", "โด่ง"}, {"000498", "นางสาวจิตติมา ชูกร", "พนักงานประชาสัมพันธ์ระบบ", "โบว์"}, {"000500", "นางสาวกัลยวรรธน์ งั่วสมบูรณ์", "พนักงานประชาสัมพันธ์ระบบ", "แนน"}, {"000520", "นางสาวรัชฎาภรณ์ ศรีกุล", "พนักงานประชาสัมพันธ์ระบบ", "แป้ง"}, {"000543", "นางสาวธนภร ชัยวิเศษ", "พนักงานประชาสัมพันธ์ระบบ", "ลูกหมี"}, {"000547", "นางสาวอาทิตยา ด้วงพันธุ์", "พนักงานประชาสัมพันธ์ระบบ", "การ์ตูน"}, {"104284", "นางสาวธันฐภัทร์ วิททิงตั้น", "พนักงานแคชเชียร์", "ควินน์"}, {"104326", "นางสาวศุภพร บุระรัตน์", "พนักงานแคชเชียร์", "ปุ้ม"}, {"104343", "นางสาวพิมพ์ลภัส รัตนอุดม", "พนักงานแคชเชียร์", "แพม"}, {"104466", "นางสาวอ้อยทิพย์ ศรีทอง", "พนักงานแคชเชียร์", "โบว์"}, {"104620", "นางสาวอนัญญา ใจบุญ", "พนักงานแคชเชียร์", "ฝ้าย"}, {"104734", "นางสาวภาสินี สกุลวรเดช", "พนักงานแคชเชียร์", "เก๋"}}
	out := []EmployeeProfile{}
	for _, r := range raw {
		if r[2] != "พนักงาน IT" {
			continue
		}
		out = append(out, EmployeeProfile{Code: r[0], Name: r[1], Position: r[2], Nickname: r[3], DefaultShift: "unassigned", Active: true})
	}
	return out
}
func initAttendance() {
	if !useMongo || mongoClient == nil {
		return
	}
	db := mongoClient.Database("jpark_store")
	attendanceEmployees = db.Collection("attendance_employees")
	attendanceEvents = db.Collection("attendance_events")
	attendanceDevices = db.Collection("attendance_devices")
	attendanceShifts = db.Collection("attendance_shifts")
	attendanceAvatars = db.Collection("attendance_avatars")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	models := []mongo.WriteModel{}
	for _, e := range initialEmployeeProfiles() {
		models = append(models, mongo.NewUpdateOneModel().SetFilter(bson.M{"_id": e.Code}).SetUpdate(bson.M{"$setOnInsert": e}).SetUpsert(true))
	}
	_, err := attendanceEmployees.BulkWrite(ctx, models)
	if err != nil {
		fmt.Println("Employee register unavailable:", err)
	}
	_, _ = attendanceEvents.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "time", Value: -1}, {Key: "_id", Value: -1}}})
	_, _ = attendanceEvents.Indexes().CreateOne(ctx, mongo.IndexModel{Keys: bson.D{{Key: "code", Value: 1}, {Key: "time", Value: 1}}})
	initNovemberITRoster()
}
func employeeProfiles(ctx context.Context) ([]EmployeeProfile, error) {
	cur, err := attendanceEmployees.Find(ctx, bson.M{}, options.Find().SetSort(bson.D{{Key: "_id", Value: 1}}))
	if err != nil {
		return nil, err
	}
	defer cur.Close(ctx)
	out := []EmployeeProfile{}
	err = cur.All(ctx, &out)
	filtered := []EmployeeProfile{}
	for _, p := range out {
		if isITProfile(p) {
			filtered = append(filtered, p)
		}
	}
	return filtered, err
}
func deviceRecord(ctx context.Context) (FingerDevice, error) {
	var d FingerDevice
	err := attendanceDevices.FindOne(ctx, bson.M{"_id": attendanceDeviceID}).Decode(&d)
	if err == mongo.ErrNoDocuments {
		return FingerDevice{ID: attendanceDeviceID}, nil
	}
	return d, err
}
func publicFingerDevice(d FingerDevice) map[string]interface{} {
	return map[string]interface{}{"id": d.ID, "configured": d.KeyHash != "", "enabled": d.Enabled, "lastSeen": d.LastSeen, "lastRead": d.LastRead, "lastCompleteRead": d.LastCompleteRead, "lastError": d.LastError, "machineCount": d.MachineCount}
}
func validEmployeeShift(s string) bool {
	return s == "day" || s == "night" || s == "off" || s == "unassigned"
}
func employeeRegisterHandler(w http.ResponseWriter, r *http.Request) {
	if attendanceEmployees == nil {
		http.Error(w, "ต้องเชื่อม MongoDB", 503)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if r.Method == http.MethodGet {
		out, err := employeeProfiles(ctx)
		if err != nil {
			w.WriteHeader(503)
			return
		}
		json.NewEncoder(w).Encode(map[string]interface{}{"employees": out})
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	actor, ok := currentMember(r)
	if !ok || actor.Role != "admin" {
		w.WriteHeader(403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 128<<10)
	var input struct {
		Employees []EmployeeProfile `json:"employees"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Employees) == 0 || len(input.Employees) > 500 {
		w.WriteHeader(400)
		return
	}
	seen := map[string]bool{}
	for i := range input.Employees {
		x := &input.Employees[i]
		x.Code = canonicalEmployeeCode(x.Code)
		x.Name = strings.Join(strings.Fields(x.Name), " ")
		if x.Code == "" || seen[x.Code] || !isITProfile(*x) || x.Name == "" || len(x.Name) > 200 || len(x.Position) > 200 || len(x.Nickname) > 100 || len(x.Phone) > 60 || !validEmployeeShift(x.DefaultShift) {
			http.Error(w, "ข้อมูลพนักงานหรือรหัสซ้ำไม่ถูกต้อง", 400)
			return
		}
		seen[x.Code] = true
	}
	before, err := employeeProfiles(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	audit, err := beginAudit(actor, "ทะเบียนพนักงาน", "saveEmployees", "", before, input.Employees)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	models := []mongo.WriteModel{}
	for _, x := range input.Employees {
		models = append(models, mongo.NewUpdateOneModel().SetFilter(bson.M{"_id": x.Code}).SetUpdate(bson.M{"$set": x}).SetUpsert(true))
	}
	_, err = attendanceEmployees.BulkWrite(ctx, models)
	outcome := "success"
	if err != nil {
		outcome = "failed"
	}
	after, readErr := employeeProfiles(ctx)
	if readErr == nil {
		if finishAudit(audit, outcome, before, after) != nil {
			w.Header().Set("X-Audit-Warning", "pending")
		}
	}
	if err != nil {
		w.WriteHeader(503)
		return
	}
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
func attendanceDeviceHandler(w http.ResponseWriter, r *http.Request) {
	if attendanceDevices == nil {
		w.WriteHeader(503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
	defer cancel()
	device, err := deviceRecord(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method == http.MethodGet {
		json.NewEncoder(w).Encode(publicFingerDevice(device))
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1024)
	var input struct {
		Action string `json:"action"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || (input.Action != "issue_key" && input.Action != "disable") {
		w.WriteHeader(400)
		return
	}
	actor, _ := currentMember(r)
	audit, err := beginAudit(actor, "เครื่องสแกนนิ้ว", input.Action, device.ID, publicFingerDevice(device), nil)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	set := bson.M{"enabled": false}
	token := ""
	if input.Action == "issue_key" {
		token, err = randomMemberToken()
		if err != nil {
			w.WriteHeader(503)
			return
		}
		hash := sha256.Sum256([]byte(token))
		set = bson.M{"keyHash": hex.EncodeToString(hash[:]), "enabled": true}
	}
	_, err = attendanceDevices.UpdateOne(ctx, bson.M{"_id": attendanceDeviceID}, bson.M{"$set": set}, options.UpdateOne().SetUpsert(true))
	if err != nil {
		_ = finishAudit(audit, "failed", publicFingerDevice(device), nil)
		w.WriteHeader(503)
		return
	}
	_ = finishAudit(audit, "success", publicFingerDevice(device), map[string]interface{}{"enabled": input.Action == "issue_key", "keyRotated": input.Action == "issue_key"})
	if token == "" {
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	}
	staff, err := employeeProfiles(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	people := map[string]string{}
	for _, p := range staff {
		if p.Active {
			people[p.Code] = p.Name
		}
	}
	// The secret is returned once, never exposed by GET and never copied into audit.
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "config": map[string]interface{}{"device_id": attendanceDeviceID, "device_ip": "10.10.33.201", "device_port": 4370, "comm_key": 0, "web_url": "https://" + r.Host, "api_key": token, "poll_seconds": 5, "employees": people}})
}
func fingerEventID(device, code, stamp string, status, punch int) string {
	v := fmt.Sprintf("%s\x1f%s\x1f%s\x1f%d\x1f%d", device, code, stamp, status, punch)
	h := sha256.Sum256([]byte(v))
	return hex.EncodeToString(h[:])
}
func fingerIngest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if attendanceEvents == nil {
		w.WriteHeader(503)
		return
	}
	auth := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if len(auth) != 43 {
		w.WriteHeader(401)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	var input struct {
		Device       string `json:"device_id"`
		ReadOK       bool   `json:"read_ok"`
		MachineCount int    `json:"machine_count"`
		Records      []struct {
			Code   string `json:"code"`
			Time   string `json:"time"`
			Status int    `json:"status"`
			Punch  int    `json:"punch"`
		} `json:"records"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || input.Device != attendanceDeviceID || len(input.Records) > 500 || input.MachineCount < 0 || (!input.ReadOK && len(input.Records) > 0) {
		w.WriteHeader(400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	device, err := deviceRecord(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	sum := sha256.Sum256([]byte(auth))
	if !device.Enabled || subtle.ConstantTimeCompare([]byte(device.KeyHash), []byte(hex.EncodeToString(sum[:]))) != 1 {
		w.WriteHeader(401)
		return
	}
	staff, err := employeeProfiles(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	people := map[string]string{}
	for _, p := range staff {
		if p.Active {
			people[p.Code] = p.Name
		}
	}
	records := []FingerEvent{}
	for _, x := range input.Records {
		code := canonicalEmployeeCode(x.Code)
		stamp, e := time.ParseInLocation("2006-01-02 15:04:05", x.Time, attendanceZone)
		if e != nil || stamp.Format("2006-01-02 15:04:05") != x.Time || code == "" || x.Status < 0 || x.Status > 255 || x.Punch < 0 || x.Punch > 255 || stamp.Year() < 2000 || stamp.Year() > 2100 {
			http.Error(w, "invalid scan record", 400)
			return
		}
		if _, ok := people[code]; !ok {
			continue
		}
		records = append(records, FingerEvent{ID: fingerEventID(input.Device, code, x.Time, x.Status, x.Punch), Device: input.Device, Code: code, Time: x.Time, Status: x.Status, Punch: x.Punch, ReceivedAt: time.Now().UTC()})
	}
	accepted := []string{}
	models := []mongo.WriteModel{}
	seen := map[string]bool{}
	for _, x := range records {
		if seen[x.ID] {
			continue
		}
		seen[x.ID] = true
		accepted = append(accepted, x.ID)
		models = append(models, mongo.NewUpdateOneModel().SetFilter(bson.M{"_id": x.ID}).SetUpdate(bson.M{"$setOnInsert": x}).SetUpsert(true))
	}
	if len(models) > 0 {
		if _, err = attendanceEvents.BulkWrite(ctx, models, options.BulkWrite().SetOrdered(false)); err != nil {
			w.WriteHeader(503)
			return
		}
	}
	set := bson.M{"lastSeen": time.Now().UTC(), "lastError": "อ่านเครื่องไม่สำเร็จ"}
	if input.ReadOK {
		set["lastRead"] = time.Now().UTC()
		set["lastError"] = ""
		set["machineCount"] = input.MachineCount
		if len(input.Records) < 500 {
			set["lastCompleteRead"] = time.Now().UTC()
		}
	}
	if _, err = attendanceDevices.UpdateOne(ctx, bson.M{"_id": attendanceDeviceID}, bson.M{"$set": set}); err != nil {
		w.WriteHeader(503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "accepted_ids": accepted, "employees": people})
}
func arrivalStatus(date, shift, first string) string {
	if shift == "off" {
		return "off"
	}
	if shift != "day" && shift != "night" {
		return "unassigned"
	}
	if first == "" {
		return "missing"
	}
	cutoff := date + " 07:01:00"
	if shift == "night" {
		cutoff = date + " 19:01:00"
	}
	if first >= cutoff {
		return "late"
	}
	return "ontime"
}
func scanSummary(day time.Time, code, shift string, events []FingerEvent) (string, string, int) {
	date := day.Format("2006-01-02")
	from, to := date+" 04:00:00", date+" 16:00:00"
	if shift == "night" {
		from, to = date+" 16:00:00", day.AddDate(0, 0, 1).Format("2006-01-02")+" 04:00:00"
	}
	first, last := "", ""
	count := 0
	for _, event := range events {
		if event.Code != code {
			continue
		}
		if shift == "unassigned" || shift == "off" {
			if !strings.HasPrefix(event.Time, date) {
				continue
			}
		} else if event.Time < from || event.Time >= to {
			continue
		}
		if first == "" || event.Time < first {
			first = event.Time
		}
		if last == "" || event.Time > last {
			last = event.Time
		}
		count++
	}
	return first, last, count
}
func attendanceViewHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	if attendanceEvents == nil {
		w.WriteHeader(503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	staff, err := employeeProfiles(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	device, err := deviceRecord(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	date := r.URL.Query().Get("date")
	if date == "" {
		date = time.Now().In(attendanceZone).Format("2006-01-02")
	}
	day, err := time.ParseInLocation("2006-01-02", date, attendanceZone)
	if err != nil {
		w.WriteHeader(400)
		return
	}
	shifts := map[string]string{}
	cur, err := attendanceShifts.Find(ctx, bson.M{"date": date})
	if err != nil {
		w.WriteHeader(503)
		return
	}
	var assigned []EmployeeShift
	err = cur.All(ctx, &assigned)
	cur.Close(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	for _, x := range assigned {
		shifts[x.Code] = x.Shift
	}
	cur, err = attendanceEvents.Find(ctx, bson.M{"time": bson.M{"$gte": date + " 00:00:00", "$lt": day.AddDate(0, 0, 1).Format("2006-01-02") + " 04:00:00"}}, options.Find().SetSort(bson.D{{Key: "time", Value: 1}}))
	if err != nil {
		w.WriteHeader(503)
		return
	}
	daily := []FingerEvent{}
	err = cur.All(ctx, &daily)
	cur.Close(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	today := []map[string]interface{}{}
	for _, p := range staff {
		if !p.Active {
			continue
		}
		shift := p.DefaultShift
		if v, ok := shifts[p.Code]; ok {
			shift = v
		}
		first, last, count := scanSummary(day, p.Code, shift, daily)
		today = append(today, map[string]interface{}{"employee": p, "shift": shift, "first": first, "last": last, "count": count, "status": arrivalStatus(date, shift, first)})
	}
	scopeCodes := []string{}
	for _, p := range staff {
		scopeCodes = append(scopeCodes, p.Code)
	}
	filter := bson.M{"code": bson.M{"$in": scopeCodes}}
	for _, key := range []string{"from", "to"} {
		s := r.URL.Query().Get(key)
		if s == "" {
			s = date
		}
		d, e := time.ParseInLocation("2006-01-02", s, attendanceZone)
		if e != nil {
			w.WriteHeader(400)
			return
		}
		bounds, ok := filter["time"].(bson.M)
		if !ok {
			bounds = bson.M{}
		}
		if key == "from" {
			bounds["$gte"] = d.Format("2006-01-02") + " 00:00:00"
		} else {
			bounds["$lt"] = d.AddDate(0, 0, 1).Format("2006-01-02") + " 00:00:00"
		}
		filter["time"] = bounds
	}
	if code := r.URL.Query().Get("code"); code != "" {
		code = canonicalEmployeeCode(code)
		if code == "" {
			w.WriteHeader(400)
			return
		}
		allowed := false
		for _, p := range staff {
			if p.Code == code {
				allowed = true
			}
		}
		if !allowed {
			w.WriteHeader(400)
			return
		}
		filter["code"] = code
	}
	offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
	if offset < 0 || offset > 100000 {
		w.WriteHeader(400)
		return
	}
	total, err := attendanceEvents.CountDocuments(ctx, filter)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	cur, err = attendanceEvents.Find(ctx, filter, options.Find().SetSort(bson.D{{Key: "time", Value: -1}, {Key: "_id", Value: -1}}).SetSkip(int64(offset)).SetLimit(100))
	if err != nil {
		w.WriteHeader(503)
		return
	}
	events := []FingerEvent{}
	err = cur.All(ctx, &events)
	cur.Close(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]interface{}{"date": date, "employees": staff, "today": today, "records": events, "total": total, "offset": offset, "device": publicFingerDevice(device), "serverTime": time.Now().UTC()})
}
func attendanceShiftHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if attendanceShifts == nil {
		w.WriteHeader(503)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var input struct {
		Date   string          `json:"date"`
		Shifts []EmployeeShift `json:"shifts"`
	}
	if json.NewDecoder(r.Body).Decode(&input) != nil || len(input.Shifts) > 500 {
		w.WriteHeader(400)
		return
	}
	if _, err := time.Parse("2006-01-02", input.Date); err != nil {
		w.WriteHeader(400)
		return
	}
	seen := map[string]bool{}
	for i := range input.Shifts {
		x := &input.Shifts[i]
		x.Code = canonicalEmployeeCode(x.Code)
		if x.Code == "" || seen[x.Code] || !validEmployeeShift(x.Shift) {
			w.WriteHeader(400)
			return
		}
		seen[x.Code] = true
		x.Date = input.Date
		x.ID = input.Date + ":" + x.Code
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	cur, err := attendanceShifts.Find(ctx, bson.M{"date": input.Date})
	if err != nil {
		w.WriteHeader(503)
		return
	}
	before := []EmployeeShift{}
	err = cur.All(ctx, &before)
	cur.Close(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	actor, _ := currentMember(r)
	audit, err := beginAudit(actor, "กะพนักงาน", "saveShifts", input.Date, before, input.Shifts)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	models := []mongo.WriteModel{}
	for _, x := range input.Shifts {
		models = append(models, mongo.NewUpdateOneModel().SetFilter(bson.M{"_id": x.ID}).SetUpdate(bson.M{"$set": x}).SetUpsert(true))
	}
	if len(models) > 0 {
		_, err = attendanceShifts.BulkWrite(ctx, models)
	}
	outcome := "success"
	if err != nil {
		outcome = "failed"
	}
	_ = finishAudit(audit, outcome, before, input.Shifts)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
