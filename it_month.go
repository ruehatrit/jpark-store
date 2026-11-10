package main

import (
	"context"
	"encoding/json"
	"go.mongodb.org/mongo-driver/v2/bson"
	"go.mongodb.org/mongo-driver/v2/mongo"
	"go.mongodb.org/mongo-driver/v2/mongo/options"
	"net/http"
	"time"
)

type ITDay struct {
	Date       string `json:"date"`
	Code       string `json:"code"`
	Shift      string `json:"shift"`
	In         string `json:"in"`
	Out        string `json:"out"`
	Status     string `json:"status"`
	Late       bool   `json:"late"`
	Source     string `json:"source"`
	Incomplete bool   `json:"incomplete"`
}

func evaluateITDay(date, code, shift string, events []FingerEvent, now, complete, oldest time.Time) ITDay {
	row := ITDay{Date: date, Code: code, Shift: shift, Status: "unassigned"}
	if shift == "off" || shift == "leave" {
		row.Status = shift
		return row
	}
	if shift != "day" && shift != "night" {
		return row
	}
	day, _ := time.ParseInLocation("2006-01-02", date, attendanceZone)
	start := day.Add(7 * time.Hour)
	if shift == "night" {
		start = day.Add(19 * time.Hour)
	}
	end := start.Add(12 * time.Hour)
	inStart, inEnd := start.Add(-3*time.Hour).Format("2006-01-02 15:04:05"), start.Add(6*time.Hour).Format("2006-01-02 15:04:05")
	outEnd := end.Add(6 * time.Hour).Format("2006-01-02 15:04:05")
	for _, x := range events {
		if x.Code != code {
			continue
		}
		if x.Time >= inStart && x.Time < inEnd && (row.In == "" || x.Time < row.In) {
			row.In = x.Time
		}
		if x.Time >= inEnd && x.Time < outEnd && (row.Out == "" || x.Time > row.Out) {
			row.Out = x.Time
		}
	}
	covered := !complete.Before(end) && !oldest.IsZero() && !oldest.After(start.Add(-3*time.Hour))
	if row.In == "" && row.Out == "" {
		if now.Before(start) {
			row.Status = "future"
		} else if now.Before(end) {
			row.Status = "waiting"
		} else if covered {
			row.Status = "absent"
		} else {
			row.Status = "data_pending"
		}
		return row
	}
	if row.In == "" {
		row.Status = "missing_in"
		row.Incomplete = true
		return row
	}
	row.Late = row.In >= start.Format("2006-01-02 15:04:05")
	row.Status = "present"
	if row.Late {
		row.Status = "late"
	}
	if row.Out == "" && !now.Before(end) && covered {
		row.Incomplete = true
	}
	return row
}
func itMonthHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(405)
		return
	}
	if attendanceEvents == nil {
		w.WriteHeader(503)
		return
	}
	month := r.URL.Query().Get("month")
	if month == "" {
		month = time.Now().In(attendanceZone).Format("2006-01")
	}
	start, err := time.ParseInLocation("2006-01", month, attendanceZone)
	if err != nil || start.Format("2006-01") != month {
		w.WriteHeader(400)
		return
	}
	end := start.AddDate(0, 1, 0)
	ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
	defer cancel()
	profiles, err := employeeProfiles(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	device, err := deviceRecord(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	codes := []string{}
	for _, p := range profiles {
		codes = append(codes, p.Code)
	}
	cur, err := attendanceShifts.Find(ctx, bson.M{"code": bson.M{"$in": codes}, "date": bson.M{"$gte": start.Format("2006-01-02"), "$lt": end.Format("2006-01-02")}})
	if err != nil {
		w.WriteHeader(503)
		return
	}
	plans := []EmployeeShift{}
	err = cur.All(ctx, &plans)
	cur.Close(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	byKey := map[string]EmployeeShift{}
	for _, p := range plans {
		byKey[p.Date+":"+p.Code] = p
	}
	cur, err = attendanceEvents.Find(ctx, bson.M{"code": bson.M{"$in": codes}, "time": bson.M{"$gte": start.Format("2006-01-02") + " 00:00:00", "$lt": end.Add(13 * time.Hour).Format("2006-01-02 15:04:05")}}, options.Find().SetSort(bson.D{{Key: "time", Value: 1}}))
	if err != nil {
		w.WriteHeader(503)
		return
	}
	events := []FingerEvent{}
	err = cur.All(ctx, &events)
	cur.Close(ctx)
	if err != nil {
		w.WriteHeader(503)
		return
	}
	var first FingerEvent
	oldest := time.Time{}
	err = attendanceEvents.FindOne(ctx, bson.M{"code": bson.M{"$in": codes}}, options.FindOne().SetSort(bson.D{{Key: "time", Value: 1}})).Decode(&first)
	if err == nil {
		oldest, _ = time.ParseInLocation("2006-01-02 15:04:05", first.Time, attendanceZone)
	} else if err != mongo.ErrNoDocuments {
		w.WriteHeader(503)
		return
	}
	now := time.Now().In(attendanceZone)
	rows := []ITDay{}
	for d := start; d.Before(end); d = d.AddDate(0, 0, 1) {
		for _, p := range profiles {
			if !p.Active {
				continue
			}
			date := d.Format("2006-01-02")
			plan, ok := byKey[date+":"+p.Code]
			shift := "unassigned"
			if ok {
				shift = plan.Shift
			}
			row := evaluateITDay(date, p.Code, shift, events, now, device.LastCompleteRead, oldest)
			row.Source = plan.Source
			rows = append(rows, row)
		}
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]interface{}{"month": month, "employees": profiles, "rows": rows, "device": publicFingerDevice(device), "serverTime": now, "today": now.Format("2006-01-02")})
}
