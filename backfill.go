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
const octoberMigration = "attendance_oct2026_routine_v1"
const octoberMarkerFile = "/tmp/jpark-backfill-oct2026.done"

// Initial import only. The migration marker prevents deleted/edited tasks from
// being recreated when Render restarts. Stable IDs make partial retries safe.
func initOctoberBackfill() {
 if !workReady {return}
 var seed []WorkEntry
 if err:=json.Unmarshal([]byte(octoberBackfillJSON),&seed);err!=nil {workReady=false;fmt.Println("Backfill data invalid:",err);return}
 for i:=range seed {if !validWork(&seed[i]) {workReady=false;fmt.Println("Backfill row invalid:",seed[i].ID);return}}
 if workCol!=nil {
  markers:=mongoClient.Database("jpark_store").Collection("worklog_migrations")
  ctx,cancel:=context.WithTimeout(context.Background(),90*time.Second);defer cancel()
  err:=markers.FindOne(ctx,bson.M{"_id":octoberMigration}).Err()
  if err==nil {return}
  if err!=mongo.ErrNoDocuments {workReady=false;fmt.Println("Backfill marker read failed:",err);return}
  models:=make([]mongo.WriteModel,0,len(seed))
  for _,x:=range seed {models=append(models,mongo.NewUpdateOneModel().SetFilter(bson.M{"_id":x.ID}).SetUpdate(bson.M{"$setOnInsert":x}).SetUpsert(true))}
  if _,err=workCol.BulkWrite(ctx,models);err!=nil {workReady=false;fmt.Println("Backfill import failed:",err);return}
  if _,err=markers.UpdateOne(ctx,bson.M{"_id":octoberMigration},bson.M{"$setOnInsert":bson.M{"completedAt":time.Now(),"rows":len(seed)}},options.UpdateOne().SetUpsert(true));err!=nil {workReady=false;fmt.Println("Backfill checkpoint failed:",err)}
  return
 }
 if _,err:=os.Stat(octoberMarkerFile);err==nil {return} else if !os.IsNotExist(err) {workReady=false;return}
 seen:=map[string]bool{};for _,x:=range workEntries {seen[x.ID]=true}
 next:=append([]WorkEntry{},workEntries...);for _,x:=range seed {if !seen[x.ID] {next=append(next,x)}}
 b,err:=json.Marshal(next);if err==nil {err=os.WriteFile(workFile+".new",b,0600)};if err==nil {err=os.Rename(workFile+".new",workFile)};if err==nil {err=os.WriteFile(octoberMarkerFile,[]byte(octoberMigration),0600)}
 if err!=nil {workReady=false;fmt.Println("Backfill local import failed:",err);return};workEntries=next
}
