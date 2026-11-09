package main

import (
 "context"
 "encoding/json"
 "fmt"
 "os"
 "time"
 "go.mongodb.org/mongo-driver/v2/bson"
 "go.mongodb.org/mongo-driver/v2/mongo"
 "go.mongodb.org/mongo-driver/v2/mongo/options"
)
const septemberMigration = "attendance_sep2026_routine_v1"
const septemberMarkerFile = "/tmp/jpark-backfill-sep2026.done"

// Initial import only. The migration marker prevents deleted/edited tasks from
// being recreated when Render restarts. Stable IDs make partial retries safe.
func initSeptemberBackfill() {
 if !workReady {return}
 var seed []WorkEntry
 if err:=json.Unmarshal([]byte(septemberBackfillJSON),&seed);err!=nil {workReady=false;fmt.Println("Backfill data invalid:",err);return}
 for i:=range seed {if !validWork(&seed[i]) {workReady=false;fmt.Println("Backfill row invalid:",seed[i].ID);return}}
 if workCol!=nil {
  markers:=mongoClient.Database("jpark_store").Collection("worklog_migrations")
  ctx,cancel:=context.WithTimeout(context.Background(),90*time.Second);defer cancel()
  err:=markers.FindOne(ctx,bson.M{"_id":septemberMigration}).Err()
  if err==nil {return}
  if err!=mongo.ErrNoDocuments {workReady=false;fmt.Println("Backfill marker read failed:",err);return}
  // Preserve existing manual journals on matching dates; never overwrite them.
  cur,err:=workCol.Find(ctx,bson.M{"date":bson.M{"$gte":"2026-09-01","$lte":"2026-09-30"}})
  if err!=nil {workReady=false;return}
  existing:=[]WorkEntry{};err=cur.All(ctx,&existing);cur.Close(ctx)
  if err!=nil {workReady=false;return}
  occupied:=map[string]bool{}
  for _,x:=range existing {for _,person:=range x.People {occupied[person+"|"+x.Date]=true}}
  models:=make([]mongo.WriteModel,0,len(seed))
  for _,x:=range seed {if occupied[x.People[0]+"|"+x.Date] {continue};models=append(models,mongo.NewUpdateOneModel().SetFilter(bson.M{"_id":x.ID}).SetUpdate(bson.M{"$setOnInsert":x}).SetUpsert(true))}
  if len(models)>0 {if _,err=workCol.BulkWrite(ctx,models);err!=nil {workReady=false;fmt.Println("September import failed:",err);return}}
  if _,err=markers.UpdateOne(ctx,bson.M{"_id":septemberMigration},bson.M{"$setOnInsert":bson.M{"completedAt":time.Now(),"rows":len(seed)}},options.UpdateOne().SetUpsert(true));err!=nil {workReady=false;fmt.Println("Backfill checkpoint failed:",err)}
  return
 }
 if _,err:=os.Stat(septemberMarkerFile);err==nil {return} else if !os.IsNotExist(err) {workReady=false;return}
 seen:=map[string]bool{};occupied:=map[string]bool{};for _,x:=range workEntries {seen[x.ID]=true;for _,person:=range x.People {occupied[person+"|"+x.Date]=true}}
 next:=append([]WorkEntry{},workEntries...);for _,x:=range seed {if !seen[x.ID]&&!occupied[x.People[0]+"|"+x.Date] {next=append(next,x);occupied[x.People[0]+"|"+x.Date]=true}}
 b,err:=json.Marshal(next);if err==nil {err=os.WriteFile(workFile+".new",b,0600)};if err==nil {err=os.Rename(workFile+".new",workFile)};if err==nil {err=os.WriteFile(septemberMarkerFile,[]byte(septemberMigration),0600)}
 if err!=nil {workReady=false;fmt.Println("Backfill local import failed:",err);return};workEntries=next
}
