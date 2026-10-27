# Tests install.ps1's signature and checksum checks against fixtures signed
# with a throwaway key (testdata/). CI runs it on Windows under both
# Windows PowerShell 5.1 (.NET Framework) and PowerShell 7.
$ErrorActionPreference = 'Stop'

$env:BURNOUT_INSTALL_LIB = '1'
. (Join-Path $PSScriptRoot 'install.ps1')
Set-StrictMode -Off # install.ps1 sets it; the checks below don't need it

$td = Join-Path $PSScriptRoot 'testdata'
$sums = Join-Path $td 'checksums.txt'
$sig = Join-Path $td 'checksums.txt.sig'
$asset = Join-Path $td 'asset.bin'
$fixture = [IO.File]::ReadAllText((Join-Path $td 'fixture.pub.pem'))
$script:fails = 0

function Expect([string]$Label, [bool]$Want, [scriptblock]$Check) {
    $got = $false
    try { $got = [bool](& $Check) } catch { $got = $false }
    if ($got -eq $Want) { Write-Host "ok   $Label" } else { Write-Host "FAIL $Label (got $got)"; $script:fails++ }
}

Write-Host "# PowerShell $($PSVersionTable.PSVersion) ($($PSVersionTable.PSEdition))"

$tampered = Join-Path ([IO.Path]::GetTempPath()) ("burnout-tampered-" + [Guid]::NewGuid())
[IO.File]::WriteAllBytes($tampered, [IO.File]::ReadAllBytes($sums) + [byte]0x0A)
try {
    Expect 'fixture key, fixture signature' $true { Test-SignatureWithKey $fixture $sums $sig }
    Expect 'tampered checksums.txt' $false { Test-SignatureWithKey $fixture $tampered $sig }
    Expect 'wrong key' $false { Test-SignatureWithKey $BurnoutKeys[0] $sums $sig }
    Expect 'unpinned key' $false { Test-ReleaseSignature $sums $sig }
    Expect 'pinned keys parse' $true {
        foreach ($k in $BurnoutKeys) { [void](ConvertTo-EccPublicBlob $k) }; $true
    }

    Expect 'matching hash' $true { Test-AssetChecksum $sums $asset 'asset.bin' }
    Expect 'different hash' $false { Test-AssetChecksum $sums $asset 'other.bin' }
    Expect 'unlisted asset' $false { Test-AssetChecksum $sums $asset 'missing.bin' }
    Expect 'name prefix only' $false { Test-AssetChecksum $sums $asset 'asset' }
} finally {
    Remove-Item -Force $tampered
}

# DER edge cases: r with a sign byte (33 bytes), s shorter than 32.
$r = [byte[]](@(0x00, 0x80) + @(1) * 31)
$s = [byte[]](@(7) * 30)
$der = [byte[]](@(0x30, (4 + $r.Length + $s.Length), 0x02, $r.Length) + $r + @(0x02, $s.Length) + $s)
$p1363 = ConvertFrom-DerSignature $der
Expect 'DER r drops its sign byte' $true { $p1363[0] -eq 0x80 -and $p1363[31] -eq 1 }
Expect 'DER s is left-padded' $true { $p1363[32] -eq 0 -and $p1363[33] -eq 0 -and $p1363[34] -eq 7 -and $p1363[63] -eq 7 }
Expect 'DER with trailing data' $false { ConvertFrom-DerSignature ([byte[]]($der + [byte]0)) }
Expect 'DER r too long' $false {
    ConvertFrom-DerSignature ([byte[]](@(0x30, 39, 0x02, 35) + @(1) * 35 + @(0x02, 0)))
}

if ($script:fails -ne 0) { exit 1 }
