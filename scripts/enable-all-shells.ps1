param(
  [Parameter(Mandatory = $true)]
  [string]$KsPath,
  [Parameter(Mandatory = $true)]
  [string]$GitBashPath,
  [switch]$Elevated,
  [string]$LogPath
)

$ErrorActionPreference = 'Stop'

function Test-Administrator {
  $identity = [Security.Principal.WindowsIdentity]::GetCurrent()
  $principal = [Security.Principal.WindowsPrincipal]::new($identity)
  return $principal.IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
}

function Quote-Argument([string]$Value) {
  return '"' + $Value.Replace('"', '\"') + '"'
}

if (-not (Test-Administrator)) {
  # Reuse the current host executable. Sysnative is a file-system redirector,
  # not an executable path that Start-Process resolves reliably in every host.
  $nativePowerShell = Join-Path $PSHOME 'powershell.exe'
  $logPath = Join-Path $env:TEMP "keys-switcher-install-shell-$PID.log"
  $arguments = '-NoProfile -ExecutionPolicy Bypass -File ' +
    (Quote-Argument $PSCommandPath) + ' -KsPath ' +
    (Quote-Argument $KsPath) + ' -GitBashPath ' +
    (Quote-Argument $GitBashPath) + ' -Elevated -LogPath ' +
    (Quote-Argument $logPath)
  try {
    $process = Start-Process -FilePath $nativePowerShell -ArgumentList $arguments -Verb RunAs -Wait -PassThru
    if ($process.ExitCode -ne 0) {
      $diagnostic = if (Test-Path -LiteralPath $logPath) {
        Get-Content -LiteralPath $logPath -Raw
      }
      throw "The elevated shell installer failed with exit code $($process.ExitCode).$([Environment]::NewLine)$diagnostic"
    }
  } finally {
    Remove-Item -LiteralPath $logPath -Force -ErrorAction SilentlyContinue
  }
  exit 0
}

if ($LogPath) {
  Start-Transcript -LiteralPath $LogPath -Force | Out-Null
}

if (-not (Test-Path -LiteralPath $KsPath -PathType Leaf)) {
  throw "ks executable was not found at $KsPath."
}
if (-not (Test-Path -LiteralPath $GitBashPath -PathType Leaf)) {
  throw "Git Bash was not found at $GitBashPath."
}

& $KsPath install
if ($LASTEXITCODE -ne 0) {
  throw "Could not install ks machine-wide (exit code $LASTEXITCODE)."
}

$programFilesDirectory = if ($env:ProgramW6432) { $env:ProgramW6432 } else { $env:ProgramFiles }
$installedKsPath = Join-Path $programFilesDirectory 'keys-switcher\ks.exe'
if (-not (Test-Path -LiteralPath $installedKsPath -PathType Leaf)) {
  throw "The machine-wide ks executable was not found at $installedKsPath."
}

$scriptDirectory = Split-Path -Parent $PSCommandPath
$powerShellInstaller = Join-Path $scriptDirectory 'enable-powershell-auto-env.ps1'
$bashInstaller = Join-Path $scriptDirectory 'enable-bash-auto-env.sh'
$userProfiles = Get-CimInstance Win32_UserProfile |
  Where-Object { -not $_.Special -and $_.LocalPath -and (Test-Path -LiteralPath $_.LocalPath -PathType Container) } |
  ForEach-Object { $_.LocalPath } |
  Sort-Object -Unique

foreach ($userProfile in $userProfiles) {
  $powerShellProfile = Join-Path $userProfile 'Documents\WindowsPowerShell\profile.ps1'
  $bashProfile = Join-Path $userProfile '.bashrc'

  & $powerShellInstaller -KsPath $installedKsPath -ProfilePath $powerShellProfile
  if ($LASTEXITCODE -ne 0) {
    throw "Could not configure PowerShell for $userProfile (exit code $LASTEXITCODE)."
  }

  & $GitBashPath $bashInstaller $installedKsPath $bashProfile
  if ($LASTEXITCODE -ne 0) {
    throw "Could not configure Git Bash for $userProfile (exit code $LASTEXITCODE)."
  }
}

Write-Output "Configured PowerShell and Git Bash for $($userProfiles.Count) local user profile(s)."

if ($LogPath) {
  Stop-Transcript | Out-Null
}
