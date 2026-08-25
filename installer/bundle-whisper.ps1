# Copy whisper.cpp and its DLLs into the extension so transcription works on a
# Windows PC with no developer tooling installed.
#
# Run this ON WINDOWS, from a checkout of this repository:
#     powershell -ExecutionPolicy Bypass -File installer\bundle-whisper.ps1
#
# It downloads the official whisper.cpp Windows release unless -WhisperZip
# points at one already on disk. The speech MODEL is not bundled — it is
# hundreds of megabytes and downloads itself on first run. This ships only the
# engine.
#
# Unlike macOS, nothing here rewrites load paths: the Windows loader searches
# the directory the .exe was loaded from, so the DLLs simply have to sit beside
# whisper-cli.exe. install.go copies them into the app's bin directory together.

[CmdletBinding()]
param(
    # A whisper.cpp release zip already on disk. Leave empty to download.
    [string]$WhisperZip = "",
    # Which prebuilt release to fetch. x64 is right for almost every PC;
    # Windows on ARM would need the arm64 asset instead.
    [string]$Release = "v1.7.6",
    [string]$Asset   = "whisper-bin-x64.zip"
)

$ErrorActionPreference = "Stop"

$dest = Join-Path (Split-Path -Parent $MyInvocation.MyCommand.Path) "mcpb\server"
New-Item -ItemType Directory -Force -Path $dest | Out-Null

$work = Join-Path ([System.IO.Path]::GetTempPath()) ("whisper-bundle-" + [guid]::NewGuid())
New-Item -ItemType Directory -Force -Path $work | Out-Null

try {
    if (-not $WhisperZip) {
        $url = "https://github.com/ggml-org/whisper.cpp/releases/download/$Release/$Asset"
        $WhisperZip = Join-Path $work $Asset
        Write-Host "Downloading $url"
        Invoke-WebRequest -Uri $url -OutFile $WhisperZip
    }
    if (-not (Test-Path $WhisperZip)) {
        throw "whisper release zip not found: $WhisperZip"
    }

    $extracted = Join-Path $work "extracted"
    Expand-Archive -Path $WhisperZip -DestinationPath $extracted -Force

    # The release layout has moved between versions, so find the executable
    # rather than assuming where it sits.
    $exe = Get-ChildItem -Path $extracted -Recurse -Filter "whisper-cli.exe" |
           Select-Object -First 1
    if (-not $exe) {
        throw "whisper-cli.exe not found inside $WhisperZip"
    }

    Copy-Item $exe.FullName (Join-Path $dest "whisper-cli.exe") -Force

    # Every DLL beside the executable: whisper.dll, ggml*.dll and their
    # dependencies. ggml aborts rather than degrading when it cannot load a
    # backend, so a missing DLL is not a quiet loss of quality — it is a crash.
    $dlls = Get-ChildItem -Path $exe.Directory -Filter "*.dll"
    foreach ($dll in $dlls) {
        Copy-Item $dll.FullName (Join-Path $dest $dll.Name) -Force
    }

    if (-not ($dlls | Where-Object { $_.Name -like "ggml-cpu*.dll" })) {
        Write-Warning "No ggml-cpu*.dll in this release. The CPU backend may be linked into ggml.dll, which is fine — but if transcription aborts on the target PC, this is the first thing to check."
    }

    $bundled = (Get-ChildItem $dest -Filter "*.dll" | Measure-Object -Property Length -Sum).Sum
    Write-Host ("Bundled whisper into {0} ({1:N1} MB of DLLs, {2} files)" -f `
        $dest, ($bundled / 1MB), $dlls.Count)
    Write-Host "Reminder: test on a PC with no whisper install of its own before shipping."
}
finally {
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
