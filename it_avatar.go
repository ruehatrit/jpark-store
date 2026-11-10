package main

import (
	"bytes"
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"time"
)

var attendanceAvatars *mongo.Collection

type ITAvatar struct {
	Code    string    `json:"code" bson:"_id"`
	MIME    string    `json:"mime" bson:"mime"`
	Updated time.Time `json:"updated" bson:"updated"`
	Data    []byte    `json:"-" bson:"data"`
}

func itAvatarHandler(w http.ResponseWriter, r *http.Request) {
	if attendanceAvatars == nil {
		w.WriteHeader(503)
		return
	}
	code := canonicalEmployeeCode(r.URL.Query().Get("code"))
	ctx, cancel := context.WithTimeout(r.Context(), 12*time.Second)
	defer cancel()
	people, err := employeeProfiles(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	found := false
	for _, p := range people {
		if p.Code == code {
			found = true
		}
	}
	if !found {
		w.WriteHeader(404)
		return
	}
	if r.Method == http.MethodGet {
		var pic ITAvatar
		err := attendanceAvatars.FindOne(ctx, bson.M{"_id": code}).Decode(&pic)
		if err == mongo.ErrNoDocuments {
			w.WriteHeader(404)
			return
		}
		if err != nil {
			w.WriteHeader(503)
			return
		}
		w.Header().Set("Content-Type", pic.MIME)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "private, no-store")
		_, _ = w.Write(pic.Data)
		return
	}
	actor, ok := currentMember(r)
	if !ok || actor.Role != "admin" {
		w.WriteHeader(403)
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, (1<<20)+(64<<10))
	if r.ParseMultipartForm(64<<10) != nil {
		http.Error(w, "รูปต้องไม่เกิน 1 MB", 400)
		return
	}
	defer r.MultipartForm.RemoveAll()
	file, _, err := r.FormFile("file")
	if err != nil {
		w.WriteHeader(400)
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, (1<<20)+1))
	if err != nil || len(data) > 1<<20 {
		w.WriteHeader(400)
		return
	}
	cfg, kind, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (kind != "jpeg" && kind != "png") || cfg.Width > 2048 || cfg.Height > 2048 {
		http.Error(w, "ใช้รูป JPG/PNG ขนาดไม่เกิน 2048 พิกเซล", 400)
		return
	}
	mime := "image/jpeg"
	if kind == "png" {
		mime = "image/png"
	}
	pic := ITAvatar{Code: code, MIME: mime, Updated: time.Now().UTC(), Data: data}
	audit, err := beginAudit(actor, "โปรไฟล์ IT", "uploadAvatar", code, nil, pic)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	_, err = attendanceAvatars.UpdateOne(ctx, bson.M{"_id": code}, bson.M{"$set": pic}, options.UpdateOne().SetUpsert(true))
	if err == nil {
		_, err = attendanceEmployees.UpdateOne(ctx, bson.M{"_id": code}, bson.M{"$set": bson.M{"avatarVersion": pic.Updated.Format(time.RFC3339Nano)}})
	}
	outcome := "success"
	if err != nil {
		outcome = "failed"
	}
	_ = finishAudit(audit, outcome, nil, pic)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
