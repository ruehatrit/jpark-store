package main

import (
 "crypto/subtle"
 "encoding/json"
 "net/http"
 "os"
 "sync"
 "time"
)

type CameraStatus struct { Channel int `json:"channel"`; IP string `json:"ip"`; Online bool `json:"online"` }
type CCTVPayload struct { Cameras []CameraStatus `json:"cameras"`; CollectedAt time.Time `json:"collectedAt"` }
var cctvStore struct { sync.RWMutex; payload CCTVPayload; received time.Time }
func cctvIngest(w http.ResponseWriter,r *http.Request) {
 if r.Method!="POST" {http.Error(w,"method not allowed",405);return}
 secret:=os.Getenv("CCTV_INGEST_TOKEN"); auth:=r.Header.Get("Authorization")
 if len(secret)<32 || subtle.ConstantTimeCompare([]byte(auth),[]byte("Bearer "+secret))!=1 {http.Error(w,"unauthorized",401);return}
 r.Body=http.MaxBytesReader(w,r.Body,64<<10)
 var p CCTVPayload
 if err:=json.NewDecoder(r.Body).Decode(&p);err!=nil || len(p.Cameras)>64 || len(p.Cameras)==0 {http.Error(w,"invalid payload",400);return}
 for _,c:=range p.Cameras {if c.Channel<1 || c.Channel>64 || len(c.IP)>64 {http.Error(w,"invalid camera",400);return}}
 cctvStore.Lock();cctvStore.payload=p;cctvStore.received=time.Now();cctvStore.Unlock()
 extraSyncCCTV(p.Cameras)
 w.Header().Set("Content-Type","application/json");w.Write([]byte(`{"ok":true}`))
}
func cctvStatus(w http.ResponseWriter,r *http.Request){
 if r.Method!="GET" {http.Error(w,"method not allowed",405);return}
 cctvStore.RLock();p:=cctvStore.payload;received:=cctvStore.received;cctvStore.RUnlock()
 w.Header().Set("Content-Type","application/json");w.Header().Set("Cache-Control","no-store")
 _=json.NewEncoder(w).Encode(map[string]interface{}{"cameras":p.Cameras,"collectedAt":p.CollectedAt,"receivedAt":received,"stale":received.IsZero()||time.Since(received)>90*time.Second,"configured":os.Getenv("CCTV_INGEST_TOKEN")!=""})
}
