# JPARK Store PRO V10 - Daily Check + Zones + Print Report

ระบบ Store เดิม + MongoDB Atlas และโมดูล "ตรวจสอบอุปกรณ์ลานจอดประจำวัน"

## ลิงก์
- `/` ระบบ Store / Stock เดิม
- `/daily.html` ระบบตรวจสอบอุปกรณ์ลานจอดประจำวัน

## แยกข้อมูล ไม่กระทบ Stock
- Store: MongoDB collection `app_state`
- Daily Check: MongoDB collection `daily_check_state`
- การเพิ่มโซน/รายการตรวจ/ผลตรวจ จะไม่เปลี่ยนยอด Stock หรือทะเบียนเครื่องมือเดิม

## V10 เพิ่มใหม่
- ปุ่ม `+ เพิ่มโซน` ให้กำหนดโซน/จุดติดตั้งเอง
- โซนบันทึกลง MongoDB และเรียกใช้ตอนเพิ่มรายการตรวจ
- ป้องกันลบโซนที่ยังถูกใช้อยู่ใน Checklist
- ประวัติแต่ละรอบมีปุ่ม `พิมพ์` รายงาน A4 รายวัน
- ฟอร์มพิมพ์มีโลโก้, สรุป ปกติ/ผิดปกติ/ไม่ได้ตรวจ, ตาราง, หมายเหตุ, ผู้ตรวจ, ผู้รับทราบ
- หน้า Report มี `พิมพ์รายงานรายเดือน` A4 แนวนอน
- รองรับ Print ออกเครื่องพิมพ์ หรือ Save as PDF ผ่าน Browser
- Report รายเดือน + กราฟ + Top Issues + Export CSV

## Build Command บน Render
`go mod tidy && go build -o app .`
