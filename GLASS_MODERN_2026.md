# JPARK Glass Modern 2026

- Unified glass-style presentation across index.html, daily.html, asset.html and cctv.html.
- CCTV Monitoring quick-access card on the home screen and link in Store sidebar.
- Home CCTV banner displays green when all reported cameras are online, red with camera IDs when offline, amber when feed is stale or unavailable.
- Existing 30-second dashboard polling and Go API preserved.
- No NVR password or ingest token is included in these changes.

## Deploy
Upload the contents of `jpark-store-main/` to the root of the GitHub repository, commit, then allow Render to deploy. Hard-refresh the browser after deployment.

## Notes
- Status is visible while the home dashboard is open; it is not an OS/browser push notification.
- Agent must remain running to keep status current.
- Test login, Stock, Daily Check, Issues, Asset, CCTV and mobile layout after deployment.
