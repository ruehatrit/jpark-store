# JPARK CCTV Agent — run on a Windows PC with LAN access to the Hikvision NVR.
# Set JPARK_URL to your actual HTTPS domain and CCTV_INGEST_TOKEN to the matching Render environment secret.
$ErrorActionPreference = 'Stop'
$Nvr = 'http://10.10.33.3'
$JparkUrl = Read-Host 'JPARK HTTPS base URL (example https://your-site.com)'
$Token = Read-Host 'CCTV_INGEST_TOKEN (from Render environment)' -AsSecureString
$TokenText = [System.Net.NetworkCredential]::new('', $Token).Password
Add-Type -AssemblyName System.Windows.Forms
$form = New-Object System.Windows.Forms.Form
$form.Text = 'JPARK Hikvision Login'; $form.Width=380; $form.Height=185; $form.StartPosition='CenterScreen'
$label = New-Object System.Windows.Forms.Label; $label.Text='Hikvision password (hidden)'; $label.SetBounds(15,15,340,22); $form.Controls.Add($label)
$entry = New-Object System.Windows.Forms.TextBox; $entry.SetBounds(15,42,330,25); $entry.UseSystemPasswordChar=$true; $form.Controls.Add($entry)
$ok = New-Object System.Windows.Forms.Button; $ok.Text='Connect'; $ok.SetBounds(245,82,100,30); $ok.DialogResult=[System.Windows.Forms.DialogResult]::OK; $form.Controls.Add($ok); $form.AcceptButton=$ok
if ($form.ShowDialog() -ne [System.Windows.Forms.DialogResult]::OK) { throw 'Login canceled' }
$User='ma'; $Pass=$entry.Text; $entry.Clear();$form.Dispose()
Write-Host 'Agent running. Keep this window open. Ctrl+C to stop.'
while ($true) {
 try {
  $handler = New-Object System.Net.Http.HttpClientHandler
  $handler.Credentials = New-Object System.Net.NetworkCredential($User,$Pass)
  $client = New-Object System.Net.Http.HttpClient($handler)
  $client.Timeout = [TimeSpan]::FromSeconds(15)
  try {
   $response = $client.GetAsync("$Nvr/ISAPI/ContentMgmt/InputProxy/channels/status").GetAwaiter().GetResult()
   $response.EnsureSuccessStatusCode() | Out-Null
   $raw = $response.Content.ReadAsStringAsync().GetAwaiter().GetResult()
  } finally { $client.Dispose();$handler.Dispose() }
  [xml]$xml = ($raw -join "`n")
  $nodes = $xml.SelectNodes("//*[local-name()='InputProxyChannelStatus']")
  $items = @($nodes | ForEach-Object {
   $id = $_.SelectSingleNode("./*[local-name()='id']")
   $ip = $_.SelectSingleNode(".//*[local-name()='ipAddress']")
   $online = $_.SelectSingleNode("./*[local-name()='online']")
   if ($id -and $ip -and $online) { @{ channel=[int]$id.InnerText; ip=$ip.InnerText; online=($online.InnerText -eq 'true') } }
  })
  if ($items.Count -eq 0) { throw 'No channels returned by NVR' }
  $body = @{ cameras=$items; collectedAt=(Get-Date).ToUniversalTime().ToString('o') } | ConvertTo-Json -Depth 5
  Invoke-RestMethod -Uri ($JparkUrl.TrimEnd('/')+'/api/cctv/ingest') -Method Post -Headers @{Authorization="Bearer $TokenText"} -ContentType 'application/json' -Body ([System.Text.Encoding]::UTF8.GetBytes($body)) | Out-Null
  Write-Host "$(Get-Date -Format 'HH:mm:ss') sent $($items.Count) channels"
 } catch { Write-Warning "Update failed: $($_.Exception.Message)" }
 Start-Sleep -Seconds 30
}
