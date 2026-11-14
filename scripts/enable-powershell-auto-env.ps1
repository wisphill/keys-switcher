param(
  [Parameter(Mandatory = $true)]
  [string]$KsPath
)

$ErrorActionPreference = 'Stop'
$profilePath = $PROFILE.CurrentUserAllHosts
$profileDirectory = Split-Path -Parent $profilePath
New-Item -ItemType Directory -Path $profileDirectory -Force | Out-Null

$marker = '# ks: auto-load Anthropic environment after aikeys switch'
$existing = if (Test-Path $profilePath) { Get-Content -Raw -Path $profilePath } else { '' }

if ($existing -notmatch [regex]::Escape($marker)) {
  $init = & $KsPath shell-init --binary $KsPath powershell
  if ($LASTEXITCODE -ne 0) {
    throw "Could not generate ks PowerShell integration (exit code $LASTEXITCODE)."
  }
  Add-Content -Path $profilePath -Value "`n$marker`n$init"
  Write-Output "Added ks integration to $profilePath. Open a new PowerShell window."
} else {
  Write-Output "ks integration is already present in $profilePath."
}
