param(
  [Parameter(Mandatory = $true)]
  [string]$KsPath,
  [string]$ProfilePath
)

$ErrorActionPreference = 'Stop'
if (-not $ProfilePath) {
  $ProfilePath = $PROFILE.CurrentUserAllHosts
}
$profileDirectory = Split-Path -Parent $profilePath
New-Item -ItemType Directory -Path $profileDirectory -Force | Out-Null

$marker = '# ks: auto-load Anthropic environment after aikeys switch'
$existing = ''
if (Test-Path -LiteralPath $profilePath) {
  $reader = [IO.StreamReader]::new($profilePath, [Text.Encoding]::Default, $true)
  try {
    $existing = $reader.ReadToEnd()
    $profileEncoding = $reader.CurrentEncoding
  } finally {
    $reader.Dispose()
  }
}

$init = & $KsPath shell-init --binary $KsPath powershell
if ($LASTEXITCODE -ne 0 -or -not $init) {
  throw "Could not generate ks PowerShell integration (exit code $LASTEXITCODE)."
}

$newLine = [Environment]::NewLine
$initScript = $init -join $newLine

if ($existing -notmatch [regex]::Escape($marker)) {
  Add-Content -LiteralPath $profilePath -Value "$newLine$marker$newLine$initScript"
  Write-Output "Added ks integration to $profilePath. Open a new PowerShell window."
} else {
  # Older installers interpolated the array of output lines into a single line.
  $legacyPattern = '(?m)^' + [regex]::Escape($marker) + '\r?\n' +
    [regex]::Escape(($init -join ' ')) + '(?=\r?$)'
  $updated = [regex]::Replace($existing, $legacyPattern, { $marker + $newLine + $initScript })
  if ($updated -cne $existing) {
    [IO.File]::WriteAllText($profilePath, $updated, $profileEncoding)
    Write-Output "Repaired ks integration in $profilePath. Open a new PowerShell window."
  } else {
    Write-Output "ks integration is already present in $profilePath."
  }
}
