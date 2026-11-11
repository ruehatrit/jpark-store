package main

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

type memberManagementGrant struct {
	Session, Username, Version string
	Expires                    time.Time
}

var managementGrantMu sync.Mutex
var managementGrants = map[string]memberManagementGrant{}

const memberManagementTTL = 10 * time.Minute

func requireMemberManagement(next http.HandlerFunc) http.HandlerFunc {
	return requireMemberAdmin(func(w http.ResponseWriter, r *http.Request) {
		actor, ok := currentMember(r)
		if !ok {
			w.WriteHeader(401)
			return
		}
		cookie, err := r.Cookie("jpark_session")
		proof := r.Header.Get("X-Member-Confirmation")
		valid := false
		if err == nil && len(proof) == 43 {
			managementGrantMu.Lock()
			g, exists := managementGrants[memberTokenKey(proof)]
			valid = exists && time.Now().Before(g.Expires) && g.Session == memberTokenKey(cookie.Value) && g.Username == actor.Username && g.Version == actor.Version
			if exists && !time.Now().Before(g.Expires) {
				delete(managementGrants, memberTokenKey(proof))
			}
			managementGrantMu.Unlock()
		}
		if !valid {
			http.Error(w, "กรุณายืนยันรหัสผ่าน admin หลักก่อนเข้าหน้าสมาชิกและ Log", http.StatusPreconditionRequired)
			return
		}
		next(w, r)
	})
}
func memberConfirmHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	actor, ok := currentMember(r)
	if !ok || actor.Role != "admin" {
		w.WriteHeader(403)
		return
	}
	cookie, e := r.Cookie("jpark_session")
	if e != nil {
		w.WriteHeader(401)
		return
	}
	if r.Method == http.MethodDelete {
		proof := r.Header.Get("X-Member-Confirmation")
		managementGrantMu.Lock()
		key := memberTokenKey(proof)
		if g, ok := managementGrants[key]; ok && g.Session == memberTokenKey(cookie.Value) {
			delete(managementGrants, key)
		}
		managementGrantMu.Unlock()
		json.NewEncoder(w).Encode(map[string]bool{"ok": true})
		return
	}
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 2048)
	var q struct {
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&q) != nil || q.Password == "" || len(q.Password) > 256 {
		http.Error(w, "กรอกรหัสผ่าน admin หลัก", 400)
		return
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	limit := "member-confirm|" + ip + "|" + strings.ToLower(actor.Username)
	if !loginAllowed(limit) {
		http.Error(w, "ลองรหัสหลายครั้ง กรุณารอ 15 นาที", 429)
		return
	}
	x, y := sha256.Sum256([]byte(q.Password)), sha256.Sum256([]byte(adminPassword()))
	if subtle.ConstantTimeCompare(x[:], y[:]) != 1 {
		_ = writeMemberEvent(actor.Username, actor.Name, "confirmAdmin", "failed", nil, nil)
		http.Error(w, "รหัสผ่าน admin หลักไม่ถูกต้อง", 401)
		return
	}
	if auditCol == nil || writeMemberEvent(actor.Username, actor.Name, "confirmAdmin", "success", nil, nil) != nil {
		http.Error(w, "เก็บ Log ไม่สำเร็จ ยังไม่เปิดหน้าจัดการสมาชิก", 503)
		return
	}
	loginLimitMu.Lock()
	delete(loginLimits, limit)
	loginLimitMu.Unlock()
	token, e := randomMemberToken()
	if e != nil {
		w.WriteHeader(503)
		return
	}
	expires := time.Now().Add(memberManagementTTL)
	managementGrantMu.Lock()
	for key, g := range managementGrants {
		if time.Now().After(g.Expires) {
			delete(managementGrants, key)
		}
	}
	managementGrants[memberTokenKey(token)] = memberManagementGrant{Session: memberTokenKey(cookie.Value), Username: actor.Username, Version: actor.Version, Expires: expires}
	managementGrantMu.Unlock()
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "confirmation": token, "expiresAt": expires, "ttlSeconds": int(memberManagementTTL / time.Second)})
}
