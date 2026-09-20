# Build and verify the real executables, then stop only the processes this script owns.
$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)
$goCommand = Get-Command go -ErrorAction SilentlyContinue
$goExe = if ($goCommand) { $goCommand.Source } else { Join-Path (Get-Location) '.tools\go\bin\go.exe' }
$env:GOCACHE = Join-Path (Get-Location) '.cache\go-build'
New-Item -ItemType Directory -Path bin -Force | Out-Null
& $goExe build -o bin/relay.exe ./cmd/relay
if ($LASTEXITCODE -ne 0) { throw 'Relay build failed' }
& $goExe build -o bin/backend.exe ./cmd/backend
if ($LASTEXITCODE -ne 0) { throw 'Backend build failed' }
$children = @()
try {
    $names = @('alpha', 'bravo', 'charlie')
    for ($i = 0; $i -lt 3; $i++) {
        $children += Start-Process -FilePath "$PWD\bin\backend.exe" -ArgumentList @('-name', $names[$i], '-demo-controls', '-listen', "127.0.0.1:$(8081 + $i)") -WindowStyle Hidden -PassThru
    }
    $children += Start-Process -FilePath "$PWD\bin\relay.exe" -ArgumentList @('-config', 'config/relay.json') -WindowStyle Hidden -PassThru -RedirectStandardOutput "$PWD\bin\smoke.log" -RedirectStandardError "$PWD\bin\smoke-error.log"
    $ready = $false
    for ($i = 0; $i -lt 50; $i++) {
        if ($children.Where({ $_.HasExited }).Count -gt 0) { throw 'A demo process exited; check port availability and bin/smoke-error.log.' }
        try { $null = Invoke-RestMethod http://127.0.0.1:9090/readyz -TimeoutSec 1; $ready = $true; break } catch { Start-Sleep -Milliseconds 100 }
    }
    if (-not $ready) { throw 'Proxy did not become ready' }
    $before = @(1..6 | ForEach-Object { (Invoke-RestMethod http://127.0.0.1:8080/orders).backend })
    if (($before | Sort-Object -Unique).Count -ne 3) { throw "Expected all backends: $before" }
    $null = Invoke-RestMethod -Method Post http://127.0.0.1:8082/demo/fail
    Start-Sleep -Seconds 3
    $during = @(1..6 | ForEach-Object { (Invoke-RestMethod http://127.0.0.1:8080/orders).backend })
    if ($during -contains 'bravo') { throw 'Unhealthy backend still receiving traffic' }
    if (@($during | Where-Object { $_ -eq 'alpha' }).Count -ne 3 -or @($during | Where-Object { $_ -eq 'charlie' }).Count -ne 3) { throw "Unbalanced healthy pool: $during" }
    $null = Invoke-RestMethod -Method Post http://127.0.0.1:8082/demo/recover
    Start-Sleep -Seconds 2
    $after = @(1..6 | ForEach-Object { (Invoke-RestMethod http://127.0.0.1:8080/orders).backend })
    if ($after -notcontains 'bravo') { throw 'Recovered backend did not rejoin' }
    Write-Output "PASS distribution: $($before -join ', ')"
    Write-Output "PASS failure exclusion: $($during -join ', ')"
    Write-Output "PASS recovery: $($after -join ', ')"
} finally {
    foreach ($child in $children) { if (-not $child.HasExited) { Stop-Process -Id $child.Id -ErrorAction SilentlyContinue } }
}
