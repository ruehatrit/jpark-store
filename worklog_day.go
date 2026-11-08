package main

import (
 "context"
 "fmt"
 "net/http"
 "reflect"
 "go.mongodb.org/mongo-driver/v2/bson"
 "go.mongodb.org/mongo-driver/v2/mongo"
)

// Called under workMu: one manually entered journal per person per date.
// Legacy routine rows remain editable but new creates cannot duplicate them.
func checkWorkDay(ctx context.Context, method string, x WorkEntry) error {
 if method==http.MethodDelete {return nil}
 var old WorkEntry; found:=false
 if workCol!=nil {
  err:=workCol.FindOne(ctx,bson.M{"_id":x.ID}).Decode(&old)
  if err==nil {found=true} else if err!=mongo.ErrNoDocuments {return err}
 } else {for _,e:=range workEntries {if e.ID==x.ID {old=e;found=true;break}}}
 if found&&method==http.MethodPost {return nil} // Idempotent retry.
 if found&&method==http.MethodPut&&old.Date==x.Date&&reflect.DeepEqual(old.People,x.People) {return nil}
 if workCol!=nil {
  err:=workCol.FindOne(ctx,bson.M{"date":x.Date,"people":bson.M{"$in":x.People},"id":bson.M{"$ne":x.ID}}).Err()
  if err==mongo.ErrNoDocuments {return nil};if err!=nil {return err}
  return fmt.Errorf("วันนี้มีบันทึกของคนนี้แล้ว ให้เปิดบันทึกวันที่ %s แล้วกดแก้ไข รวมงานที่ทำไว้ในบันทึกเดียว",x.Date)
 }
 for _,e:=range workEntries {if e.ID==x.ID||e.Date!=x.Date {continue};for _,a:=range e.People {for _,b:=range x.People {if a==b {return fmt.Errorf("วันนี้มีบันทึกของ %s แล้ว ให้แก้ไขบันทึกเดิมและรวมงานไว้ด้วยกัน",b)}}}}
 return nil
}
