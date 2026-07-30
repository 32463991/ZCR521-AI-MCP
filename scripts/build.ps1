[CmdletBinding()]
param(
    [string]$Version = "0.01",
    [string]$NdkPath = "",
    [string]$PythonPath = "",
    [long]$SourceDateEpoch = 1785340800,
    [switch]$SkipTests,
    [switch]$Skip7Zip
)

$ErrorActionPreference = "Stop"
$Repo = (Resolve-Path (Join-Path $PSScriptRoot "..")).Path
$Build = Join-Path $Repo "build"
$Dist = Join-Path $Repo "dist"
$ModuleStage = Join-Path $Build "module"
$Go = Join-Path $Repo ".tools\go\bin\go.exe"
if (-not (Test-Path -LiteralPath $Go)) {
    $GoCommand = Get-Command go -ErrorAction SilentlyContinue
    if ($null -eq $GoCommand) {
        throw "Go 1.26.5 is required. Install it or extract it to .tools\go."
    }
    $Go = $GoCommand.Source
}
if ([string]::IsNullOrWhiteSpace($PythonPath)) {
    $PythonCommand = Get-Command python -ErrorAction SilentlyContinue
    if ($null -eq $PythonCommand) {
        $PythonCommand = Get-Command python3 -ErrorAction SilentlyContinue
    }
    if ($null -eq $PythonCommand) {
        throw "Python 3.11+ is required for ELF, ZIP and SBOM verification; use -PythonPath to select it."
    }
    $PythonPath = $PythonCommand.Source
}

if ([string]::IsNullOrWhiteSpace($NdkPath)) {
    if (-not [string]::IsNullOrWhiteSpace($env:ANDROID_NDK_HOME)) {
        $NdkPath = $env:ANDROID_NDK_HOME
    } elseif (-not [string]::IsNullOrWhiteSpace($env:LOCALAPPDATA)) {
        $NdkPath = Join-Path $env:LOCALAPPDATA "Android\Sdk\ndk\29.0.14206865"
    }
}
if (-not (Test-Path -LiteralPath $NdkPath)) {
    throw "Android NDK r29 was not found: $NdkPath"
}

$MappedDrive = $null
if ($NdkPath -match "[^\u0000-\u007F]") {
    foreach ($letter in @("N", "M", "L", "K")) {
        if (-not (Test-Path "$letter`:\")) {
            subst "$letter`:" $NdkPath
            $MappedDrive = "$letter`:"
            $NdkPath = "$letter`:\"
            break
        }
    }
    if ($null -eq $MappedDrive) {
        throw "The NDK path contains non-ASCII characters and no drive letter is available for subst."
    }
}

try {
    New-Item -ItemType Directory -Force -Path $Build, $Dist | Out-Null
    $env:SOURCE_DATE_EPOCH = [string]$SourceDateEpoch
    $env:GOTOOLCHAIN = "local"
    if ([string]::IsNullOrWhiteSpace($env:GOPROXY)) {
        $env:GOPROXY = "https://proxy.golang.org,direct"
    }

    & $Go version
    $ActualGo = & $Go env GOVERSION
    if ($ActualGo -ne "go1.26.5") {
        throw "The Go toolchain must be exactly 1.26.5; found $ActualGo"
    }
    $NdkProperties = Join-Path $NdkPath "source.properties"
    if (-not (Test-Path -LiteralPath $NdkProperties)) {
        throw "The NDK is missing source.properties: $NdkProperties"
    }
    $NdkRevisionLine = Select-String -LiteralPath $NdkProperties -Pattern '^\s*Pkg\.Revision\s*=\s*29\.0\.14206865\s*$'
    if ($null -eq $NdkRevisionLine) {
        throw "Android NDK must be exactly r29 (29.0.14206865)."
    }
    & $Go mod verify
    if (-not $SkipTests) {
        & $Go test ./...
        if ($LASTEXITCODE -ne 0) { throw "Unit tests failed." }
        if ([string]::IsNullOrWhiteSpace($env:CC)) {
            $RaceCompiler = Get-Command gcc -ErrorAction SilentlyContinue
            if ($null -eq $RaceCompiler) {
                throw "Windows race tests require a MinGW-w64 GCC on PATH. Install one or use -SkipTests only after CI has passed."
            }
            $env:CC = $RaceCompiler.Source
        }
        $env:CGO_ENABLED = "1"
        & $Go test -race ./...
        if ($LASTEXITCODE -ne 0) { throw "Race tests failed." }
    }
    & $Go mod vendor
    & $Go run -mod=vendor ./cmd/zcr521d schema --output (Join-Path $Repo "schemas\tools.json")
    if ($LASTEXITCODE -ne 0) {
        throw "Schema generation failed."
    }

    $Commit = "unknown"
    if ((Get-Command git -ErrorAction SilentlyContinue) -and (Test-Path -LiteralPath (Join-Path $Repo ".git"))) {
        $CommitCandidate = (& git -C $Repo rev-parse --verify HEAD 2>$null)
        if ($LASTEXITCODE -eq 0) { $Commit = $CommitCandidate.Trim() }
    }
    $BuildTime = [DateTimeOffset]::FromUnixTimeSeconds($SourceDateEpoch).UtcDateTime.ToString("yyyy-MM-ddTHH:mm:ssZ")
    $ModulePropPath = Join-Path $Repo "module\module.prop"
    $ModulePropSHA256 = (Get-FileHash -LiteralPath $ModulePropPath -Algorithm SHA256).Hash.ToLowerInvariant()
    $LdFlags = "-s -w -buildid= -X github.com/zcr521/android-ai-mcp/internal/buildinfo.Version=$Version -X github.com/zcr521/android-ai-mcp/internal/buildinfo.Commit=$Commit -X github.com/zcr521/android-ai-mcp/internal/buildinfo.BuildTime=$BuildTime -X github.com/zcr521/android-ai-mcp/internal/buildinfo.ModulePropSHA256=$ModulePropSHA256 -linkmode external -extldflags=-Wl,-z,max-page-size=16384"
    $AndroidOut = Join-Path $Build "android"
    New-Item -ItemType Directory -Force -Path $AndroidOut | Out-Null
    $Toolchain = Join-Path $NdkPath "toolchains\llvm\prebuilt\windows-x86_64\bin"
    $Targets = @(
        @{ ABI="arm64-v8a"; GOARCH="arm64"; CC="aarch64-linux-android26-clang.cmd"; Machine="183" },
        @{ ABI="armeabi-v7a"; GOARCH="arm"; GOARM="7"; CC="armv7a-linux-androideabi26-clang.cmd"; Machine="40" },
        @{ ABI="x86_64"; GOARCH="amd64"; CC="x86_64-linux-android26-clang.cmd"; Machine="62" }
    )
    foreach ($target in $Targets) {
        $AbiOut = Join-Path $AndroidOut $target.ABI
        New-Item -ItemType Directory -Force -Path $AbiOut | Out-Null
        $env:GOOS = "android"
        $env:GOARCH = $target.GOARCH
        $env:GOARM = if ($target.ContainsKey("GOARM")) { $target.GOARM } else { "" }
        $env:CGO_ENABLED = "1"
        $env:CC = Join-Path $Toolchain $target.CC
        & $Go build -mod=vendor -trimpath -buildmode=pie -tags "osusergo,netgo" -ldflags $LdFlags -o (Join-Path $AbiOut "zcr521d") ./cmd/zcr521d
        & $PythonPath (Join-Path $Repo "scripts\verify_elf.py") --file (Join-Path $AbiOut "zcr521d") --machine $target.Machine --page-size 16384 --api 26
    }
    Remove-Item Env:GOOS, Env:GOARCH, Env:GOARM, Env:CGO_ENABLED, Env:CC -ErrorAction SilentlyContinue

    if (-not $Skip7Zip) {
        $Bash = Get-Command bash -ErrorAction SilentlyContinue
        if ($null -eq $Bash) {
            throw "Building 7zz requires bash (Git Bash, WSL or Linux CI); placeholder binaries are forbidden."
        }
        $env:ANDROID_NDK_HOME = $NdkPath.Replace("\", "/")
        $env:ANDROID_API_LEVEL = "26"
        $env:SEVENZIP_OUT_DIR = (Join-Path $Repo "third_party\7zip\out").Replace("\", "/")
        & $Bash.Source (Join-Path $Repo "third_party\7zip\build-android.sh")
        Remove-Item Env:ANDROID_API_LEVEL, Env:SEVENZIP_OUT_DIR -ErrorAction SilentlyContinue
    }

    $BridgeLdFlags = "-s -w -buildid= -X github.com/zcr521/android-ai-mcp/internal/buildinfo.Version=$Version -X github.com/zcr521/android-ai-mcp/internal/buildinfo.Commit=$Commit -X github.com/zcr521/android-ai-mcp/internal/buildinfo.BuildTime=$BuildTime"
    $BridgeOut = Join-Path $Build "bridge"
    New-Item -ItemType Directory -Force -Path $BridgeOut | Out-Null
    foreach ($hostTarget in @(
        @{ GOOS="windows"; GOARCH="amd64"; Name="zcr521-bridge-windows-amd64.exe" },
        @{ GOOS="linux"; GOARCH="amd64"; Name="zcr521-bridge-linux-amd64" },
        @{ GOOS="linux"; GOARCH="arm64"; Name="zcr521-bridge-linux-arm64" },
        @{ GOOS="darwin"; GOARCH="amd64"; Name="zcr521-bridge-macos-amd64" },
        @{ GOOS="darwin"; GOARCH="arm64"; Name="zcr521-bridge-macos-arm64" }
    )) {
        $env:GOOS = $hostTarget.GOOS
        $env:GOARCH = $hostTarget.GOARCH
        $env:CGO_ENABLED = "0"
        & $Go build -mod=vendor -trimpath -ldflags $BridgeLdFlags -o (Join-Path $BridgeOut $hostTarget.Name) ./cmd/zcr521-bridge
    }
    Remove-Item Env:GOOS, Env:GOARCH, Env:CGO_ENABLED -ErrorAction SilentlyContinue

    if (Test-Path -LiteralPath $ModuleStage) {
        Remove-Item -LiteralPath $ModuleStage -Recurse -Force
    }
    Copy-Item -LiteralPath (Join-Path $Repo "module") -Destination $ModuleStage -Recurse
    foreach ($target in $Targets) {
        $TargetDir = Join-Path $ModuleStage ("bin\" + $target.ABI)
        New-Item -ItemType Directory -Force -Path $TargetDir | Out-Null
        Copy-Item -LiteralPath (Join-Path $AndroidOut ($target.ABI + "\zcr521d")) -Destination (Join-Path $TargetDir "zcr521d")
        Copy-Item -LiteralPath (Join-Path $Repo ("third_party\7zip\out\" + $target.ABI + "\7zz")) -Destination (Join-Path $TargetDir "7zz")
    }
    New-Item -ItemType Directory -Force -Path (Join-Path $ModuleStage "licenses") | Out-Null
    Copy-Item -LiteralPath (Join-Path $Repo "third_party\7zip\out\LICENSE-7ZIP.txt") -Destination (Join-Path $ModuleStage "licenses\7zip.txt")
    Copy-Item -LiteralPath (Join-Path $Repo "LICENSE") -Destination (Join-Path $ModuleStage "licenses\GPL-3.0-or-later.txt")
    Copy-Item -LiteralPath (Join-Path $Repo "THIRD_PARTY_NOTICES.md") -Destination (Join-Path $ModuleStage "licenses\THIRD_PARTY_NOTICES.md")

    & $PythonPath (Join-Path $Repo "scripts\package.py") --repo $Repo --module-stage $ModuleStage --bridge-dir $BridgeOut --dist $Dist --version $Version --epoch $SourceDateEpoch
    if ($LASTEXITCODE -ne 0) {
        throw "Packaging failed."
    }
    & $PythonPath (Join-Path $Repo "scripts\verify_module.py") (Join-Path $Dist "ZCR521-Android-AI-MCP-v$Version-universal.zip")
    if ($LASTEXITCODE -ne 0) {
        throw "Module verification failed."
    }
} finally {
    if ($null -ne $MappedDrive) {
        subst $MappedDrive /D
    }
}
