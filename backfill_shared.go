package main

import (
 "context"
 "encoding/json"
 "fmt"
 "os"
 "strings"
 "time"
 "go.mongodb.org/mongo-driver/v2/bson"
 "go.mongodb.org/mongo-driver/v2/mongo"
 "go.mongodb.org/mongo-driver/v2/mongo/options"
)

const sharedShiftMigration = "oct2026_shared_shift_journal_v1"
const sharedShiftMarker = "/tmp/jpark-oct2026-shared-shift.done"

// Preserve edits as extra text when replacing only the generated routine rows.
func combineSharedDraft(seed WorkEntry, old []WorkEntry) WorkEntry {
 seen:=map[string]bool{}
 for _,x:=range old {
  if x.Date!=seed.Date||len(x.People)!=1||x.People[0]!=seed.People[0] {continue}
  knownTitle:=x.Title=="บันทึกการทำงานประจำวัน"||x.Title=="Support พนักงานลานจอด"||x.Title=="ช่วยพนักงานลานจอด"||x.Title=="ตรวจสอบกล้อง CCTV"||x.Title=="เช็กกล้อง CCTV"||x.Title=="ตรวจสอบและทำความสะอาดตู้ Payment"||x.Title=="เช็กและทำความสะอาดตู้ Payment"
  for _,line:=range strings.Split(x.Note,"\n") {
   clean:=strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line),"• "))
   clean=strings.Split(clean," | ")[0]
   if knownTitle {if at:=strings.Index(clean," — ");at>=0&&!strings.HasPrefix(clean,"ตรวจสอบและทำความสะอาดตู้ชำระเงิน Payment") {clean=clean[at+len(" — "):]}}
   standard:=clean=="ช่วยพนักงานลานจอดใช้งานระบบ และรับแจ้งปัญหาที่เจอ"||clean=="ช่วยเหลือการใช้งานระบบลานจอดและรับแจ้งปัญหาจากพนักงานลานจอด"||clean=="ตรวจสอบสถานะการเชื่อมต่อและภาพจากกล้อง CCTV"||clean=="เช็กภาพกล้อง CCTV ว่าภาพขึ้นและกล้องยังออนไลน์อยู่ไหม"||clean=="เช็กภาพจากกล้อง CCTV ว่าภาพขึ้นและกล้องยังออนไลน์อยู่ไหม"||clean=="เช็กตู้ Payment และเช็ดทำความสะอาดตู้"||strings.HasPrefix(clean,"ตรวจสอบและทำความสะอาดตู้ชำระเงิน Payment — วันที่จัดสรรตัวอย่าง")||strings.HasPrefix(clean,"เช็กตู้ Payment และเช็ดทำความสะอาดตู้ (วันตัวอย่าง")
   if !knownTitle||!standard {v:=strings.TrimSpace(line);if v!=""&&!seen[v] {seed.Note+="\n• "+v;seen[v]=true}}
  }
  if !knownTitle {seed.Note+="\n• "+x.Title}
 }
 // Payment dates are reassigned as a shared shift schedule; review the revision.
 seed.Status="รอยืนยันย้อนหลัง"
 return seed
}

func initSharedShiftBackfill() {
 if !workReady {return}
 var seed []WorkEntry
 if json.Unmarshal([]byte(octoberBackfillJSON),&seed)!=nil {workReady=false;return}
 if workCol!=nil {
  ctx,cancel:=context.WithTimeout(context.Background(),90*time.Second);defer cancel()
  db:=mongoClient.Database("jpark_store");markers:=db.Collection("worklog_migrations")
  err:=markers.FindOne(ctx,bson.M{"_id":sharedShiftMigration}).Err();if err==nil {return};if err!=mongo.ErrNoDocuments {workReady=false;return}
  cur,err:=workCol.Find(ctx,bson.M{"id":bson.M{"$regex":"^backfill-oct2026-"}});if err!=nil {workReady=false;return};defer cur.Close(ctx)
  old:=[]WorkEntry{};if cur.All(ctx,&old)!=nil {workReady=false;return}
  backups:=[]mongo.WriteModel{};for _,x:=range old {backups=append(backups,mongo.NewUpdateOneModel().SetFilter(bson.M{"_id":sharedShiftMigration+":"+x.ID}).SetUpdate(bson.M{"$setOnInsert":x}).SetUpsert(true))}
  if len(backups)>0 {if _,err=db.Collection("worklog_import_backups").BulkWrite(ctx,backups);err!=nil {workReady=false;return}}
  writes:=[]mongo.WriteModel{};ids:=[]string{}
  for _,x:=range seed {x=combineSharedDraft(x,old);ids=append(ids,x.ID);writes=append(writes,mongo.NewUpdateOneModel().SetFilter(bson.M{"_id":x.ID}).SetUpdate(bson.M{"$setOnInsert":x}).SetUpsert(true))}
  if _,err=workCol.BulkWrite(ctx,writes);err!=nil {workReady=false;fmt.Println("Shared shift import failed:",err);return}
  if _,err=workCol.DeleteMany(ctx,bson.M{"id":bson.M{"$regex":"^backfill-oct2026-","$nin":ids}});err!=nil {workReady=false;return}
  if _,err=markers.UpdateOne(ctx,bson.M{"_id":sharedShiftMigration},bson.M{"$setOnInsert":bson.M{"completedAt":time.Now(),"rows":len(seed)}},options.UpdateOne().SetUpsert(true));err!=nil {workReady=false}
  return
 }
 if _,err:=os.Stat(sharedShiftMarker);err==nil {return} else if !os.IsNotExist(err) {workReady=false;return}
 old:=append([]WorkEntry{},workEntries...);backup,_:=json.Marshal(old)
 if err:=os.WriteFile("/tmp/jpark-worklog-before-shared-shift.json",backup,0600);err!=nil {workReady=false;return}
 next:=[]WorkEntry{};seen:=map[string]bool{}
 for _,x:=range workEntries {if !strings.HasPrefix(x.ID,"backfill-oct2026-"){next=append(next,x);seen[x.ID]=true}}
 for _,x:=range seed {if !seen[x.ID] {next=append(next,combineSharedDraft(x,old))}}
 b,err:=json.Marshal(next);if err==nil {err=os.WriteFile(workFile+".new",b,0600)};if err==nil {err=os.Rename(workFile+".new",workFile)};if err==nil {err=os.WriteFile(sharedShiftMarker,[]byte(sharedShiftMigration),0600)}
 if err!=nil {workReady=false;return};workEntries=next
}
