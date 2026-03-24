<#
.SYNOPSIS
  Start the Duplicate Tournament Manager Go server on Windows.
.USAGE
  .\start_go_server.ps1 [PORT] [-Watch] [-Fg] [-ValidateSpan] [-NoRackCollapse] [-SimTimeoutMs N]
#>
param(
    [int]$Port = 8090,
    [switch]$Watch,
    [switch]$Fg,
    [switch]$ValidateSpan,
    [switch]$NoRackCollapse,
    [int]$SimTimeoutMs = 0
)

$ErrorActionPreference = 'Stop'

$ScriptDir = Split-Path -Parent $MyInvocation.MyCommand.Path
$Root = $ScriptDir

# Locate backend dir
$BackendDir = Join-Path $Root "duplicate-tournament-manager" "backend"
if (-not (Test-Path $BackendDir)) {
    if (Test-Path (Join-Path $ScriptDir "backend")) {
        $Root = Split-Path -Parent $ScriptDir
        $BackendDir = Join-Path $Root "duplicate-tournament-manager" "backend"
    } else {
        Write-Error "Could not find duplicate-tournament-manager\backend next to this script."
        exit 1
    }
}

# Free the port if occupied
try {
    $conns = Get-NetTCPConnection -LocalPort $Port -ErrorAction SilentlyContinue
    if ($conns) {
        Write-Host "Freeing port $Port ..."
        foreach ($c in $conns) {
            try { Stop-Process -Id $c.OwningProcess -Force -ErrorAction SilentlyContinue } catch {}
        }
        Start-Sleep -Milliseconds 400
    }
} catch {}

# Check Go version
$goVer = & go version 2>$null
if (-not $goVer) {
    Write-Error "Go not found. Install Go 1.24+ from https://go.dev/dl/"
    exit 1
}
Write-Host "Using $goVer"

# Keep Go caches inside repo
$env:GOCACHE = Join-Path $Root ".gocache"
$env:GOMODCACHE = Join-Path $Root ".gomodcache"
New-Item -ItemType Directory -Force -Path $env:GOCACHE, $env:GOMODCACHE | Out-Null

# Build macondo-wrapper
$WrapperDir = Join-Path $BackendDir "bin"
New-Item -ItemType Directory -Force -Path $WrapperDir | Out-Null
$Wrapper = Join-Path $WrapperDir "macondo-wrapper.exe"
Write-Host "Ensuring macondo-wrapper is built..."
Push-Location $BackendDir
try {
    & go build -o $Wrapper ./cmd/macondo-wrapper
    if ($LASTEXITCODE -ne 0) { throw "Failed to build macondo-wrapper" }
} finally { Pop-Location }

# Seed lexica if needed
$lexicaDir = Join-Path $Root "lexica"
$rootKwg = Join-Path $Root "FILE2017.kwg"
$lexKwg = Join-Path $lexicaDir "FILE2017.kwg"
if ((Test-Path $rootKwg) -and -not (Test-Path $lexKwg)) {
    Write-Host "Seeding lexica\FILE2017.kwg from repo root..."
    New-Item -ItemType Directory -Force -Path $lexicaDir | Out-Null
    Copy-Item $rootKwg $lexKwg
}

# Ensure letterdistributions is accessible (junction if needed)
$ldTarget = Join-Path $BackendDir "letterdistributions"
$ldLink = Join-Path $Root "letterdistributions"
if ((Test-Path $ldTarget) -and -not (Test-Path $ldLink)) {
    Write-Host "Creating junction for letterdistributions..."
    try {
        New-Item -ItemType Junction -Path $ldLink -Target $ldTarget -ErrorAction Stop | Out-Null
    } catch {
        Write-Host "Junction failed, copying instead..."
        Copy-Item -Recurse $ldTarget $ldLink
    }
}

# Set environment
$env:PORT = $Port
$env:ENGINE = "macondo"
$env:MACONDO_BIN = $Wrapper
$env:MACONDO_DATA_PATH = Join-Path $Root "macondo" "data"
$env:KLV2_DIR = $lexicaDir
if (-not $env:MACONDO_SIM_TIMEOUT_MS) { $env:MACONDO_SIM_TIMEOUT_MS = "60000" }
if ($SimTimeoutMs -gt 0) { $env:MACONDO_SIM_TIMEOUT_MS = "$SimTimeoutMs" }
$env:VALIDATE_SPAN = if ($ValidateSpan) { "1" } else { "0" }
$env:RACK_COLLAPSE = if ($NoRackCollapse) { "0" } else { "1" }
if (-not $env:DEBUG_MATCH) { $env:DEBUG_MATCH = "0" }

# Build Wolges wrapper if cargo available
$wolgesDir = Join-Path $Root "wolges"
if (Test-Path $wolgesDir) {
    $cargo = Get-Command cargo -ErrorAction SilentlyContinue
    if ($cargo) {
        Write-Host "Ensuring wolges-wrapper is built..."
        Push-Location $wolgesDir
        try {
            & cargo build --release --bin wolges-wrapper 2>$null
            $relBin = Join-Path $wolgesDir "target" "release" "wolges-wrapper.exe"
            $dbgBin = Join-Path $wolgesDir "target" "debug" "wolges-wrapper.exe"
            if (Test-Path $relBin) { $env:WOLGES_BIN = $relBin }
            elseif (Test-Path $dbgBin) { $env:WOLGES_BIN = $dbgBin }
        } catch {
            Write-Host "warning: could not build wolges-wrapper"
        } finally { Pop-Location }
    } else {
        Write-Host "cargo not found; Hybrid/Wolges engines may not work" -ForegroundColor Yellow
    }
}

Write-Host "Starting Duplicate Tournament Manager on http://localhost:$Port ..."
Set-Location $BackendDir

function Start-Server {
    # Stop previous instance
    $pidFile = Join-Path $BackendDir "server.pid"
    if (Test-Path $pidFile) {
        $oldPid = Get-Content $pidFile -ErrorAction SilentlyContinue
        if ($oldPid) {
            try { Stop-Process -Id $oldPid -Force -ErrorAction SilentlyContinue } catch {}
            Start-Sleep -Milliseconds 300
        }
    }

    if ($Fg) {
        Write-Host "Running server in foreground (logs to stdout/stderr)..."
        & go run -mod=mod ./cmd/server
        return
    }

    # Background mode
    $stdoutLog = Join-Path $BackendDir "server.stdout"
    $stderrLog = Join-Path $BackendDir "server.stderr"
    $proc = Start-Process -FilePath "go" -ArgumentList "run","-mod=mod","./cmd/server" `
        -WorkingDirectory $BackendDir -NoNewWindow -PassThru `
        -RedirectStandardOutput $stdoutLog -RedirectStandardError $stderrLog
    $proc.Id | Out-File -FilePath $pidFile -Encoding ascii

    # Wait for health
    $tries = 25
    $ok = $false
    while ($tries -gt 0) {
        try {
            $resp = Invoke-WebRequest -Uri "http://localhost:$Port/health" -TimeoutSec 2 -ErrorAction SilentlyContinue
            if ($resp.StatusCode -eq 200) { $ok = $true; break }
        } catch {}
        Start-Sleep -Milliseconds 200
        $tries--
    }

    if ($ok) {
        Write-Host "Server is up on http://localhost:$Port (PID: $($proc.Id))"
        Write-Host ""
        Write-Host "Tips:"
        Write-Host "  - Crear partida vs-bot: boton 'Nueva partida' en la UI"
        Write-Host "  - Abort: boton 'Abortar' (POST /matches/{id}/abort)"
        Write-Host "  - Post-mortem: tras GAME_OVER, usa controles y 'Generar' para ver jugadas"
    } else {
        Write-Host "Server not healthy yet. See $stderrLog" -ForegroundColor Red
    }
}

Start-Server

# Watch mode
if ($Watch) {
    Write-Host "Watch mode enabled. Rebuilding and restarting on changes..."

    function Get-DirHash {
        param([string[]]$Dirs)
        $files = foreach ($d in $Dirs) {
            Get-ChildItem -Path $d -Recurse -Include "*.go","*.html","*.css","*.js" -File -ErrorAction SilentlyContinue
        }
        $hashes = foreach ($f in ($files | Sort-Object FullName)) {
            (Get-FileHash -Path $f.FullName -Algorithm SHA256).Hash
        }
        return ($hashes -join "`n" | Get-FileHash -Algorithm SHA256 -InputStream ([System.IO.MemoryStream]::new([System.Text.Encoding]::UTF8.GetBytes(($hashes -join "`n"))))).Hash
    }

    $staticDir = Join-Path $BackendDir "internal" "api" "static"
    $last = Get-DirHash -Dirs @($BackendDir, $staticDir)

    try {
        while ($true) {
            Start-Sleep -Seconds 1
            $cur = Get-DirHash -Dirs @($BackendDir, $staticDir)
            if ($cur -ne $last) {
                Write-Host "Changes detected. Rebuilding wrapper and restarting server..."
                Push-Location $BackendDir
                try { & go build -o $Wrapper ./cmd/macondo-wrapper } catch { Write-Host "warning: wrapper build failed" }
                Pop-Location
                Start-Server
                $last = $cur
            }
        }
    } finally {
        $pidFile = Join-Path $BackendDir "server.pid"
        if (Test-Path $pidFile) {
            $pid = Get-Content $pidFile -ErrorAction SilentlyContinue
            if ($pid) { try { Stop-Process -Id $pid -Force } catch {} }
        }
        Write-Host "Stopping..."
    }
}

Write-Host ""
Write-Host "Open your browser at: http://localhost:$Port"
Write-Host "If you need to reset in-memory state: http://localhost:$Port/reset"
