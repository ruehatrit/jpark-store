package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"path"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
)

const pmMaxFile = 3 << 20

var pmFileID = regexp.MustCompile(`^[A-Za-z0-9_-]{16,80}$`)

type PMFile struct {
	EvidenceDate string    `json:"evidenceDate,omitempty" bson:"evidenceDate,omitempty"`
	ID           string    `json:"id" bson:"_id"`
	Month        string    `json:"month" bson:"month"`
	Key          string    `json:"key" bson:"key"`
	Name         string    `json:"name" bson:"name"`
	MIME         string    `json:"mime" bson:"mime"`
	Size         int       `json:"size" bson:"size"`
	UploadedBy   string    `json:"uploadedBy" bson:"uploadedBy"`
	UploadedAt   time.Time `json:"uploadedAt" bson:"uploadedAt"`
	Digest       string    `json:"-" bson:"digest"`
	Data         []byte    `json:"-" bson:"data"`
}

func pmNVRKey(key string) bool {
	assetMu.Lock()
	defer assetMu.Unlock()
	for _, a := range assetSt.Assets {
		if !a.Active {
			continue
		}
		k := "asset:" + a.AssetID + "-" + strconv.Itoa(a.ID)
		if a.IP != "" {
			k = "ip:" + a.IP
		}
		if k == key && (strings.Contains(strings.ToLower(a.Category), "nvr") || strings.Contains(strings.ToLower(a.Name), "nvr") || strings.Contains(strings.ToLower(a.AssetID), "nvr")) {
			return true
		}
	}
	return false
}
func pmEquipmentKey(key string) bool {
	assetMu.Lock()
	for _, a := range assetSt.Assets {
		k := "asset:" + a.AssetID + "-" + strconv.Itoa(a.ID)
		if a.IP != "" {
			k = "ip:" + a.IP
		}
		if a.Active && k == key {
			assetMu.Unlock()
			return true
		}
	}
	assetMu.Unlock()
	cctvStore.RLock()
	for _, c := range cctvStore.payload.Cameras {
		k := "cctv:channel-" + strconv.Itoa(c.Channel)
		if c.IP != "" {
			k = "ip:" + c.IP
		}
		if k == key {
			cctvStore.RUnlock()
			return true
		}
	}
	cctvStore.RUnlock()
	for _, ip := range []string{"10.10.36.28", "10.10.36.27", "10.10.36.21", "10.10.36.25", "10.10.36.22", "10.10.36.24", "10.10.36.23", "10.10.36.26"} {
		if key == "ip:"+ip {
			return true
		}
	}
	// Fixed offline camera placeholders use the same stable keys as the PM page.
	for ch := 1; ch <= 25; ch++ {
		if key == "cctv:channel-"+strconv.Itoa(ch) {
			return true
		}
	}
	return false
}
func pmSharedAttachmentKey(ctx context.Context, month, key string) (bool, error) {
	if key == "network:ping" {
		return true, nil
	}
	if !strings.HasPrefix(key, "point:") {
		return false, nil
	}
	name := strings.TrimPrefix(key, "point:")
	if name == "" || len(name) > 140 || name != strings.Join(strings.Fields(name), " ") {
		return false, nil
	}
	for _, p := range []string{"B1 A15", "B1 A16", "B1 B8", "B1 C9", "B1 G11", "B1 H12", "B1 M9", "B2 C18", "B2 B13", "B3 B13", "B3 C12", "B3 F6", "B3 C28", "B3 N8"} {
		if name == p {
			return true, nil
		}
	}
	results, err := pmLoad(month)
	if err != nil {
		return false, err
	}
	for _, r := range results {
		if r.PhotoScope == "point" && strings.Join(strings.Fields(r.Location), " ") == name {
			return true, nil
		}
	}
	return false, nil
}
func pmFileType(name string, data []byte) (string, bool) {
	detected := http.DetectContentType(data)
	switch strings.ToLower(path.Ext(name)) {
	case ".pdf":
		return "application/pdf", bytes.HasPrefix(data, []byte("%PDF-"))
	case ".png":
		return "image/png", detected == "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg", detected == "image/jpeg"
	case ".txt", ".csv":
		if utf8.Valid(data) && !bytes.Contains(data, []byte{0}) {
			if strings.ToLower(path.Ext(name)) == ".csv" {
				return "text/csv; charset=utf-8", true
			}
			return "text/plain; charset=utf-8", true
		}
	}
	return "", false
}
func pmFilesHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if pmFileCol == nil {
		http.Error(w, "ต้องเชื่อม MongoDB เพื่อเก็บไฟล์ผลทดสอบ", 503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
	defer cancel()
	if r.Method == http.MethodGet && r.URL.Query().Get("id") != "" {
		id := r.URL.Query().Get("id")
		if !pmFileID.MatchString(id) {
			w.WriteHeader(400)
			return
		}
		var f PMFile
		err := pmFileCol.FindOne(ctx, bson.M{"_id": id}).Decode(&f)
		if err == mongo.ErrNoDocuments {
			w.WriteHeader(404)
			return
		}
		if err != nil {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", f.MIME)
		disposition := "attachment"
		if r.URL.Query().Get("preview") == "1" && (f.MIME == "image/png" || f.MIME == "image/jpeg") {
			disposition = "inline"
		}
		w.Header().Set("Content-Disposition", mime.FormatMediaType(disposition, map[string]string{"filename": f.Name}))
		_, _ = w.Write(f.Data)
		return
	}
	month, legacy, err := pmPeriod(r)
	if err != nil || legacy {
		http.Error(w, "กรุณาเลือกรอบ PM เป็นเดือน", 400)
		return
	}
	if r.Method == http.MethodGet {
		filter := bson.M{"month": month}
		if key := r.URL.Query().Get("key"); key != "" {
			filter["key"] = key
		}
		cur, err := pmFileCol.Find(ctx, filter, options.Find().SetProjection(bson.M{"data": 0, "digest": 0}).SetSort(bson.D{{Key: "uploadedAt", Value: 1}}))
		if err != nil {
			w.WriteHeader(503)
			return
		}
		defer cur.Close(ctx)
		files := []PMFile{}
		if cur.All(ctx, &files) != nil {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"files": files})
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodDelete && r.Method != http.MethodPatch {
		w.WriteHeader(405)
		return
	}
	actor, ok := currentMember(r)
	if !ok {
		w.WriteHeader(401)
		return
	}
	pmMu.Lock()
	defer pmMu.Unlock()
	if r.Method == http.MethodPatch {
		pmUpdateEvidenceDate(w, r, ctx, month, actor)
		return
	}
	if r.Method == http.MethodDelete {
		r.Body = http.MaxBytesReader(w, r.Body, 2048)
		var q struct {
			ID string `json:"id"`
		}
		if json.NewDecoder(r.Body).Decode(&q) != nil || !pmFileID.MatchString(q.ID) {
			w.WriteHeader(400)
			return
		}
		var f PMFile
		err := pmFileCol.FindOne(ctx, bson.M{"_id": q.ID, "month": month}, options.FindOne().SetProjection(bson.M{"data": 0, "digest": 0})).Decode(&f)
		if err == mongo.ErrNoDocuments {
			w.WriteHeader(404)
			return
		}
		if err != nil {
			w.WriteHeader(503)
			return
		}
		audit, err := beginAudit(actor, "PM", "deleteAttachment", f.ID, f, nil)
		if err != nil {
			http.Error(w, "เก็บ Log ไม่สำเร็จ ยังไม่ลบไฟล์", 503)
			return
		}
		_, err = pmFileCol.DeleteOne(ctx, bson.M{"_id": f.ID, "month": month})
		outcome := "success"
		if err != nil {
			outcome = "failed"
		}
		var after interface{}
		if err != nil {
			after = f
		}
		logErr := finishAudit(audit, outcome, f, after)
		if err != nil {
			http.Error(w, "ลบไฟล์ไม่สำเร็จ", 503)
			return
		}
		if logErr != nil {
			w.Header().Set("X-Audit-Warning", "pending")
		}
		_ = json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	}
	key := r.URL.Query().Get("key")
	validKey := pmEquipmentKey(key)
	if !validKey {
		var e error
		validKey, e = pmSharedAttachmentKey(ctx, month, key)
		if e != nil {
			w.WriteHeader(503)
			return
		}
	}
	if !validKey && strings.HasPrefix(key, "internet:") {
		results, e := pmLoad(month)
		if e != nil {
			w.WriteHeader(503)
			return
		}
		_, validKey = results[key]
	}
	if !validKey {
		http.Error(w, "กรุณาบันทึกผลตรวจของจุดทดสอบก่อนแนบไฟล์ หรือเลือกอุปกรณ์ที่มีในทะเบียน", 400)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, pmMaxFile+(64<<10))
	if r.ParseMultipartForm(64<<10) != nil {
		http.Error(w, "ไฟล์ต้องไม่เกิน 3 MB", 400)
		return
	}
	defer r.MultipartForm.RemoveAll()
	evidenceDate := r.FormValue("evidenceDate")
	if evidenceDate != "" && !validPMEvidenceDate(evidenceDate, time.Now()) {
		http.Error(w, "วันที่ทำ PM / ถ่ายรูปต้องเป็นวันที่จริงและไม่เกินวันนี้", 400)
		return
	}
	id := r.FormValue("uploadId")
	if !pmFileID.MatchString(id) {
		http.Error(w, "รหัสอัปโหลดไม่ถูกต้อง", 400)
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "กรุณาเลือกไฟล์ผลทดสอบ", 400)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, pmMaxFile+1))
	if err != nil || len(data) == 0 || len(data) > pmMaxFile {
		http.Error(w, "ไฟล์ต้องมีข้อมูลและไม่เกิน 3 MB", 400)
		return
	}
	name := path.Base(strings.ReplaceAll(header.Filename, "\\", "/"))
	if name == "." || len(name) > 240 || strings.ContainsAny(name, "\r\n\x00") {
		http.Error(w, "ชื่อไฟล์ไม่ถูกต้อง", 400)
		return
	}
	kind, valid := pmFileType(name, data)
	if !valid {
		http.Error(w, "รองรับ PDF, TXT, CSV, JPG และ PNG", 400)
		return
	}
	sum := sha256.Sum256(data)
	digest := hex.EncodeToString(sum[:])
	var old PMFile
	err = pmFileCol.FindOne(ctx, bson.M{"_id": id}, options.FindOne().SetProjection(bson.M{"data": 0})).Decode(&old)
	if err == nil {
		if old.Month != month || old.Key != key || old.Digest != digest || old.Name != name || old.EvidenceDate != evidenceDate {
			http.Error(w, "รหัสอัปโหลดนี้ถูกใช้แล้ว", 409)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "file": old})
		return
	}
	if err != mongo.ErrNoDocuments {
		w.WriteHeader(503)
		return
	}
	count, err := pmFileCol.CountDocuments(ctx, bson.M{"month": month, "key": key})
	if err != nil {
		w.WriteHeader(503)
		return
	}
	if count >= 5 {
		http.Error(w, "แนบได้ไม่เกิน 5 ไฟล์ต่อจุดหรืออุปกรณ์ต่อเดือน", 400)
		return
	}
	f := PMFile{EvidenceDate: evidenceDate, ID: id, Month: month, Key: key, Name: name, MIME: kind, Size: len(data), UploadedBy: actor.Username, UploadedAt: time.Now().UTC(), Digest: digest, Data: data}
	audit, err := beginAudit(actor, "PM", "uploadAttachment", f.ID, nil, f)
	if err != nil {
		http.Error(w, "เก็บ Log ไม่สำเร็จ ยังไม่อัปโหลดไฟล์", 503)
		return
	}
	_, err = pmFileCol.InsertOne(ctx, f)
	outcome := "success"
	if err != nil {
		outcome = "failed"
	}
	var after interface{}
	if err == nil {
		after = f
	}
	logErr := finishAudit(audit, outcome, nil, after)
	if err != nil {
		http.Error(w, "บันทึกไฟล์ไม่สำเร็จ กรุณาลองใหม่", 503)
		return
	}
	if logErr != nil {
		w.Header().Set("X-Audit-Warning", "pending")
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "file": f})
}
