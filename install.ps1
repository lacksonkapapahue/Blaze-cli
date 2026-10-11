# Blaze CLI installer for Windows (PowerShell).
#   irm https://github.com/lacksonkapapahue/Blaze-cli/releases/latest/download/install.ps1 | iex
# If the repo is ever private: $env:GITHUB_TOKEN = "<token with repo read>" first.
$ErrorActionPreference = 'Stop'
$repo = 'lacksonkapapahue/Blaze-cli'
$arch = if ($env:PROCESSOR_ARCHITECTURE -eq 'ARM64') { 'arm64' } else { 'amd64' }
$asset = "blaze-windows-$arch.exe"
$dir = Join-Path $env:LOCALAPPDATA 'Blaze\bin'
New-Item -ItemType Directory -Force -Path $dir | Out-Null
$dest = Join-Path $dir 'blaze.exe'
Write-Host ""
Write-Host "  B L A Z E  installer" -ForegroundColor Blue
Write-Host "     windows/$arch -> $dir"
Write-Host ""
if ($env:GITHUB_TOKEN) {
  $h = @{ Authorization = "Bearer $env:GITHUB_TOKEN" }
  $rel = Invoke-RestMethod -Headers $h "https://api.github.com/repos/$repo/releases/latest"
  $a = $rel.assets | Where-Object { $_.name -eq $asset } | Select-Object -First 1
  $h['Accept'] = 'application/octet-stream'
  Invoke-WebRequest -Headers $h -Uri $a.url -OutFile $dest
} else {
  Invoke-WebRequest -Uri "https://github.com/$repo/releases/latest/download/$asset" -OutFile $dest
  try { $sums = (Invoke-WebRequest -UseBasicParsing -Uri "https://github.com/$repo/releases/latest/download/SHA256SUMS").Content } catch { $sums = $null }
  if ($sums) {
    if ($sums -is [byte[]]) { $sums = [Text.Encoding]::UTF8.GetString($sums) }
    $line = ($sums -split "`n") | Where-Object { ($_ -split '\s+')[1] -eq $asset } | Select-Object -First 1
    if ($line) {
      $want = ($line -split '\s+')[0].ToLower()
      $got = (Get-FileHash -Algorithm SHA256 $dest).Hash.ToLower()
      if ($want -ne $got) { Remove-Item $dest -Force; throw "Checksum mismatch for $asset; not installing." }
    }
  }
}
$userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
if (($userPath -split ';') -notcontains $dir) {
  [Environment]::SetEnvironmentVariable('Path', "$userPath;$dir", 'User')
  $env:Path = "$env:Path;$dir"
  Write-Host "  Added $dir to your PATH."
}
& $dest version
& $dest setup
