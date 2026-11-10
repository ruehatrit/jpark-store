package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"encoding/xml"
	"errors"
	"fmt"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"io"
	"net/http"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"
)

type xText struct {
	T    string `xml:"t"`
	Runs []struct {
		T string `xml:"t"`
	} `xml:"r"`
}

func (x xText) value() string {
	s := x.T
	for _, r := range x.Runs {
		s += r.T
	}
	return s
}

type xCell struct {
	Ref    string `xml:"r,attr"`
	Type   string `xml:"t,attr"`
	Value  string `xml:"v"`
	Inline xText  `xml:"is"`
}
type xRow struct {
	Cells []xCell `xml:"c"`
}
type RosterPreview struct {
	Month    string          `json:"month"`
	Source   string          `json:"source"`
	Rows     []EmployeeShift `json:"rows"`
	Counts   map[string]int  `json:"counts"`
	Warnings []string        `json:"warnings"`
	Hash     string          `json:"hash"`
}

func xColumn(ref string) int {
	n := 0
	for _, c := range ref {
		if c < 'A' || c > 'Z' {
			break
		}
		n = n*26 + int(c-'A'+1)
	}
	return n
}
func rosterName(s string) string { return strings.Join(strings.Fields(s), "") }
func readITRoster(data []byte, filename string, people []EmployeeProfile) (RosterPreview, error) {
	out := RosterPreview{Source: filename, Rows: []EmployeeShift{}, Counts: map[string]int{}, Warnings: []string{}}
	z, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return out, errors.New("ไฟล์ต้องเป็น Excel .xlsx")
	}
	if len(z.File) > 3000 {
		return out, errors.New("ไฟล์มีข้อมูลมากเกินไป")
	}
	entries := map[string]*zip.File{}
	var total uint64
	for _, f := range z.File {
		total += f.UncompressedSize64
		if total > 50<<20 {
			return out, errors.New("ไฟล์ขยายใหญ่เกินไป")
		}
		entries[path.Clean(f.Name)] = f
	}
	read := func(name string) ([]byte, error) {
		f := entries[path.Clean(name)]
		if f == nil {
			return nil, fmt.Errorf("ไม่พบ %s", name)
		}
		if f.UncompressedSize64 > 10<<20 {
			return nil, errors.New("ตารางใหญ่เกินไป")
		}
		r, e := f.Open()
		if e != nil {
			return nil, e
		}
		defer r.Close()
		return io.ReadAll(io.LimitReader(r, 10<<20))
	}
	var stringsDoc struct {
		Items []xText `xml:"si"`
	}
	if b, e := read("xl/sharedStrings.xml"); e == nil {
		if xml.Unmarshal(b, &stringsDoc) != nil {
			return out, errors.New("อ่านข้อความใน Excel ไม่สำเร็จ")
		}
	}
	var workbook struct {
		Sheets []struct {
			Name string `xml:"name,attr"`
			RID  string `xml:"id,attr"`
		} `xml:"sheets>sheet"`
		Props struct {
			Date1904 string `xml:"date1904,attr"`
		} `xml:"workbookPr"`
	}
	b, e := read("xl/workbook.xml")
	if e != nil || xml.Unmarshal(b, &workbook) != nil {
		return out, errors.New("อ่าน workbook ไม่สำเร็จ")
	}
	var rels struct {
		Items []struct {
			ID     string `xml:"Id,attr"`
			Target string `xml:"Target,attr"`
		} `xml:"Relationship"`
	}
	b, e = read("xl/_rels/workbook.xml.rels")
	if e != nil || xml.Unmarshal(b, &rels) != nil {
		return out, errors.New("อ่าน worksheet ไม่สำเร็จ")
	}
	sheetID := ""
	for _, s := range workbook.Sheets {
		if sheetID == "" {
			sheetID = s.RID
		}
		if strings.EqualFold(strings.TrimSpace(s.Name), "IT") {
			sheetID = s.RID
			break
		}
	}
	sheetPath := ""
	for _, r := range rels.Items {
		if r.ID == sheetID {
			if strings.HasPrefix(r.Target, "/") {
				sheetPath = strings.TrimPrefix(r.Target, "/")
			} else {
				sheetPath = path.Join("xl", r.Target)
			}
		}
	}
	b, e = read(sheetPath)
	if e != nil {
		return out, e
	}
	var sheet struct {
		Rows []xRow `xml:"sheetData>row"`
	}
	if xml.Unmarshal(b, &sheet) != nil {
		return out, errors.New("อ่านช่องตารางไม่สำเร็จ")
	}
	value := func(c xCell) string {
		if c.Type == "s" {
			i, e := strconv.Atoi(c.Value)
			if e == nil && i >= 0 && i < len(stringsDoc.Items) {
				return stringsDoc.Items[i].value()
			}
			return ""
		}
		if c.Type == "inlineStr" {
			return c.Inline.value()
		}
		return c.Value
	}
	dateValue := func(c xCell) string {
		v := value(c)
		if c.Type == "" || c.Type == "n" {
			n, e := strconv.ParseFloat(v, 64)
			if e == nil && n > 35000 && n < 80000 {
				base := time.Date(1899, 12, 30, 0, 0, 0, 0, time.UTC)
				if workbook.Props.Date1904 == "1" || workbook.Props.Date1904 == "true" {
					base = time.Date(1904, 1, 1, 0, 0, 0, 0, time.UTC)
				}
				return base.AddDate(0, 0, int(n)).Format("2006-01-02")
			}
		}
		for _, layout := range []string{"2006-01-02", "2/1/2006"} {
			if d, e := time.Parse(layout, v); e == nil {
				return d.Format("2006-01-02")
			}
		}
		return ""
	}
	header := map[int]string{}
	for _, row := range sheet.Rows {
		candidate := map[int]string{}
		for _, c := range row.Cells {
			if d := dateValue(c); d != "" {
				candidate[xColumn(c.Ref)] = d
			}
		}
		if len(candidate) > len(header) {
			header = candidate
		}
	}
	if len(header) < 20 {
		return out, errors.New("ไม่พบแถววันที่ ใช้รูปแบบตาราง IT เดิม")
	}
	months := map[string]int{}
	for _, d := range header {
		months[d[:7]]++
	}
	count := 0
	for m, n := range months {
		if n > count {
			out.Month = m
			count = n
		}
	}
	start, e := time.Parse("2006-01", out.Month)
	if e != nil || count != start.AddDate(0, 1, -1).Day() {
		return out, errors.New("วันที่ในเดือนไม่ครบหรือซ้ำ")
	}
	days := map[string]bool{}
	for col, d := range header {
		if !strings.HasPrefix(d, out.Month) {
			delete(header, col)
			continue
		}
		if days[d] {
			return out, errors.New("วันที่ซ้ำในตาราง")
		}
		days[d] = true
	}
	known := map[string]EmployeeProfile{}
	for _, p := range people {
		if p.Active {
			known[rosterName(p.Name)] = p
		}
	}
	marks := map[string]map[string]bool{}
	matched := map[string]bool{}
	for _, row := range sheet.Rows {
		var p EmployeeProfile
		found := false
		for _, c := range row.Cells {
			if xColumn(c.Ref) <= 8 {
				if x, ok := known[rosterName(value(c))]; ok {
					p = x
					found = true
					break
				}
			}
		}
		if !found {
			continue
		}
		matched[p.Code] = true
		for col, d := range header {
			k := d + ":" + p.Code
			if marks[k] == nil {
				marks[k] = map[string]bool{}
			}
			for _, c := range row.Cells {
				if xColumn(c.Ref) != col {
					continue
				}
				v := strings.ToUpper(strings.TrimSpace(value(c)))
				if v != "" && v != "D" && v != "N" && v != "OFF" && v != "SL" {
					return out, fmt.Errorf("พบสัญลักษณ์ %s ที่ %s ของ %s", v, d, p.Name)
				}
				if v != "" {
					marks[k][v] = true
				}
			}
		}
	}
	if len(matched) == 0 {
		return out, errors.New("ชื่อในไฟล์ไม่ตรงกับทะเบียนทีมไอที")
	}
	for _, p := range people {
		if p.Active && !matched[p.Code] {
			out.Warnings = append(out.Warnings, "ไม่พบชื่อ "+p.Name+" ในไฟล์ จะไม่แก้กะของคนนี้")
		}
	}
	for key, m := range marks {
		date, code := key[:10], key[11:]
		shift := "unassigned"
		if m["D"] && m["N"] {
			return out, fmt.Errorf("%s รหัส %s มีทั้ง D และ N ต้องแก้ไฟล์ก่อน", date, code)
		}
		if m["D"] {
			shift = "day"
		} else if m["N"] {
			shift = "night"
		} else if m["SL"] {
			shift = "leave"
		} else if m["OFF"] {
			shift = "off"
		}
		out.Rows = append(out.Rows, EmployeeShift{ID: key, Date: date, Code: code, Shift: shift, Source: filename})
		out.Counts[shift]++
	}
	sort.Slice(out.Rows, func(i, j int) bool { return out.Rows[i].ID < out.Rows[j].ID })
	canonical, _ := json.Marshal(out.Rows)
	hash := sha256.Sum256(append(data, canonical...))
	out.Hash = hex.EncodeToString(hash[:])
	return out, nil
}
func applyITRoster(ctx context.Context, rows []EmployeeShift) error {
	models := []mongo.WriteModel{}
	for _, r := range rows {
		models = append(models, mongo.NewUpdateOneModel().SetFilter(bson.M{"_id": r.ID}).SetUpdate(bson.M{"$set": r}).SetUpsert(true))
	}
	if len(models) == 0 {
		return errors.New("ไม่มีรายการกะ")
	}
	_, err := attendanceShifts.BulkWrite(ctx, models)
	return err
}
func itRosterHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if attendanceShifts == nil {
		w.WriteHeader(503)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, (5<<20)+(64<<10))
	if r.ParseMultipartForm(64<<10) != nil {
		http.Error(w, "ไฟล์ต้องไม่เกิน 5 MB", 400)
		return
	}
	defer r.MultipartForm.RemoveAll()
	f, h, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(400)
		return
	}
	defer f.Close()
	if len(h.Filename) > 240 {
		http.Error(w, "ชื่อไฟล์ยาวเกินไป", 400)
		return
	}
	data, err := io.ReadAll(io.LimitReader(f, (5<<20)+1))
	if err != nil || len(data) > 5<<20 {
		w.WriteHeader(400)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 25*time.Second)
	defer cancel()
	people, err := employeeProfiles(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	preview, err := readITRoster(data, path.Base(h.Filename), people)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	action := r.FormValue("action")
	if action == "preview" {
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(preview)
		return
	}
	if action != "commit" || r.FormValue("hash") != preview.Hash {
		http.Error(w, "กรุณาดูตัวอย่างใหม่ก่อนยืนยัน", 409)
		return
	}
	actor, _ := currentMember(r)
	ids := make([]string, 0, len(preview.Rows))
	for _, row := range preview.Rows {
		ids = append(ids, row.ID)
	}
	cur, err := attendanceShifts.Find(ctx, bson.M{"_id": bson.M{"$in": ids}})
	if err != nil {
		w.WriteHeader(503)
		return
	}
	var before []EmployeeShift
	if err = cur.All(ctx, &before); err != nil {
		w.WriteHeader(503)
		return
	}
	audit, err := beginAudit(actor, "ตารางกะ IT", "importExcel", preview.Month, before, preview)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	err = applyITRoster(ctx, preview.Rows)
	outcome := "success"
	if err != nil {
		outcome = "failed"
	}
	_ = finishAudit(audit, outcome, before, preview.Rows)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "month": preview.Month, "count": len(preview.Rows)})
}
func initNovemberITRoster() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	markers := mongoClient.Database("jpark_store").Collection("attendance_roster_imports")
	id := "it_roster_nov2026_from_file_v1"
	err := markers.FindOne(ctx, bson.M{"_id": id}).Err()
	if err == nil {
		return
	}
	if err != mongo.ErrNoDocuments {
		return
	}
	var rows []EmployeeShift
	if json.Unmarshal([]byte(novemberITRosterJSON), &rows) != nil {
		return
	}
	if applyITRoster(ctx, rows) != nil {
		return
	}
	_, _ = markers.UpdateOne(ctx, bson.M{"_id": id}, bson.M{"$setOnInsert": bson.M{"completedAt": time.Now(), "count": len(rows)}}, options.UpdateOne().SetUpsert(true))
}
