$ErrorActionPreference = 'Stop'
Set-Location (Split-Path $PSScriptRoot -Parent)
$goCommand = Get-Command go -ErrorAction SilentlyContinue
$goExe = if ($goCommand) { $goCommand.Source } else { Join-Path (Get-Location) '.tools\go\bin\go.exe' }
if (-not (Test-Path -LiteralPath $goExe)) { throw 'Install Go 1.23+ or place its toolchain in .tools/go.' }
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
        $port = 8081 + $i
        $children += Start-Process -FilePath (Join-Path (Get-Location) 'bin\backend.exe') -ArgumentList @('-name', $names[$i], '-listen', "127.0.0.1:$port") -WindowStyle Hidden -PassThru
    }
    Write-Host 'Relay: http://127.0.0.1:8080 | Metrics: http://127.0.0.1:9090/metrics'
    Write-Host 'Use another terminal for requests. Ctrl+C stops Relay and its demo backends.'
    & .\bin\relay.exe -config config/relay.json
} finally {
    foreach ($child in $children) { if (-not $child.HasExited) { Stop-Process -Id $child.Id -ErrorAction SilentlyContinue } }
}
