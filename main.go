package main

import (
    "encoding/csv"
    "encoding/json"
    "fmt"
    "net/http"
    "os"
    "strconv"
    "sync"
    "time"
)

type Item struct {
    ID    int    `json:"id"`
    Cat   string `json:"cat"`
    Name  string `json:"name"`
    Unit  string `json:"unit"`
    Stock int    `json:"stock"`
    Min   int    `json:"min"`
}

type Tx struct {
    ID     int    `json:"id"`
    Date   string `json:"date"`
    Type   string `json:"type"`
    Name   string `json:"name"`
    Cat    string `json:"cat"`
    Unit   string `json:"unit"`
    Person string `json:"person"`
    Loc    string `json:"loc"`
    Note   string `json:"note"`
    ItemID int    `json:"itemId"`
    Qty    int    `json:"qty"`
}

type User struct {
    ID   int    `json:"id"`
    Name string `json:"name"`
    Role string `json:"role"`
}

type State struct {
    Items []Item `json:"items"`
    Txs   []Tx   `json:"txs"`
    Users []User `json:"users"`
}

var mu sync.Mutex
var st = State{
    Items: []Item{
        {1, "วัสดุสิ้นเปลือง", "ถ่านรีโมท 23A", "ก้อน", 30, 10},
        {2, "วัสดุสิ้นเปลือง", "กระดาษ Payment / Kiosk", "ม้วน", 287, 20},
        {3, "วัสดุสิ้นเปลือง", "กระดาษ Handheld", "ม้วน", 50, 10},
        {4, "เครื่องมือช่าง", "มัลติมิเตอร์", "เครื่อง", 3, 1},
        {5, "เครื่องมือช่าง", "ชุดไขควง", "ชุด", 5, 1},
        {6, "เครื่องมือช่าง", "คีมย้ำสาย LAN", "อัน", 3, 1},
    },
    Txs: []Tx{},
    Users: []User{
        {1, "ผู้ดูแล Store", "IT"},
        {2, "ช่าง IT 1", "Technician"},
        {3, "ช่าง IT 2", "Technician"},
    },
}

const dataFile = "/tmp/jpark-store.json"

func load() {
    b, e := os.ReadFile(dataFile)
    if e == nil {
        _ = json.Unmarshal(b, &st)
    }
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

func save() {
    b, _ := json.MarshalIndent(st, "", "  ")
    _ = os.WriteFile(dataFile, b, 0644)
}

func nextItem() int { m := 0; for _, x := range st.Items { if x.ID > m { m = x.ID } }; return m + 1 }
func nextTx() int   { m := 0; for _, x := range st.Txs { if x.ID > m { m = x.ID } }; return m + 1 }
func nextUser() int { m := 0; for _, x := range st.Users { if x.ID > m { m = x.ID } }; return m + 1 }

func main() {
    load()
    http.HandleFunc("/api/state", func(w http.ResponseWriter, r *http.Request) {
        w.Header().Set("Content-Type", "application/json; charset=utf-8")
        mu.Lock(); defer mu.Unlock()
        _ = json.NewEncoder(w).Encode(st)
    })
    http.HandleFunc("/api/action", action)
    http.HandleFunc("/api/export", exportCSV)
    http.Handle("/", http.FileServer(http.Dir(".")))
    p := os.Getenv("PORT")
    if p == "" { p = "10000" }
    fmt.Println("JPARK Store on :" + p)
    _ = http.ListenAndServe(":"+p, nil)
}

func action(w http.ResponseWriter, r *http.Request) {
    w.Header().Set("Content-Type", "application/json; charset=utf-8")
    var a map[string]interface{}
    if json.NewDecoder(r.Body).Decode(&a) != nil { http.Error(w, "bad json", 400); return }
    mu.Lock(); defer mu.Unlock()
    ok := true; errMsg := ""
    act, _ := a["action"].(string)
    s := func(k string) string { v, _ := a[k].(string); return v }
    n := func(k string) int {
        v := a[k]
        switch x := v.(type) {
        case float64: return int(x)
        case string: i, _ := strconv.Atoi(x); return i
        }
        return 0
    }

    switch act {
    case "addItem":
        st.Items = append(st.Items, Item{nextItem(), s("cat"), s("name"), s("unit"), 0, n("min")})
    case "delItem":
        id := n("id"); z := st.Items[:0]
        for _, x := range st.Items { if x.ID != id { z = append(z, x) } }
        st.Items = z
    case "addUser":
        st.Users = append(st.Users, User{nextUser(), s("name"), s("role")})
    case "delUser":
        id := n("id"); z := st.Users[:0]
        for _, x := range st.Users { if x.ID != id { z = append(z, x) } }
        st.Users = z
    case "transaction":
        id, q := n("itemId"), n("qty")
        idx := -1
        for i := range st.Items { if st.Items[i].ID == id { idx = i; break } }
        if idx < 0 { ok = false; errMsg = "ไม่พบรายการ"; break }
        typ := s("type"); delta := -q
        if typ == "รับเข้า" || typ == "คืนเครื่องมือ" { delta = q }
        if st.Items[idx].Stock+delta < 0 { ok = false; errMsg = "สต็อกไม่เพียงพอ"; break }
        st.Items[idx].Stock += delta
        st.Txs = append(st.Txs, Tx{
            ID: nextTx(), Date: s("date"), Type: typ, Name: st.Items[idx].Name,
            Cat: st.Items[idx].Cat, Unit: st.Items[idx].Unit, Person: s("person"),
            Loc: s("loc"), Note: s("note"), ItemID: id, Qty: q,
        })
    case "delTx":
        id := n("id"); z := st.Txs[:0]
        for _, t := range st.Txs {
            if t.ID == id {
                for i := range st.Items {
                    if st.Items[i].ID == t.ItemID {
                        if t.Type == "รับเข้า" || t.Type == "คืนเครื่องมือ" { st.Items[i].Stock -= t.Qty } else { st.Items[i].Stock += t.Qty }
                    }
                }
            } else { z = append(z, t) }
        }
        st.Txs = z
    default:
        ok = false; errMsg = "unknown action"
    }
    if ok { save() }
    _ = json.NewEncoder(w).Encode(map[string]interface{}{ "ok": ok, "error": errMsg, "time": time.Now() })
}

func exportCSV(w http.ResponseWriter, r *http.Request) {
    month := r.URL.Query().Get("month")
    w.Header().Set("Content-Type", "text/csv; charset=utf-8")
    w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=JPARK_Store_Report_%s.csv", month))
    _, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
    cw := csv.NewWriter(w); defer cw.Flush()
    _ = cw.Write([]string{"วันที่","ประเภท","รายการ","หมวด","จำนวน","หน่วย","ผู้ดำเนินการ","จุดใช้งาน","หมายเหตุ"})
    mu.Lock(); defer mu.Unlock()
    for _, t := range st.Txs {
        if month == "" || (len(t.Date) >= 7 && t.Date[:7] == month) {
            _ = cw.Write([]string{t.Date,t.Type,t.Name,t.Cat,strconv.Itoa(t.Qty),t.Unit,t.Person,t.Loc,t.Note})
        }
    }
}
