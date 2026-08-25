$ErrorActionPreference = "Stop"

$repo = if ($env:SAFELANE_REPO) { $env:SAFELANE_REPO } else { "safelane-dev/safelane" }
$installDir = if ($env:SAFELANE_INSTALL_DIR) {
    $env:SAFELANE_INSTALL_DIR
} else {
    Join-Path $env:LOCALAPPDATA "SafeLane\bin"
}
$downloadBase = if ($env:SAFELANE_DOWNLOAD_BASE_URL) {
    $env:SAFELANE_DOWNLOAD_BASE_URL.TrimEnd("/")
} else {
    "https://github.com/$repo/releases/download"
}
$version = $env:SAFELANE_VERSION
$agentHome = if ($env:SAFELANE_AGENT_HOME) {
    $env:SAFELANE_AGENT_HOME
} else {
    [Environment]::GetFolderPath([Environment+SpecialFolder]::UserProfile)
}

if (-not $version) {
    # Resolve the latest tag through GitHub's releases redirect instead of the
    # unauthenticated API. The API has a low shared-IP rate limit, which can
    # make a fresh install fail before it even starts downloading.
    $latestUri = "https://github.com/$repo/releases/latest"
    try {
        try {
            $latest = Invoke-WebRequest -Uri $latestUri -Method Head -MaximumRedirection 0 -ErrorAction Stop
            $location = $latest.Headers.Location
        } catch {
            $location = $_.Exception.Response.Headers.Location
        }
        if ($location) {
            $version = ([Uri]$location).Segments[-1].TrimEnd('/')
        }
    } catch {
        $version = $null
    }
}
if ($version -notmatch '^v\d+\.\d+\.\d+(?:[.-][0-9A-Za-z.-]+)?$') {
    throw "Could not determine a valid SafeLane release version: $version"
}

$architecture = [System.Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString().ToLowerInvariant()
$arch = switch ($architecture) {
    "x64" { "amd64" }
    "arm64" { "arm64" }
    default { throw "Unsupported Windows architecture: $architecture" }
}

$filename = "safelane-$version-windows-$arch.zip"
$tmpDir = Join-Path ([System.IO.Path]::GetTempPath()) ("safelane-install-" + [guid]::NewGuid().ToString("N"))
New-Item -ItemType Directory -Path $tmpDir | Out-Null

try {
    $archivePath = Join-Path $tmpDir $filename
    $checksumsPath = Join-Path $tmpDir "checksums.txt"
    Write-Host "Downloading SafeLane $version for windows/$arch..."
    Invoke-WebRequest -Uri "$downloadBase/$version/$filename" -OutFile $archivePath
    Invoke-WebRequest -Uri "$downloadBase/$version/checksums.txt" -OutFile $checksumsPath

    $escapedFilename = [regex]::Escape($filename)
    $checksumLine = Get-Content -LiteralPath $checksumsPath |
        Where-Object { $_ -match "^([0-9a-fA-F]{64})\s+\*?$escapedFilename$" } |
        Select-Object -First 1
    if (-not $checksumLine) {
        throw "checksums.txt has no entry for $filename"
    }
    $expected = ([regex]::Match($checksumLine, '^([0-9a-fA-F]{64})')).Groups[1].Value.ToLowerInvariant()
    $actual = (Get-FileHash -Algorithm SHA256 -LiteralPath $archivePath).Hash.ToLowerInvariant()
    if ($expected -ne $actual) {
        throw "Checksum verification failed for $filename."
    }

    $extractDir = Join-Path $tmpDir "archive"
    Expand-Archive -LiteralPath $archivePath -DestinationPath $extractDir
    $sourceBinary = Join-Path $extractDir "safelane.exe"
    if (-not (Test-Path -LiteralPath $sourceBinary -PathType Leaf)) {
        throw "$filename does not contain safelane.exe"
    }
    $sourceSkill = Join-Path $extractDir "safelane-skill\SKILL.md"
    $hasSkill = Test-Path -LiteralPath $sourceSkill -PathType Leaf

    New-Item -ItemType Directory -Path $installDir -Force | Out-Null
    $destination = Join-Path $installDir "safelane.exe"
    $stagedDestination = "$destination.new"
    Copy-Item -LiteralPath $sourceBinary -Destination $stagedDestination -Force
    Move-Item -LiteralPath $stagedDestination -Destination $destination -Force

    if ($hasSkill) {
        foreach ($skillDestination in @(
            (Join-Path $agentHome ".claude\skills\safelane\SKILL.md"),
            (Join-Path $agentHome ".agents\skills\safelane\SKILL.md")
        )) {
            $skillDirectory = Split-Path -Parent $skillDestination
            New-Item -ItemType Directory -Path $skillDirectory -Force | Out-Null
            $stagedSkill = "$skillDestination.new"
            Copy-Item -LiteralPath $sourceSkill -Destination $stagedSkill -Force
            Move-Item -LiteralPath $stagedSkill -Destination $skillDestination -Force
        }
    } else {
        Write-Warning "$version predates bundled agent skills; rerun the installer after upgrading to a newer release."
    }

    if ($env:SAFELANE_NO_PATH_UPDATE -ne "1") {
        $userPath = [Environment]::GetEnvironmentVariable("Path", "User")
        $entries = @($userPath -split ";" | Where-Object { $_ })
        $alreadyPresent = $entries | Where-Object { $_.TrimEnd("\") -ieq $installDir.TrimEnd("\") }
        if (-not $alreadyPresent) {
            $newUserPath = (@($installDir) + $entries) -join ";"
            [Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
            Write-Host "Added $installDir to your user PATH. Restart your terminal."
        }
        $processEntries = @($env:Path -split ";" | Where-Object { $_ })
        if (-not ($processEntries | Where-Object { $_.TrimEnd("\") -ieq $installDir.TrimEnd("\") })) {
            $env:Path = "$installDir;$env:Path"
        }
    }

    Write-Host "SafeLane $version installed to $destination"
    if ($hasSkill) {
        Write-Host "SafeLane skill installed for Claude and Codex under $agentHome"
    }
} finally {
    if (Test-Path -LiteralPath $tmpDir) {
        Remove-Item -LiteralPath $tmpDir -Recurse -Force
    }
}
