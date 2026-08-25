# Copy whisper.cpp and the DLLs it needs into the extension, so transcription
# works on a Windows PC with no developer tooling installed.
#
#     powershell -ExecutionPolicy Bypass -File installer\bundle-whisper.ps1
#
# The speech MODEL is not bundled — it is hundreds of megabytes and downloads
# itself on first run. This ships only the engine (~10 MB).
#
# Note this does not have to run on Windows: it only downloads a zip and copies
# files out of it, which any platform can do. See bundle-whisper.sh for the
# equivalent, which is what actually produced the committed files.
#
# Unlike macOS, nothing here rewrites load paths: the Windows loader searches
# the directory the .exe was loaded from, so the DLLs simply have to sit beside
# whisper-cli.exe. install.go copies them into the app's bin directory together.

[CmdletBinding()]
param(
    [string]$WhisperZip = "",
    # whisper.cpp tags its prebuilt releases bNNNN, not vX.Y.Z.
    [string]$Release = "b4938",
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
    if (-not (Test-Path $WhisperZip)) { throw "whisper release zip not found: $WhisperZip" }

    $extracted = Join-Path $work "extracted"
    Expand-Archive -Path $WhisperZip -DestinationPath $extracted -Force

    # The release layout has moved between versions, so find the executable
    # rather than assuming where it sits.
    $exe = Get-ChildItem -Path $extracted -Recurse -Filter "whisper-cli.exe" | Select-Object -First 1
    if (-not $exe) { throw "whisper-cli.exe not found inside $WhisperZip" }

    Copy-Item $exe.FullName (Join-Path $dest "whisper-cli.exe") -Force

    # Only what whisper-cli actually loads. The release also carries SDL2.dll,
    # llama.dll and parakeet.dll for its other tools — about 5 MB that would
    # never be opened.
    #
    # Every ggml-cpu-*.dll matters: the release ships one per instruction-set
    # generation (sandybridge, haswell, skylakex, icelake, alderlake, …) and
    # ggml picks the right one for the CPU at run time. Shipping a subset means
    # a machine outside that subset gets no backend, and ggml aborts rather than
    # falling back. This is also why GGML_BACKEND_PATH must NOT be set on
    # Windows — it names a single file and would pin every PC to one variant.
    $wanted = @("whisper.dll", "ggml.dll", "ggml-base.dll")
    $copied = 0
    foreach ($dll in Get-ChildItem -Path $exe.Directory -Filter "*.dll") {
        if ($wanted -contains $dll.Name -or $dll.Name -like "ggml-cpu-*.dll") {
            Copy-Item $dll.FullName (Join-Path $dest $dll.Name) -Force
            $copied++
        }
    }

    $variants = (Get-ChildItem $dest -Filter "ggml-cpu-*.dll").Count
    if ($variants -lt 2) {
        Write-Warning "Only $variants ggml CPU backend(s) bundled. Expect one per instruction-set generation; too few means older or newer CPUs will fail to start."
    }

    $size = (Get-ChildItem $dest -Filter "*.dll" | Measure-Object -Property Length -Sum).Sum
    Write-Host ("Bundled whisper into {0} ({1} DLLs, {2} CPU backends, {3:N1} MB)" -f `
        $dest, $copied, $variants, ($size / 1MB))
    Write-Host "Reminder: test on a PC with no whisper install of its own before shipping."
}
finally {
    Remove-Item -Recurse -Force $work -ErrorAction SilentlyContinue
}
