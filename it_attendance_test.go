package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"
)

func TestNextMonthRosterAndConflictingShifts(t *testing.T) {
	workbook := func(conflict bool) []byte {
		var buf bytes.Buffer
		z := zip.NewWriter(&buf)
		parts := map[string]string{
			"xl/workbook.xml":            `<workbook xmlns:r="http://schemas.openxmlformats.org/officeDocument/2006/relationships"><sheets><sheet name="IT" r:id="rId1"/></sheets></workbook>`,
			"xl/_rels/workbook.xml.rels": `<Relationships><Relationship Id="rId1" Target="worksheets/sheet1.xml"/></Relationships>`,
		}
		x := `<worksheet><sheetData><row>`
		for d := 1; d <= 31; d++ {
			x += fmt.Sprintf(`<c r="%s1" t="inlineStr"><is><t>2026-12-%02d</t></is></c>`, columnName(d+8), d)
		}
		x += `</row><row><c r="B2" t="inlineStr"><is><t>นายฤหัสฤทธิ์ หงษ์โต</t></is></c>`
		for d := 1; d <= 31; d++ {
			x += fmt.Sprintf(`<c r="%s2" t="inlineStr"><is><t>D</t></is></c>`, columnName(d+8))
		}
		x += `</row>`
		if conflict {
			x += `<row><c r="B3" t="inlineStr"><is><t>นายฤหัสฤทธิ์ หงษ์โต</t></is></c><c r="I3" t="inlineStr"><is><t>N</t></is></c></row>`
		}
		parts["xl/worksheets/sheet1.xml"] = x + `</sheetData></worksheet>`
		for name, s := range parts {
			f, _ := z.Create(name)
			_, _ = f.Write([]byte(s))
		}
		_ = z.Close()
		return buf.Bytes()
	}
	p, err := readITRoster(workbook(false), "December.xlsx", initialEmployeeProfiles())
	if err != nil || p.Month != "2026-12" || len(p.Rows) != 31 || p.Counts["day"] != 31 || len(p.Warnings) != 3 || p.Hash == "" {
		t.Fatalf("future month import: %+v %v", p, err)
	}
	if _, err = readITRoster(workbook(true), "December.xlsx", initialEmployeeProfiles()); err == nil {
		t.Fatal("conflicting D/N accepted")
	}
}
func columnName(n int) string {
	s := ""
	for n > 0 {
		n--
		s = string(rune('A'+n%26)) + s
		n /= 26
	}
	return s
}

func TestNovemberRosterMatchesActualWorkbook(t *testing.T) {
	file, err := os.ReadFile("../../upload/ตารางกะ IT พฤศจิกายน .xlsx")
	if err != nil {
		t.Skip("local uploaded workbook not present")
	}
	preview, err := readITRoster(file, "ตารางกะ IT พฤศจิกายน .xlsx", initialEmployeeProfiles())
	if err != nil {
		t.Fatal(err)
	}
	if preview.Month != "2026-11" || len(preview.Rows) != 120 || preview.Counts["unassigned"] != 0 {
		t.Fatal("incomplete roster", preview.Month, preview.Counts)
	}
	var seed []EmployeeShift
	_ = json.Unmarshal([]byte(novemberITRosterJSON), &seed)
	got := map[string]string{}
	for _, r := range preview.Rows {
		got[r.ID] = r.Shift
	}
	for _, r := range seed {
		if got[r.ID] != r.Shift {
			t.Fatal("date/shift mismatch", r.ID, r.Shift, got[r.ID])
		}
	}
	want := map[string]map[string]int{"000526": {"night": 25, "off": 5}, "000549": {"day": 16, "night": 10, "off": 4}, "000562": {"day": 17, "night": 9, "off": 4}, "000571": {"day": 25, "off": 5}}
	for code, counts := range want {
		for shift, n := range counts {
			count := 0
			for _, r := range preview.Rows {
				if r.Code == code && r.Shift == shift {
					count++
				}
			}
			if count != n {
				t.Fatal(code, shift, count, n)
			}
		}
	}
}
func TestITMonthlyEntryExitAndAbsence(t *testing.T) {
	parse := func(s string) time.Time {
		v, _ := time.ParseInLocation("2006-01-02 15:04:05", s, attendanceZone)
		return v
	}
	oldest := parse("2026-09-01 00:00:00")
	now := parse("2026-11-10 13:00:00")
	complete := now
	events := []FingerEvent{{Code: "000526", Time: "2026-11-08 18:50:00"}, {Code: "000526", Time: "2026-11-09 07:02:00"}}
	row := evaluateITDay("2026-11-08", "000526", "night", events, now, complete, oldest)
	if row.In != "2026-11-08 18:50:00" || row.Out != "2026-11-09 07:02:00" || row.Late || row.Status != "present" {
		t.Fatal("night pairing failed", row)
	}
	if r := evaluateITDay("2026-11-08", "000549", "day", nil, now, complete, oldest); r.Status != "absent" {
		t.Fatal("closed covered shift not absent", r)
	}
	if r := evaluateITDay("2026-11-11", "000549", "day", nil, now, complete, oldest); r.Status != "future" {
		t.Fatal("future marked absent", r)
	}
	if r := evaluateITDay("2026-11-10", "000549", "day", nil, now, complete, oldest); r.Status != "waiting" {
		t.Fatal("open shift marked absent", r)
	}
	if r := evaluateITDay("2026-11-08", "000549", "day", nil, now, time.Time{}, oldest); r.Status != "data_pending" {
		t.Fatal("offline data marked absent", r)
	}
	if r := evaluateITDay("2026-11-08", "000549", "off", nil, now, complete, oldest); r.Status != "off" {
		t.Fatal("OFF marked absent")
	}
	if r := evaluateITDay("2026-11-08", "000549", "unassigned", nil, now, complete, oldest); r.Status != "unassigned" {
		t.Fatal("missing roster marked absent")
	}
	late := []FingerEvent{{Code: "000549", Time: "2026-11-08 07:01:00"}, {Code: "000549", Time: "2026-11-08 19:01:00"}}
	r := evaluateITDay("2026-11-08", "000549", "day", late, now, complete, oldest)
	if !r.Late || r.Out == "" || r.Incomplete {
		t.Fatal("late/full day incorrect", r)
	}
	r = evaluateITDay("2026-11-08", "000549", "day", late[:1], now, complete, oldest)
	if !r.Incomplete || r.Status != "late" {
		t.Fatal("missing-out not detected", r)
	}
}
