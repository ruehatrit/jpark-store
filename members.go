package main

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"golang.org/x/crypto/bcrypt"
)

type MemberAccount struct {
	Username     string    `json:"username" bson:"_id"`
	Name         string    `json:"name" bson:"name"`
	Role         string    `json:"role" bson:"role"`
	Active       bool      `json:"active" bson:"active"`
	PasswordHash string    `json:"-" bson:"passwordHash"`
	Version      string    `json:"-" bson:"version"`
	CreatedAt    time.Time `json:"createdAt" bson:"createdAt"`
	UpdatedAt    time.Time `json:"updatedAt" bson:"updatedAt"`
}
type memberSession struct {
	Username, Version string
	Expires           time.Time
}

var memberCol *mongo.Collection
var memberSessions = map[string]memberSession{}
var memberSessionMu sync.Mutex
var memberChangeMu sync.Mutex
var loginSlots = make(chan struct{}, 4)
var loginLimitMu sync.Mutex
var loginLimits = map[string]loginLimit{}
var dummyMemberHash []byte

type loginLimit struct {
	Count int
	Until time.Time
}

var memberUsername = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{2,39}$`)

func initMembers() {
	if useMongo && mongoClient != nil {
		memberCol = mongoClient.Database("jpark_store").Collection("login_accounts")
		initAudit()
	}
	dummyMemberHash, _ = bcrypt.GenerateFromPassword([]byte("invalid-account-password"), 12)
}
func randomMemberToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}
func memberTokenKey(token string) string {
	h := sha256.Sum256([]byte(token))
	return base64.RawURLEncoding.EncodeToString(h[:])
}
func rootMember() MemberAccount {
	return MemberAccount{Username: adminID(), Name: "ผู้ดูแลระบบ", Role: "admin", Active: true, Version: "root"}
}
func currentMember(r *http.Request) (MemberAccount, bool) {
	c, err := r.Cookie("jpark_session")
	if err != nil || len(c.Value) != 43 {
		return MemberAccount{}, false
	}
	key := memberTokenKey(c.Value)
	memberSessionMu.Lock()
	s, ok := memberSessions[key]
	if ok && time.Now().After(s.Expires) {
		delete(memberSessions, key)
		ok = false
	}
	memberSessionMu.Unlock()
	if !ok {
		return MemberAccount{}, false
	}
	if s.Username == adminID() && s.Version == "root" {
		return rootMember(), true
	}
	if memberCol == nil {
		return MemberAccount{}, false
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	var a MemberAccount
	if memberCol.FindOne(ctx, bson.M{"_id": s.Username}).Decode(&a) != nil || !a.Active || a.Version != s.Version {
		return MemberAccount{}, false
	}
	return a, true
}
func isAuthed(r *http.Request) bool { _, ok := currentMember(r); return ok }
func sameSiteMutation(r *http.Request) bool {
	if r.Header.Get("Sec-Fetch-Site") == "cross-site" {
		return false
	}
	if origin := r.Header.Get("Origin"); origin != "" {
		u, err := url.Parse(origin)
		return err == nil && u.Host == r.Host && (u.Scheme == "http" || u.Scheme == "https")
	}
	return true
}
func requireAuth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		actor, ok := currentMember(r)
		if !ok {
			http.Error(w, "กรุณาเข้าสู่ระบบ", 401)
			return
		}
		if r.Method == http.MethodGet || r.Method == http.MethodHead {
			next(w, r)
			return
		}
		if !sameSiteMutation(r) {
			http.Error(w, "ไม่อนุญาตคำขอจากเว็บไซต์อื่น", 403)
			return
		}
		if isAuditedPath(r.URL.Path) {
			runAuditedMutation(w, r, actor, next)
			return
		}
		next(w, r)
	}
}
func requireMemberAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		a, ok := currentMember(r)
		if !ok {
			http.Error(w, "กรุณาเข้าสู่ระบบ", 401)
			return
		}
		if a.Role != "admin" {
			http.Error(w, "เฉพาะแอดมินเท่านั้น", 403)
			return
		}
		if r.Method != http.MethodGet && !sameSiteMutation(r) {
			http.Error(w, "ไม่อนุญาตคำขอจากเว็บไซต์อื่น", 403)
			return
		}
		next(w, r)
	}
}
func loginAllowed(key string) bool {
	loginLimitMu.Lock()
	defer loginLimitMu.Unlock()
	now := time.Now()
	for k, v := range loginLimits {
		if now.After(v.Until) {
			delete(loginLimits, k)
		}
	}
	v := loginLimits[key]
	if v.Count >= 10 {
		return false
	}
	if len(loginLimits) >= 4096 && v.Count == 0 {
		return false
	}
	if v.Count == 0 {
		v.Until = now.Add(15 * time.Minute)
	}
	v.Count++
	loginLimits[key] = v
	return true
}
func login(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if !sameSiteMutation(r) {
		http.Error(w, "forbidden", 403)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var q struct {
		ID       string `json:"id"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&q) != nil || len(q.ID) > 100 || len(q.Password) > 256 {
		http.Error(w, "ข้อมูลไม่ถูกต้อง", 400)
		return
	}
	q.ID = strings.TrimSpace(q.ID)
	if q.ID != adminID() {
		q.ID = strings.ToLower(q.ID)
	}
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	key := ip + "|" + q.ID
	if !loginAllowed(key) {
		http.Error(w, `{"error":"ลองเข้าสู่ระบบหลายครั้ง กรุณารอ 15 นาที"}`, 429)
		return
	}
	select {
	case loginSlots <- struct{}{}:
		defer func() { <-loginSlots }()
	default:
		http.Error(w, `{"error":"มีคำขอจำนวนมาก กรุณาลองอีกครั้ง"}`, 429)
		return
	}
	var a MemberAccount
	valid := false
	if q.ID == adminID() {
		a = rootMember()
		x, y := sha256.Sum256([]byte(q.Password)), sha256.Sum256([]byte(adminPassword()))
		valid = subtle.ConstantTimeCompare(x[:], y[:]) == 1
	} else if memberCol != nil {
		ctx, cancel := context.WithTimeout(r.Context(), 6*time.Second)
		err := memberCol.FindOne(ctx, bson.M{"_id": q.ID}).Decode(&a)
		cancel()
		if err != nil && err != mongo.ErrNoDocuments {
			http.Error(w, `{"error":"เชื่อมต่อบัญชีไม่สำเร็จ กรุณาลองใหม่"}`, 503)
			return
		}
		hash := dummyMemberHash
		if err == nil {
			hash = []byte(a.PasswordHash)
		}
		valid = bcrypt.CompareHashAndPassword(hash, []byte(q.Password)) == nil && err == nil && a.Active
	}
	if !valid {
		_ = writeMemberEvent(q.ID, "", "login", "failed", nil, nil)
		w.WriteHeader(401)
		json.NewEncoder(w).Encode(map[string]interface{}{"ok": false, "error": "ชื่อผู้ใช้หรือรหัสผ่านไม่ถูกต้อง หรือบัญชีถูกปิดใช้งาน"})
		return
	}
	if auditCol == nil || writeMemberEvent(a.Username, a.Name, "login", "success", nil, nil) != nil {
		http.Error(w, `{"error":"ยังเชื่อมต่อฐานข้อมูลสมาชิกและ Log ไม่สำเร็จ"}`, 503)
		return
	}
	token, err := randomMemberToken()
	if err != nil {
		http.Error(w, `{"error":"สร้างเซสชันไม่สำเร็จ"}`, 503)
		return
	}
	memberSessionMu.Lock()
	now := time.Now()
	for k, v := range memberSessions {
		if now.After(v.Expires) {
			delete(memberSessions, k)
		}
	}
	if len(memberSessions) >= 2048 {
		memberSessionMu.Unlock()
		http.Error(w, `{"error":"มีผู้ใช้งานจำนวนมาก กรุณาลองใหม่"}`, 503)
		return
	}
	// Rotate any previous session in this browser.
	if c, e := r.Cookie("jpark_session"); e == nil {
		delete(memberSessions, memberTokenKey(c.Value))
	}
	memberSessions[memberTokenKey(token)] = memberSession{a.Username, a.Version, now.Add(24 * time.Hour)}
	memberSessionMu.Unlock()
	loginLimitMu.Lock()
	delete(loginLimits, key)
	loginLimitMu.Unlock()
	http.SetCookie(w, &http.Cookie{Name: "jpark_session", Value: token, Path: "/", HttpOnly: true, Secure: r.TLS != nil || r.Header.Get("X-Forwarded-Proto") == "https", SameSite: http.SameSiteLaxMode, MaxAge: 86400})
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "user": a})
}
func logout(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.WriteHeader(405)
		return
	}
	if !sameSiteMutation(r) {
		w.WriteHeader(403)
		return
	}
	a, ok := currentMember(r)
	if c, err := r.Cookie("jpark_session"); err == nil {
		memberSessionMu.Lock()
		delete(memberSessions, memberTokenKey(c.Value))
		memberSessionMu.Unlock()
	}
	if ok {
		_ = writeMemberEvent(a.Username, a.Name, "logout", "success", nil, nil)
	}
	http.SetCookie(w, &http.Cookie{Name: "jpark_session", Path: "/", HttpOnly: true, SameSite: http.SameSiteLaxMode, MaxAge: -1})
	json.NewEncoder(w).Encode(map[string]bool{"ok": true})
}
func sessionStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	a, ok := currentMember(r)
	out := map[string]interface{}{"ok": true, "authenticated": ok}
	if ok {
		out["user"] = a
	}
	json.NewEncoder(w).Encode(out)
}
func memberPublic(a MemberAccount) map[string]interface{} {
	return map[string]interface{}{"username": a.Username, "name": a.Name, "role": a.Role, "active": a.Active, "createdAt": a.CreatedAt, "updatedAt": a.UpdatedAt}
}
func membersHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if memberCol == nil || auditCol == nil {
		http.Error(w, "ต้องเชื่อม MongoDB ก่อนจัดการสมาชิก", 503)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	if r.Method == http.MethodGet {
		cur, err := memberCol.Find(ctx, bson.M{})
		if err != nil {
			http.Error(w, "โหลดสมาชิกไม่สำเร็จ", 503)
			return
		}
		defer cur.Close(ctx)
		list := []MemberAccount{rootMember()}
		var other []MemberAccount
		if cur.All(ctx, &other) != nil {
			http.Error(w, "โหลดสมาชิกไม่สำเร็จ", 503)
			return
		}
		list = append(list, other...)
		json.NewEncoder(w).Encode(map[string]interface{}{"accounts": list})
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		w.WriteHeader(405)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 4096)
	var q struct {
		Username string `json:"username"`
		Name     string `json:"name"`
		Role     string `json:"role"`
		Active   bool   `json:"active"`
		Password string `json:"password"`
	}
	if json.NewDecoder(r.Body).Decode(&q) != nil {
		http.Error(w, "ข้อมูลไม่ถูกต้อง", 400)
		return
	}
	q.Username = strings.ToLower(strings.TrimSpace(q.Username))
	q.Name = strings.TrimSpace(q.Name)
	if !memberUsername.MatchString(q.Username) || strings.EqualFold(q.Username, adminID()) || utf8.RuneCountInString(q.Name) < 1 || len(q.Name) > 200 || (q.Role != "staff" && q.Role != "admin") {
		http.Error(w, "กรุณาตรวจชื่อผู้ใช้ ชื่อ และสิทธิ์ บัญชีแอดมินหลักแก้จากหน้านี้ไม่ได้", 400)
		return
	}
	if (r.Method == http.MethodPost || q.Password != "") && (utf8.RuneCountInString(q.Password) < 8 || len(q.Password) > 72) {
		http.Error(w, "รหัสผ่านต้องมีอย่างน้อย 8 ตัวอักษร และไม่เกิน 72 ไบต์", 400)
		return
	}
	actor, _ := currentMember(r)
	if actor.Username == q.Username && (!q.Active || q.Role != "admin") {
		http.Error(w, "ไม่สามารถปิดหรือถอดสิทธิ์บัญชีแอดมินที่กำลังใช้งาน", 400)
		return
	}
	memberChangeMu.Lock()
	defer memberChangeMu.Unlock()
	var old MemberAccount
	err := memberCol.FindOne(ctx, bson.M{"_id": q.Username}).Decode(&old)
	if err != nil && err != mongo.ErrNoDocuments {
		http.Error(w, "อ่านสมาชิกไม่สำเร็จ", 503)
		return
	}
	if r.Method == http.MethodPost && err == nil {
		http.Error(w, "ชื่อผู้ใช้นี้มีแล้ว", 409)
		return
	}
	if r.Method == http.MethodPut && err == mongo.ErrNoDocuments {
		http.Error(w, "ไม่พบสมาชิก", 404)
		return
	}
	next := old
	next.Username = q.Username
	next.Name = q.Name
	next.Role = q.Role
	next.Active = q.Active
	next.UpdatedAt = time.Now().UTC()
	if r.Method == http.MethodPost {
		next.CreatedAt = next.UpdatedAt
	}
	if q.Password != "" {
		hash, e := bcrypt.GenerateFromPassword([]byte(q.Password), 12)
		if e != nil {
			http.Error(w, "ตั้งรหัสผ่านไม่สำเร็จ", 503)
			return
		}
		next.PasswordHash = string(hash)
	}
	// Every account update invalidates all older sessions, including password resets.
	next.Version, err = randomMemberToken()
	if err != nil {
		w.WriteHeader(503)
		return
	}
	before := interface{}(nil)
	if r.Method == http.MethodPut {
		before = memberPublic(old)
	}
	event, err := beginAudit(actor, "สมาชิก", r.Method, q.Username, before, map[string]interface{}{"account": memberPublic(next), "passwordChanged": q.Password != ""})
	if err != nil {
		http.Error(w, "บันทึก Log ไม่สำเร็จ ยังไม่เปลี่ยนบัญชี", 503)
		return
	}
	if r.Method == http.MethodPost {
		_, err = memberCol.InsertOne(ctx, next)
	} else {
		var res *mongo.UpdateResult
		res, err = memberCol.ReplaceOne(ctx, bson.M{"_id": q.Username}, next)
		if err == nil && res.MatchedCount == 0 {
			err = errors.New("account disappeared")
		}
	}
	outcome := "success"
	if err != nil {
		outcome = "failed"
	}
	after := interface{}(nil)
	if err == nil {
		after = memberPublic(next)
	}
	auditErr := finishAudit(event, outcome, before, after)
	if err != nil {
		http.Error(w, "บันทึกสมาชิกไม่สำเร็จ", 503)
		return
	}
	if auditErr != nil {
		w.Header().Set("X-Audit-Warning", "pending")
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"ok": true, "account": next, "auditPending": auditErr != nil})
}
