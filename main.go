package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
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
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

type Item struct {
	ID    int    `json:"id" bson:"id"`
	Cat   string `json:"cat" bson:"cat"`
	Name  string `json:"name" bson:"name"`
	Unit  string `json:"unit" bson:"unit"`
	Stock int    `json:"stock" bson:"stock"`
	Min   int    `json:"min" bson:"min"`
}

type Tx struct {
	ID     int    `json:"id" bson:"id"`
	Date   string `json:"date" bson:"date"`
	Type   string `json:"type" bson:"type"`
	Name   string `json:"name" bson:"name"`
	Cat    string `json:"cat" bson:"cat"`
	Unit   string `json:"unit" bson:"unit"`
	Person string `json:"person" bson:"person"`
	Loc    string `json:"loc" bson:"loc"`
	Note   string `json:"note" bson:"note"`
	ItemID int    `json:"itemId" bson:"itemId"`
	Qty    int    `json:"qty" bson:"qty"`
}

type User struct {
	ID   int    `json:"id" bson:"id"`
	Name string `json:"name" bson:"name"`
	Role string `json:"role" bson:"role"`
}

type State struct {
	Items []Item `json:"items" bson:"items"`
	Txs   []Tx   `json:"txs" bson:"txs"`
	Users []User `json:"users" bson:"users"`
}

var mu sync.Mutex
var sessionToken string
var mongoClient *mongo.Client
var stateCol *mongo.Collection
var useMongo bool

func initSessionToken() {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		sessionToken = fmt.Sprintf("%d", time.Now().UnixNano())
		return
	}
	sessionToken = base64.RawURLEncoding.EncodeToString(b)
}

func adminID() string {
	if v := os.Getenv("JPARK_ADMIN_ID"); v != "" {
		return v
	}
	return "admin"
}

func adminPassword() string {
	if v := os.Getenv("JPARK_ADMIN_PASSWORD"); v != "" {
		return v
	}
	return "Jpark@12345"
}

func isAuthed(r *http.Request) bool {
	c, err := r.Cookie("jpark_session")
	return err == nil && c.Value == sessionToken && sessionToken != ""
}

func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isAuthed(r) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "unauthorized"})
			return
		}
		next(w, r)
	}
}

func login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if r.Method != http.MethodPost {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	var a struct {
		ID       string `json:"id" bson:"id"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&a) != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	if a.ID != adminID() || a.Password != adminPassword() {
		w.WriteHeader(http.StatusUnauthorized)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "ชื่อผู้ใช้หรือรหัสผ่านไม่ถูกต้อง"})
		return
	}
	http.SetCookie(w, &http.Cookie{Name: "jpark_session", Value: sessionToken, Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

func logout(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{Name: "jpark_session", Value: "", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true})
}

func sessionStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "authenticated": isAuthed(r)})
}

var st = State{
	Items: []Item{
		{1, "วัสดุสิ้นเปลือง", "ถ่าน23A", "ก้อน", 10, 5},
		{2, "วัสดุสิ้นเปลือง", "กระดาษ Payment,Kiosk 80x80", "ม้วน", 287, 20},
		{3, "วัสดุสิ้นเปลือง", "กระดาษ Handheld 57x40", "ม้วน", 0, 10},
		{4, "เครื่องมือช่าง", "มัลติมิเตอร์", "เครื่อง", 3, 1},
		{5, "เครื่องมือช่าง", "ชุดไขควง", "ชุด", 5, 1},
		{6, "เครื่องมือช่าง", "คีมย้ำสาย LAN", "อัน", 3, 1},
	},
	Txs: []Tx{
		{1, "2026-08-02", "รับเข้า", "กระดาษ Payment,Kiosk 80x80", "วัสดุสิ้นเปลือง", "ม้วน", "Ruehatrit Hongto", "", "นำเข้าจากทะเบียนควบคุมสต็อก", 2, 287},
		{2, "2026-08-02", "รับเข้า", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Ruehatrit Hongto", "", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 30},
		{3, "2026-08-05", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Thanakorn Puangbupha", "ทางออก4", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 2},
		{4, "2026-08-07", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Thanakorn Puangbupha", "ทางเข้า 1,2", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 4},
		{5, "2026-08-09", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Aekkachon Nunoi", "ทางออก3,4", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 4},
		{6, "2026-08-11", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Aekkachon Nunoi", "ทางเข้้า 2", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 1},
		{7, "2026-08-15", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Aekkachon Nunoi", "ทางเข้า 1,2", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 4},
		{8, "2026-08-15", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Aekkachon Nunoi", "ทางออก2", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 1},
		{9, "2026-08-15", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Thanakorn Puangbupha", "ทางออก4", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 1},
		{10, "2026-08-17", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Aekkachon Nunoi", "ทางเข้า 1,2", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 4},
		{11, "2026-08-17", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Thanakorn Puangbupha", "ทางออก3", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 2},
		{12, "2026-08-18", "รับเข้า", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "ไม่ระบุ", "", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 30},
		{13, "2026-08-20", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Thanakorn Puangbupha", "ทางเข้า 1", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 2},
		{14, "2026-08-20", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Thanakorn Puangbupha", "ทางออก4", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 2},
		{15, "2026-08-22", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Aekkachon Nunoi", "ทางเข้า 1,2", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 4},
		{16, "2026-08-25", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Ruehatrit Hongto", "ทางเข้้า 2", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 1},
		{17, "2026-08-25", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Ruehatrit Hongto", "ทางออก3,4", "นำเข้าจากทะเบียนควบคุมสต็อก", 1, 4},
		{18, "2026-08-26", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Phisutsak Piyapan", "ทางออก1", "", 1, 1},
		{19, "2026-08-29", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Aekkachon Nunoi", "ทางเข้า1,2", "", 1, 4},
		{20, "2026-08-30", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Aekkachon Nunoi", "ทางเข้า 1,2", "", 1, 2},
		{21, "2026-08-30", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Phisutsak Piyapan", "ทางออก3,4", "", 1, 4},
		{22, "2026-08-27", "เบิกใช้", "ถ่าน23A", "วัสดุสิ้นเปลือง", "ก้อน", "Ruehatrit Hongto", "ทางเข้า 1,2", "", 1, 3},
	},
	Users: []User{
		{1, "Ruehatrit Hongto", "IT"},
		{2, "Thanakorn Puangbupha", "IT"},
		{3, "Aekkachon Nunoi", "IT"},
		{4, "Phisutsak Piyapan", "IT"},
	},
}

const dataFile = "/tmp/jpark-store.json"

type stateDoc struct {
	ID    string `bson:"_id"`
	Items []Item `bson:"items"`
	Txs   []Tx   `bson:"txs"`
	Users []User `bson:"users"`
}

func normalizeState() {
	// เติมหมวดให้ transaction เก่าที่อาจยังไม่มี cat
	for i := range st.Txs {
		if st.Txs[i].Cat == "" {
			for _, it := range st.Items {
				if it.ID == st.Txs[i].ItemID {
					st.Txs[i].Cat = it.Cat
					break
				}
			}
		}
	}
}

func connectMongo() error {
	uri := os.Getenv("MONGODB_URI")
	if uri == "" {
		return fmt.Errorf("MONGODB_URI is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	client, err := mongo.Connect(options.Client().ApplyURI(uri))
	if err != nil {
		return err
	}
	if err = client.Ping(ctx, nil); err != nil {
		_ = client.Disconnect(context.Background())
		return err
	}
	mongoClient = client
	stateCol = client.Database("jpark_store").Collection("app_state")
	useMongo = true
	return nil
}

func load() {
	if err := connectMongo(); err == nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		var d stateDoc
		err := stateCol.FindOne(ctx, bson.M{"_id": "main"}).Decode(&d)
		if err == mongo.ErrNoDocuments {
			d = stateDoc{ID: "main", Items: st.Items, Txs: st.Txs, Users: st.Users}
			if _, err = stateCol.InsertOne(ctx, d); err != nil {
				fmt.Println("MongoDB seed error:", err)
				useMongo = false
			}
		} else if err != nil {
			fmt.Println("MongoDB load error:", err)
			useMongo = false
		} else {
			st = State{Items: d.Items, Txs: d.Txs, Users: d.Users}
		}
		if useMongo {
			normalizeState()
			fmt.Println("JPARK Store persistence: MongoDB Atlas")
			return
		}
	} else {
		fmt.Println("MongoDB unavailable, temporary local fallback:", err)
	}

	b, e := os.ReadFile(dataFile)
	if e == nil {
		_ = json.Unmarshal(b, &st)
	}
	normalizeState()
	fmt.Println("JPARK Store persistence: /tmp fallback (NOT permanent)")
}

func save() error {
	if useMongo && stateCol != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		d := stateDoc{ID: "main", Items: st.Items, Txs: st.Txs, Users: st.Users}
		res, err := stateCol.ReplaceOne(ctx, bson.M{"_id": "main"}, d)
		if err != nil {
			return err
		}
		if res.MatchedCount == 0 {
			_, err = stateCol.InsertOne(ctx, d)
		}
		return err
	}
	b, _ := json.MarshalIndent(st, "", "  ")
	return os.WriteFile(dataFile, b, 0644)
}

func nextItem() int {
	m := 0
	for _, x := range st.Items {
		if x.ID > m {
			m = x.ID
		}
	}
	return m + 1
}
func nextTx() int {
	m := 0
	for _, x := range st.Txs {
		if x.ID > m {
			m = x.ID
		}
	}
	return m + 1
}
func nextUser() int {
	m := 0
	for _, x := range st.Users {
		if x.ID > m {
			m = x.ID
		}
	}
	return m + 1
}

func main() {
	initSessionToken()
	load()
	initDailyCheck()
	http.HandleFunc("/api/login", login)
	http.HandleFunc("/api/logout", logout)
	http.HandleFunc("/api/session", sessionStatus)
	http.HandleFunc("/api/state", requireAuth(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		mu.Lock()
		defer mu.Unlock()
		_ = json.NewEncoder(w).Encode(st)
	}))
	http.HandleFunc("/api/action", requireAuth(action))
	http.HandleFunc("/api/export", requireAuth(exportCSV))
	http.HandleFunc("/api/daily/state", requireAuth(dailyStateHandler))
	http.HandleFunc("/api/daily/action", requireAuth(dailyActionHandler))
	http.HandleFunc("/api/daily/export", requireAuth(dailyExportCSV))
	http.Handle("/", http.FileServer(http.Dir(".")))
	p := os.Getenv("PORT")
	if p == "" {
		p = "10000"
	}
	fmt.Println("JPARK Store on :" + p)
	_ = http.ListenAndServe(":"+p, nil)
}

func action(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	var a map[string]interface{}
	if json.NewDecoder(r.Body).Decode(&a) != nil {
		http.Error(w, "bad json", 400)
		return
	}
	mu.Lock()
	defer mu.Unlock()
	ok := true
	errMsg := ""
	act, _ := a["action"].(string)
	s := func(k string) string { v, _ := a[k].(string); return v }
	n := func(k string) int {
		v := a[k]
		switch x := v.(type) {
		case float64:
			return int(x)
		case string:
			i, _ := strconv.Atoi(x)
			return i
		}
		return 0
	}

	switch act {
	case "addItem":
		st.Items = append(st.Items, Item{nextItem(), s("cat"), s("name"), s("unit"), 0, n("min")})
	case "delItem":
		id := n("id")
		z := st.Items[:0]
		for _, x := range st.Items {
			if x.ID != id {
				z = append(z, x)
			}
		}
		st.Items = z
	case "addUser":
		st.Users = append(st.Users, User{nextUser(), s("name"), s("role")})
	case "delUser":
		id := n("id")
		z := st.Users[:0]
		for _, x := range st.Users {
			if x.ID != id {
				z = append(z, x)
			}
		}
		st.Users = z
	case "transaction":
		id, q := n("itemId"), n("qty")
		idx := -1
		for i := range st.Items {
			if st.Items[i].ID == id {
				idx = i
				break
			}
		}
		if idx < 0 {
			ok = false
			errMsg = "ไม่พบรายการ"
			break
		}
		typ := s("type")
		delta := -q
		if typ == "รับเข้า" || typ == "คืนเครื่องมือ" {
			delta = q
		}
		if st.Items[idx].Stock+delta < 0 {
			ok = false
			errMsg = "สต็อกไม่เพียงพอ"
			break
		}
		st.Items[idx].Stock += delta
		st.Txs = append(st.Txs, Tx{
			ID: nextTx(), Date: s("date"), Type: typ, Name: st.Items[idx].Name,
			Cat: st.Items[idx].Cat, Unit: st.Items[idx].Unit, Person: s("person"),
			Loc: s("loc"), Note: s("note"), ItemID: id, Qty: q,
		})
	case "delTx":
		id := n("id")
		z := st.Txs[:0]
		for _, t := range st.Txs {
			if t.ID == id {
				for i := range st.Items {
					if st.Items[i].ID == t.ItemID {
						if t.Type == "รับเข้า" || t.Type == "คืนเครื่องมือ" {
							st.Items[i].Stock -= t.Qty
						} else {
							st.Items[i].Stock += t.Qty
						}
					}
				}
			} else {
				z = append(z, t)
			}
		}
		st.Txs = z
	default:
		ok = false
		errMsg = "unknown action"
	}
	if ok {
		if err := save(); err != nil {
			ok = false
			errMsg = "บันทึกฐานข้อมูลไม่สำเร็จ: " + err.Error()
		}
	}
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": ok, "error": errMsg, "time": time.Now()})
}

func exportCSV(w http.ResponseWriter, r *http.Request) {
	month := r.URL.Query().Get("month")
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=JPARK_Store_Report_%s.csv", month))
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	cw := csv.NewWriter(w)
	defer cw.Flush()
	_ = cw.Write([]string{"วันที่", "ประเภท", "รายการ", "หมวด", "จำนวน", "หน่วย", "ผู้ดำเนินการ", "จุดใช้งาน", "หมายเหตุ"})
	mu.Lock()
	defer mu.Unlock()
	for _, t := range st.Txs {
		if month == "" || (len(t.Date) >= 7 && t.Date[:7] == month) {
			_ = cw.Write([]string{t.Date, t.Type, t.Name, t.Cat, strconv.Itoa(t.Qty), t.Unit, t.Person, t.Loc, t.Note})
		}
	}
}
