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
func validWork(x *WorkEntry) bool {
 x.Title=strings.TrimSpace(x.Title)
 people:=[]string{};seen:=map[string]bool{}
 for _,v:=range x.People {v=strings.TrimSpace(v);if v!=""&&!seen[v]&&len(v)<=200 {people=append(people,v);seen[v]=true}}
 x.People=people
 _,err:=time.Parse("2006-01-02",x.Date)
 return err==nil&&x.Title!=""&&len(x.Title)<=300&&len(x.Note)<=4000&&len(x.Location)<=300&&len(x.People)>0&&len(x.People)<=30&&len(x.Category)<=100&&(x.Shift=="กลางวัน"||x.Shift=="กลางคืน")&&(x.Status=="เสร็จแล้ว"||x.Status=="กำลังดำเนินการ"||x.Status=="รอดำเนินการ"||x.Status=="รอยืนยันย้อนหลัง")
}
func worklogHandler(w http.ResponseWriter,r *http.Request) {
 w.Header().Set("Content-Type","application/json; charset=utf-8")
 w.Header().Set("Cache-Control","no-store")
 workMu.Lock();defer workMu.Unlock()
 ctx,cancel:=context.WithTimeout(r.Context(),10*time.Second);defer cancel()
 if !workReady {http.Error(w,"อ่านข้อมูลเดิมไม่สำเร็จ",503);return}
 if r.Method==http.MethodGet {
  list:=workEntries
  if workCol!=nil {cur,err:=workCol.Find(ctx,bson.M{});if err!=nil {http.Error(w,"โหลดงานไม่สำเร็จ",503);return};defer cur.Close(ctx);list=[]WorkEntry{};if cur.All(ctx,&list)!=nil {http.Error(w,"โหลดงานไม่สำเร็จ",503);return}}
  json.NewEncoder(w).Encode(map[string]interface{}{"entries":list,"durable":workCol!=nil});return
 }
 if r.Method!=http.MethodPost&&r.Method!=http.MethodPut&&r.Method!=http.MethodDelete {w.WriteHeader(405);return}
 r.Body=http.MaxBytesReader(w,r.Body,16384)
 var x WorkEntry
 if json.NewDecoder(r.Body).Decode(&x)!=nil||len(x.ID)<8||len(x.ID)>80 {http.Error(w,"ข้อมูลหรือรหัสงานไม่ถูกต้อง",400);return}
 if r.Method!=http.MethodDelete&&!validWork(&x) {http.Error(w,"กรุณาตรวจวันที่ ชื่อผู้ทำงาน กะ และรายละเอียด",400);return}
 if err:=checkWorkDay(ctx,r.Method,x);err!=nil {http.Error(w,err.Error(),409);return}
 var err error
 if workCol!=nil {
  switch r.Method {
  case http.MethodPost:
   // Retried creates never overwrite a record already stored under this ID.
   _,err=workCol.UpdateOne(ctx,bson.M{"_id":x.ID},bson.M{"$setOnInsert":x},options.UpdateOne().SetUpsert(true))
  case http.MethodPut:
   var res *mongo.UpdateResult
   res,err=workCol.UpdateOne(ctx,bson.M{"_id":x.ID},bson.M{"$set":x})
   if err==nil&&res.MatchedCount==0 {http.Error(w,"ไม่พบงานที่ต้องการแก้ไข กรุณาโหลดล่าสุด",404);return}
  case http.MethodDelete:
   _,err=workCol.DeleteOne(ctx,bson.M{"_id":x.ID})
  }
 } else {
  idx:=-1;for i,e:=range workEntries {if e.ID==x.ID {idx=i;break}}
  if r.Method==http.MethodPost&&idx>=0 {json.NewEncoder(w).Encode(map[string]bool{"ok":true});return}
  if r.Method==http.MethodPut&&idx<0 {http.Error(w,"ไม่พบงานที่ต้องการแก้ไข กรุณาโหลดล่าสุด",404);return}
  next:=append([]WorkEntry{},workEntries...)
  switch r.Method {case http.MethodPost:next=append(next,x);case http.MethodPut:next[idx]=x;case http.MethodDelete:if idx>=0 {next=append(next[:idx],next[idx+1:]...)}}
  var b []byte;b,err=json.Marshal(next);if err==nil {err=os.WriteFile(workFile+".new",b,0600)};if err==nil {err=os.Rename(workFile+".new",workFile)};if err==nil {workEntries=next}
 }
 if err!=nil {http.Error(w,"บันทึกงานไม่สำเร็จ กรุณาลองอีกครั้ง",503);return}
 json.NewEncoder(w).Encode(map[string]bool{"ok":true})
}
