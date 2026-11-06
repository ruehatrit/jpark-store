package main

import (
 "context"
 "encoding/json"
 "net/http"
 "os"
 "strings"
 "sync"
 "time"
 "go.mongodb.org/mongo-driver/v2/bson"
 "go.mongodb.org/mongo-driver/v2/mongo"
 "go.mongodb.org/mongo-driver/v2/mongo/options"
)

type WorkEntry struct {
 ID string `json:"id" bson:"id"`
 Date string `json:"date" bson:"date"`
 Shift string `json:"shift" bson:"shift"`
 People []string `json:"people" bson:"people"`
 Title string `json:"title" bson:"title"`
 Category string `json:"category" bson:"category"`
 Location string `json:"location" bson:"location"`
 Note string `json:"note" bson:"note"`
 Status string `json:"status" bson:"status"`
}
var workMu sync.Mutex
var workEntries = []WorkEntry{}
var workCol *mongo.Collection
var workReady = true
const workFile = "/tmp/jpark-worklog.json"
func initWorklog() {
 if useMongo && mongoClient != nil { workCol=mongoClient.Database("jpark_store").Collection("it_worklog"); return }
 b,err:=os.ReadFile(workFile); if err==nil { workReady=json.Unmarshal(b,&workEntries)==nil } else if !os.IsNotExist(err) { workReady=false }
}
func worklogHandler(w http.ResponseWriter,r *http.Request) {
 w.Header().Set("Content-Type","application/json; charset=utf-8")
 w.Header().Set("Cache-Control","no-store")
 workMu.Lock(); defer workMu.Unlock()
 ctx,cancel:=context.WithTimeout(r.Context(),10*time.Second);defer cancel()
 if r.Method==http.MethodGet {
  list:=workEntries
  if workCol!=nil { cur,err:=workCol.Find(ctx,bson.M{});if err!=nil {http.Error(w,"โหลดงานไม่สำเร็จ",503);return};defer cur.Close(ctx);list=[]WorkEntry{};if cur.All(ctx,&list)!=nil {http.Error(w,"โหลดงานไม่สำเร็จ",503);return} }
  if !workReady {http.Error(w,"อ่านข้อมูลเดิมไม่สำเร็จ",503);return}
  json.NewEncoder(w).Encode(map[string]interface{}{"entries":list,"durable":workCol!=nil});return
 }
 if r.Method!=http.MethodPost {w.WriteHeader(405);return}
 if !workReady {http.Error(w,"อ่านข้อมูลเดิมไม่สำเร็จ",503);return}
 r.Body=http.MaxBytesReader(w,r.Body,16384)
 var x WorkEntry
 if json.NewDecoder(r.Body).Decode(&x)!=nil {http.Error(w,"ข้อมูลไม่ถูกต้อง",400);return}
 x.Title=strings.TrimSpace(x.Title)
 people:=[]string{}; seen:=map[string]bool{}
 for _,v:=range x.People {v=strings.TrimSpace(v);if v!=""&&!seen[v]&&len(v)<=200 {people=append(people,v);seen[v]=true}}
 x.People=people
 if _,err:=time.Parse("2006-01-02",x.Date);err!=nil||x.Title==""||len(x.Title)>300||len(x.Note)>4000||len(x.Location)>300||len(x.People)==0||len(x.People)>30||len(x.Category)>100||(x.Shift!="กลางวัน"&&x.Shift!="กลางคืน")||(x.Status!="เสร็จแล้ว"&&x.Status!="กำลังดำเนินการ"&&x.Status!="รอดำเนินการ") {http.Error(w,"กรุณาตรวจวันที่ ชื่อผู้ทำงาน กะ และรายละเอียด",400);return}
 // Client-generated IDs make retries safe if the response is lost.
 if len(x.ID)<8||len(x.ID)>80 {http.Error(w,"รหัสงานไม่ถูกต้อง",400);return}
 var err error
 if workCol!=nil {_,err=workCol.UpdateOne(ctx,bson.M{"_id":x.ID},bson.M{"$setOnInsert":x},options.UpdateOne().SetUpsert(true))} else {
  for _,e:=range workEntries {if e.ID==x.ID {json.NewEncoder(w).Encode(map[string]bool{"ok":true});return}}
  next:=append(append([]WorkEntry{},workEntries...),x);var b []byte;b,err=json.Marshal(next);if err==nil {err=os.WriteFile(workFile+".new",b,0600)};if err==nil {err=os.Rename(workFile+".new",workFile)};if err==nil {workEntries=next}
 }
 if err!=nil {http.Error(w,"บันทึกงานไม่สำเร็จ กรุณาลองอีกครั้ง",503);return}
 json.NewEncoder(w).Encode(map[string]bool{"ok":true})
}
