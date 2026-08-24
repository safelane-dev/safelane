$ErrorActionPreference = "Stop"

$repoRoot = Split-Path -Parent $PSScriptRoot
$testRoot = Join-Path ([System.IO.Path]::GetTempPath()) ("safelane-installer-test-" + [guid]::NewGuid().ToString("N"))
$expectedTempRoot = [System.IO.Path]::GetFullPath([System.IO.Path]::GetTempPath())
$resolvedTestRoot = [System.IO.Path]::GetFullPath($testRoot)
if (-not $resolvedTestRoot.StartsWith($expectedTempRoot, [System.StringComparison]::OrdinalIgnoreCase)) {
    throw "Refusing to use a test directory outside the system temp directory: $resolvedTestRoot"
}

$server = $null
$savedEnvironment = @{}
foreach ($name in @("SAFELANE_VERSION", "SAFELANE_DOWNLOAD_BASE_URL", "SAFELANE_INSTALL_DIR", "SAFELANE_AGENT_HOME", "SAFELANE_NO_PATH_UPDATE")) {
    $savedEnvironment[$name] = [Environment]::GetEnvironmentVariable($name, "Process")
}

try {
    $version = "v0.0.0-test"
    $releaseDir = Join-Path $testRoot $version
    $packageDir = Join-Path $testRoot "package"
    $installDir = Join-Path $testRoot "install"
    $agentHome = Join-Path $testRoot "agent-home"
    New-Item -ItemType Directory -Path $releaseDir, $packageDir | Out-Null

    $sourceBinary = Join-Path $packageDir "safelane.exe"
    [System.IO.File]::WriteAllText($sourceBinary, $version)
    $skillDir = Join-Path $packageDir "safelane-skill"
    New-Item -ItemType Directory -Path $skillDir | Out-Null
    [System.IO.File]::WriteAllText((Join-Path $skillDir "SKILL.md"), "test-safelane-skill")
    $filename = "safelane-$version-windows-amd64.zip"
    $archivePath = Join-Path $releaseDir $filename
    Compress-Archive -Path (Join-Path $packageDir "*") -DestinationPath $archivePath
    $hash = (Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash.ToLowerInvariant()
    [System.IO.File]::WriteAllText((Join-Path $releaseDir "checksums.txt"), "$hash  $filename`n")

    $listener = [System.Net.Sockets.TcpListener]::new([System.Net.IPAddress]::Loopback, 0)
    $listener.Start()
    $port = ([System.Net.IPEndPoint]$listener.LocalEndpoint).Port
    $listener.Stop()
    $python = (Get-Command python -ErrorAction Stop).Source
    $server = Start-Process -FilePath $python -ArgumentList @(
        "-m", "http.server", "$port", "--bind", "127.0.0.1", "--directory", $testRoot
    ) -WindowStyle Hidden -PassThru

    $ready = $false
    foreach ($attempt in 1..50) {
        try {
            Invoke-WebRequest -UseBasicParsing -Uri "http://127.0.0.1:$port/$version/checksums.txt" | Out-Null
            $ready = $true
            break
        } catch {
            Start-Sleep -Milliseconds 100
        }
    }
    if (-not $ready) { throw "test release server did not start" }

    $env:SAFELANE_VERSION = $version
    $env:SAFELANE_DOWNLOAD_BASE_URL = "http://127.0.0.1:$port"
    $env:SAFELANE_INSTALL_DIR = $installDir
    $env:SAFELANE_AGENT_HOME = $agentHome
    $env:SAFELANE_NO_PATH_UPDATE = "1"
    & (Join-Path $repoRoot "docs\install.ps1")

    $installedBinary = Join-Path $installDir "safelane.exe"
    if ((Get-Content -Raw -LiteralPath $installedBinary) -ne $version) {
        throw "PowerShell installer did not install the expected binary"
    }
    foreach ($skillPath in @(
        (Join-Path $agentHome ".claude\skills\safelane\SKILL.md"),
        (Join-Path $agentHome ".agents\skills\safelane\SKILL.md")
    )) {
        if ((Get-Content -Raw -LiteralPath $skillPath) -ne "test-safelane-skill") {
            throw "PowerShell installer did not install the SafeLane skill to $skillPath"
        }
    }

    & (Join-Path $repoRoot "docs\install.ps1") *> $null
    if ((Get-Content -Raw -LiteralPath $installedBinary) -ne $version) {
        throw "PowerShell installer did not replace an existing installation"
    }

    [System.IO.File]::AppendAllText($archivePath, "corrupt")
    $failed = $false
    try {
        & (Join-Path $repoRoot "docs\install.ps1") *> $null
    } catch {
        $failed = $_.Exception.Message -like "*Checksum verification failed*"
    }
    if (-not $failed) { throw "PowerShell installer accepted an archive with the wrong checksum" }
    if ((Get-Content -Raw -LiteralPath $installedBinary) -ne $version) {
        throw "failed upgrade replaced the previously installed binary"
    }

    Write-Host "PowerShell installer tests passed"
} finally {
    if ($server -and -not $server.HasExited) {
        Stop-Process -Id $server.Id -Force
        $server.WaitForExit()
    }
    foreach ($name in $savedEnvironment.Keys) {
        [Environment]::SetEnvironmentVariable($name, $savedEnvironment[$name], "Process")
    }
    if (Test-Path -LiteralPath $resolvedTestRoot) {
        Remove-Item -LiteralPath $resolvedTestRoot -Recurse -Force
    }
}
