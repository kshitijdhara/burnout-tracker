#Requires -Version 5.1
<#
Installs burnout for the current user on Windows (ADR-0005).

Please read this script before you run it. It:
  1. picks the release asset for this CPU (amd64 or arm64);
  2. downloads checksums.txt and checksums.txt.sig from the GitHub release;
  3. checks the signature against the release keys pinned below, using
     .NET's ECDsa (CNG), then the asset's SHA-256 against the signed
     checksums.txt;
  4. installs burnout.exe to %LOCALAPPDATA%\Programs\burnout (or -Dir) and
     adds that folder to your user PATH.

It records nothing and starts nothing. Collection starts only once you have
run `burnout setup` (consent) and `burnout service install` yourself; this
script just prints those steps. There is no option to skip either (privacy
& security section 5.2).

usage: install.ps1 [-Version vX.Y.Z] [-Dir DIR]
  BURNOUT_VERSION and BURNOUT_INSTALL_DIR set the same things.

To verify a download by hand instead, see packaging/README.md.
#>
[CmdletBinding()]
param(
    [string]$Version = $env:BURNOUT_VERSION,
    [string]$Dir = $env:BURNOUT_INSTALL_DIR
)

$ErrorActionPreference = 'Stop'
Set-StrictMode -Version 3.0

$BurnoutRepo = 'kshitijdhara/burnout-tracker'

# Pinned release keys, current then next. Keep in step with install.sh and
# internal/release/keys.go (a test checks).
# PLACEHOLDER keys until the key ceremony in packaging/README.md.
$BurnoutKeys = @(
    @'
-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAE/8SWkP4fESmdwPU6RS6MaGrtL7OP
NsOyRNsSrsKFL05ePQxaew10J1Z+OW/Iza6IjmogozwXeH314JGJjpkEEQ==
-----END PUBLIC KEY-----
'@,
    @'
-----BEGIN PUBLIC KEY-----
MFkwEwYHKoZIzj0CAQYIKoZIzj0DAQcDQgAEinX0Slhy6+TMIgTbJLbzeaxhXtYn
tPFyZNyYDQHrGNPPc2/fsyjxaEr+DEasJXoVs4l1S+aiIF0Z29NV7HrOxQ==
-----END PUBLIC KEY-----
'@
)

# The DER header of every P-256 SubjectPublicKeyInfo, up to the
# uncompressed point (0x04 || X || Y).
$P256SpkiPrefix = [byte[]](
    0x30, 0x59, 0x30, 0x13, 0x06, 0x07, 0x2A, 0x86, 0x48, 0xCE, 0x3D, 0x02, 0x01,
    0x06, 0x08, 0x2A, 0x86, 0x48, 0xCE, 0x3D, 0x03, 0x01, 0x07, 0x03, 0x42, 0x00, 0x04)

# ConvertTo-EccPublicBlob turns a P-256 PEM public key into the CNG
# BCRYPT_ECCKEY_BLOB that CngKey.Import takes. Windows PowerShell 5.1 runs on
# .NET Framework, which can't import a PEM or SubjectPublicKeyInfo itself.
function ConvertTo-EccPublicBlob([string]$Pem) {
    $b64 = ($Pem -replace '-----(BEGIN|END) PUBLIC KEY-----', '') -replace '\s', ''
    $der = [Convert]::FromBase64String($b64)
    if ($der.Length -ne 91) { throw 'release key: not a P-256 public key' }
    for ($i = 0; $i -lt $P256SpkiPrefix.Length; $i++) {
        if ($der[$i] -ne $P256SpkiPrefix[$i]) { throw 'release key: not a P-256 public key' }
    }
    # "ECS1" (BCRYPT_ECDSA_PUBLIC_P256_MAGIC), key length 32, then X and Y.
    $blob = New-Object byte[] 72
    [byte[]](0x45, 0x43, 0x53, 0x31, 0x20, 0x00, 0x00, 0x00).CopyTo($blob, 0)
    [Array]::Copy($der, 27, $blob, 8, 64)
    return , $blob
}

# ConvertFrom-DerSignature turns the ASN.1 DER signature openssl writes
# (SEQUENCE { INTEGER r, INTEGER s }) into the 64-byte r||s that
# ECDsa.VerifyData takes on .NET Framework. Anything malformed throws.
function ConvertFrom-DerSignature([byte[]]$Der) {
    $out = New-Object byte[] 64
    if ($Der.Length -lt 8 -or $Der[0] -ne 0x30 -or $Der[1] -ne ($Der.Length - 2)) {
        throw 'signature: not a DER ECDSA signature'
    }
    $pos = 2
    foreach ($half in 0, 1) {
        if ($pos + 2 -gt $Der.Length -or $Der[$pos] -ne 0x02) { throw 'signature: not a DER ECDSA signature' }
        $len = $Der[$pos + 1]
        $start = $pos + 2
        if ($len -lt 1 -or $len -gt 33 -or $start + $len -gt $Der.Length) { throw 'signature: not a DER ECDSA signature' }
        # Drop the sign byte DER adds when the top bit is set.
        while ($len -gt 32) {
            if ($Der[$start] -ne 0) { throw 'signature: not a DER ECDSA signature' }
            $start++; $len--
        }
        [Array]::Copy($Der, $start, $out, 32 * $half + (32 - $len), $len)
        $pos = $start + $len
    }
    if ($pos -ne $Der.Length) { throw 'signature: trailing data' }
    return , $out
}

# Test-SignatureWithKey is true if SigPath holds Pem's key's signature of
# DataPath.
function Test-SignatureWithKey([string]$Pem, [string]$DataPath, [string]$SigPath) {
    try {
        $sig = ConvertFrom-DerSignature ([IO.File]::ReadAllBytes($SigPath))
        $key = [Security.Cryptography.CngKey]::Import((ConvertTo-EccPublicBlob $Pem),
            [Security.Cryptography.CngKeyBlobFormat]::EccPublicBlob)
    } catch {
        return $false
    }
    $ecdsa = New-Object Security.Cryptography.ECDsaCng -ArgumentList $key
    try {
        return $ecdsa.VerifyData([IO.File]::ReadAllBytes($DataPath), $sig,
            [Security.Cryptography.HashAlgorithmName]::SHA256)
    } finally {
        $ecdsa.Dispose()
        $key.Dispose()
    }
}

# Test-ReleaseSignature is true if a pinned key signed DataPath.
function Test-ReleaseSignature([string]$DataPath, [string]$SigPath) {
    foreach ($pem in $BurnoutKeys) {
        if (Test-SignatureWithKey $pem $DataPath $SigPath) { return $true }
    }
    return $false
}

# Get-ExpectedSha256 returns Name's hash from ChecksumsPath, or $null
# unless it is listed exactly once.
function Get-ExpectedSha256([string]$ChecksumsPath, [string]$Name) {
    $found = @()
    foreach ($line in [IO.File]::ReadAllLines($ChecksumsPath)) {
        $f = $line.Trim() -split '\s+'
        if ($f.Count -eq 2 -and $f[1] -ceq $Name) { $found += $f[0].ToLowerInvariant() }
    }
    if ($found.Count -ne 1) { return $null }
    return $found[0]
}

# Test-AssetChecksum is true if FilePath's hash is Name's line in
# ChecksumsPath. Only trust ChecksumsPath once Test-ReleaseSignature passed.
function Test-AssetChecksum([string]$ChecksumsPath, [string]$FilePath, [string]$Name) {
    $want = Get-ExpectedSha256 $ChecksumsPath $Name
    if (-not $want) { return $false }
    $got = (Get-FileHash -Algorithm SHA256 -LiteralPath $FilePath).Hash.ToLowerInvariant()
    return $got -ceq $want
}

function Get-BurnoutArch {
    $arch = $env:PROCESSOR_ARCHITECTURE
    try { $arch = [Runtime.InteropServices.RuntimeInformation]::OSArchitecture.ToString() } catch { }
    switch ($arch) {
        { $_ -in 'X64', 'AMD64' } { return 'amd64' }
        { $_ -in 'Arm64', 'ARM64' } { return 'arm64' }
        default { throw "no release for Windows on $arch" }
    }
}

function Invoke-Download([string]$Url, [string]$OutFile) {
    try {
        Invoke-WebRequest -UseBasicParsing -Uri $Url -OutFile $OutFile
    } catch {
        throw "download failed: $Url ($($_.Exception.Message))"
    }
}

function Install-Burnout([string]$Version, [string]$Dir) {
    # Windows PowerShell 5.1 may default to TLS 1.0; GitHub needs 1.2.
    [Net.ServicePointManager]::SecurityProtocol = [Net.ServicePointManager]::SecurityProtocol -bor
        [Net.SecurityProtocolType]::Tls12
    $ProgressPreference = 'SilentlyContinue' # the progress bar slows downloads a lot

    if (-not $Dir) { $Dir = Join-Path $env:LOCALAPPDATA 'Programs\burnout' }
    if (-not $Version) {
        $Version = (Invoke-RestMethod -UseBasicParsing "https://api.github.com/repos/$BurnoutRepo/releases/latest").tag_name
    }
    if (-not $Version.StartsWith('v')) { $Version = "v$Version" }
    $asset = "burnout_$($Version.Substring(1))_windows_$(Get-BurnoutArch).zip"
    $base = "https://github.com/$BurnoutRepo/releases/download/$Version"

    $tmp = Join-Path ([IO.Path]::GetTempPath()) ("burnout-install-" + [Guid]::NewGuid())
    New-Item -ItemType Directory -Path $tmp | Out-Null
    try {
        Write-Host "Downloading burnout $Version"
        $sums = Join-Path $tmp 'checksums.txt'
        $sig = Join-Path $tmp 'checksums.txt.sig'
        Invoke-Download "$base/checksums.txt" $sums
        Invoke-Download "$base/checksums.txt.sig" $sig
        if (-not (Test-ReleaseSignature $sums $sig)) {
            throw 'checksums.txt is not signed by a pinned release key; not installing'
        }
        Write-Host 'Signature OK (pinned release key)'

        if (-not (Get-ExpectedSha256 $sums $asset)) { throw "$Version has no $asset" }
        $zip = Join-Path $tmp $asset
        Invoke-Download "$base/$asset" $zip
        if (-not (Test-AssetChecksum $sums $zip $asset)) {
            throw "$asset does not match checksums.txt; not installing"
        }
        Write-Host "Checksum OK ($asset)"

        # Only now that it is verified, drop the downloaded-from-internet mark.
        Unblock-File -LiteralPath $zip
        $x = Join-Path $tmp 'x'
        Expand-Archive -LiteralPath $zip -DestinationPath $x
        New-Item -ItemType Directory -Force -Path $Dir | Out-Null
        $exe = Join-Path $Dir 'burnout.exe'
        $new = Join-Path $Dir 'burnout.exe.new'
        Copy-Item -LiteralPath (Join-Path $x 'burnout.exe') -Destination $new -Force
        # A running burnout.exe can't be replaced, but it can be renamed.
        if (Test-Path -LiteralPath $exe) {
            Move-Item -LiteralPath $exe -Destination (Join-Path $Dir 'burnout.exe.old') -Force
        }
        Move-Item -LiteralPath $new -Destination $exe
        Write-Host "Installed $exe"
    } finally {
        Remove-Item -Recurse -Force -LiteralPath $tmp -ErrorAction SilentlyContinue
    }

    $userPath = [Environment]::GetEnvironmentVariable('Path', 'User')
    $parts = @($userPath -split ';' | Where-Object { $_ })
    if ($parts -notcontains $Dir) {
        [Environment]::SetEnvironmentVariable('Path', (($parts + $Dir) -join ';'), 'User')
        $env:Path = "$env:Path;$Dir"
        Write-Host "Added $Dir to your user PATH (new terminals pick it up)"
    }

    Write-Host ''
    Write-Host "Nothing is recorded yet. When you're ready:"
    Write-Host '  burnout setup             review what is collected and consent'
    Write-Host '  burnout service install   run in the background from login'
}

# CI dot-sources this file with BURNOUT_INSTALL_LIB=1 to test the verifiers
# (packaging/test-install.ps1).
if ($env:BURNOUT_INSTALL_LIB -ne '1') {
    Install-Burnout $Version $Dir
}
