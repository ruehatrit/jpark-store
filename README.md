JPARK Store PRO V8 - MongoDB Persistent Edition

- ใช้ MongoDB Atlas ผ่าน Environment Variable: MONGODB_URI
- เก็บ Items / Transactions / Users แบบถาวรใน database: jpark_store, collection: app_state
- ถ้า MongoDB ใช้งานได้ ระบบจะ log: JPARK Store persistence: MongoDB Atlas
- คงหน้าตา/ฟังก์ชัน V7: Login, Search/Filter, Report, กราฟ, Export CSV

Deploy:
1) อัปโหลดไฟล์ทั้งหมดทับ repo jpark-store ใน GitHub
2) Render จะ auto deploy
3) MONGODB_URI ต้องมีอยู่ใน Render Environment
4) ดู Logs ต้องเห็น JPARK Store persistence: MongoDB Atlas
