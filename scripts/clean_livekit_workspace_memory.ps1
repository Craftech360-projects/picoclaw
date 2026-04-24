param(
  [Parameter(Mandatory=$true)]
  [string]$Workspace
)

$memory = Join-Path $Workspace "memory\MEMORY.md"
if (!(Test-Path $memory)) {
  Write-Error "MEMORY.md not found: $memory"
  exit 1
}

$lines = Get-Content $memory
$clean = New-Object System.Collections.Generic.List[string]
$seen = @{}
$skipRawBlock = $false

foreach ($line in $lines) {
  $trim = $line.Trim()
  $unbulleted = $trim -replace '^-+\s*', ''

  if ($unbulleted -match '^(Transcript excerpt:|Session summary:)$') {
    $skipRawBlock = $true
    continue
  }

  if ($skipRawBlock) {
    if ($trim -eq '' -or $trim -match '^(User|Assistant|System|Tool):' -or $trim -notmatch '^-+\s*') {
      continue
    }
    $skipRawBlock = $false
  }

  if ($trim -match '\[System Event\]' -or
      $trim -match 'successfully connected to the room' -or
      $trim -match 'You must end this conversation now' -or
      $trim -match '^(User|Assistant|System|Tool):') {
    continue
  }

  if ($trim -match '^-+\s*(Overall memory:|Transcript excerpt:|Session summary:)$') {
    continue
  }
  if ($trim -match '^-+\s*(Last session highlights:|Good follow-up topics:)') {
    continue
  }

  $dedupeKey = $trim.ToLowerInvariant()
  if ($trim -match '^-+\s*' -and $seen.ContainsKey($dedupeKey)) {
    continue
  }
  if ($trim -match '^-+\s*') {
    $seen[$dedupeKey] = $true
  }

  $clean.Add($line)
}

$backup = "$memory.bak-$(Get-Date -Format yyyyMMdd-HHmmss)"
Copy-Item $memory $backup
Set-Content -Path $memory -Value $clean -Encoding UTF8
Write-Output "Cleaned $memory"
Write-Output "Backup: $backup"
