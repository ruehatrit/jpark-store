# JPARK CCTV Live integration

1. Deploy this Go project to Render as before. Do not expose the NVR to the public internet.
2. In Render Environment, add `CCTV_INGEST_TOKEN` with a unique random secret at least 32 characters long (use a password manager or generate securely). Redeploy.
3. On a Windows 10 PC on the same LAN as 10.10.33.3, run `cctv-agent.ps1` in PowerShell. Provide the HTTPS JPARK base URL and the same token, then log in to Hikvision locally. Use a dedicated read-only NVR account.
4. Log into JPARK normally, open the CCTV Monitoring menu. It updates every 30 seconds while the Windows PC/agent runs. If no update arrives for 90 seconds, the page shows disconnected/stale instead of false green status.

Notes: The NVR is accessed only from the LAN PC. The cloud app only accepts authenticated status push over HTTPS; no NVR password is sent to Render. Data is held in server memory (it resets on restart and does not synchronize across multiple Render instances). Keep one Render instance or implement shared storage later. Avoid sending NVR credentials in chat; rotate previously shared credentials. Test on a staging deployment before production.

## Main dashboard CCTV alerts
The main JPARK dashboard now checks `/api/cctv/status` on opening and every 30 seconds while it is visible. It shows a red alert listing the channel and IP for each camera explicitly reported offline. If telemetry is older than 90 seconds, the ingest token is missing, or the status service is unreachable, it shows an amber warning instead of falsely reporting 25 cameras offline. Alerts are visible on the main dashboard while the user is logged in; they are not Windows push notifications and do not operate when the page is closed.
