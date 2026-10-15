# Install the latest SlopCop release on native Windows.
#
# Usage:
#   irm https://raw.githubusercontent.com/BrokkAi/slopcop/master/install.ps1 | iex
#
# Environment:
#   SLOPCOP_INSTALL_DIR      Install directory. Defaults to %LOCALAPPDATA%\Programs\slopcop.
#   SLOPCOP_VERSION          Optional release tag to install, for example v0.1.0.
#   SLOPCOP_GITHUB_OWNER     GitHub owner to download from. Defaults to BrokkAi.
#   SLOPCOP_GITHUB_API       GitHub API base URL. Defaults to https://api.github.com.
#   SLOPCOP_NO_MODIFY_PATH   Set to leave the user Path unchanged.
#   GITHUB_TOKEN             Optional token for GitHub API rate limits.
#
# The script runs inside a script block so `iex` leaves no variables behind,
# and it reports failures by throwing: `exit` would close the caller's shell.

& {
    Set-StrictMode -Version 3.0
    $ErrorActionPreference = 'Stop'
    # Windows PowerShell 5.1 renders download progress so slowly that it
    # dominates the transfer time.
    $ProgressPreference = 'SilentlyContinue'
    [Net.ServicePointManager]::SecurityProtocol =
        [Net.ServicePointManager]::SecurityProtocol -bor [Net.SecurityProtocolType]::Tls12

    $ScriptVersion = '1.0.0'
    $Repository = 'slopcop'
    # An upgrade leaves the binary it replaced behind until its last user
    # exits, and an interrupted run can leave a staged copy.
    $ReplacedPrefix = '.slopcop-replaced-'
    $StagedPrefix = '.slopcop-install-'

    function Write-Step([string] $Message) {
        Write-Host "slopcop-installer: $Message"
    }

    function Get-Setting([string] $Name, [string] $Default) {
        $value = [Environment]::GetEnvironmentVariable($Name)
        if ([string]::IsNullOrEmpty($value)) { $Default } else { $value }
    }

    function Get-Sha256([string] $Path) {
        # Hash through .NET rather than Get-FileHash: Windows PowerShell started
        # from PowerShell 7 inherits a module path from which the script cmdlets
        # do not load.
        $stream = [IO.File]::OpenRead($Path)
        try {
            $digest = [Security.Cryptography.SHA256]::Create().ComputeHash($stream)
        } finally {
            $stream.Dispose()
        }
        -join ($digest | ForEach-Object { $_.ToString('x2') })
    }

    # Prefer the OS architecture from the registry: an x64 PowerShell emulated
    # on Arm64 Windows reports AMD64 in its environment, with no W6432 hint.
    $architecture = $null
    try {
        $architecture = (Get-ItemProperty -LiteralPath 'HKLM:\SYSTEM\CurrentControlSet\Control\Session Manager\Environment' -Name PROCESSOR_ARCHITECTURE).PROCESSOR_ARCHITECTURE
    } catch {
        $architecture = $null
    }
    if ([string]::IsNullOrEmpty($architecture)) { $architecture = Get-Setting 'PROCESSOR_ARCHITEW6432' '' }
    if ([string]::IsNullOrEmpty($architecture)) { $architecture = Get-Setting 'PROCESSOR_ARCHITECTURE' '' }
    if ($architecture -match '^(?i:AMD64|X86_64)$') {
        $target = 'amd64'
    } elseif ($architecture -match '^(?i:ARM64|AARCH64)$') {
        $target = 'arm64'
    } else {
        throw "slopcop installer: unsupported architecture: $architecture (the Windows release supports amd64 and arm64)"
    }

    $owner = Get-Setting 'SLOPCOP_GITHUB_OWNER' 'BrokkAi'
    $api = (Get-Setting 'SLOPCOP_GITHUB_API' 'https://api.github.com').TrimEnd('/')
    $version = Get-Setting 'SLOPCOP_VERSION' ''
    if ($version -and $version -notmatch '^v') { $version = "v$version" }
    $defaultDir = Join-Path ([Environment]::GetFolderPath('LocalApplicationData')) 'Programs\slopcop'
    $installDir = [IO.Path]::GetFullPath((Get-Setting 'SLOPCOP_INSTALL_DIR' $defaultDir))

    $headers = @{ 'User-Agent' = "slopcop-install-ps1/$ScriptVersion"; 'Accept' = 'application/vnd.github+json' }
    $token = Get-Setting 'GITHUB_TOKEN' ''
    if ($token) { $headers['Authorization'] = "Bearer $token" }

    $endpoint = if ($version) {
        "$api/repos/$owner/$Repository/releases/tags/$version"
    } else {
        "$api/repos/$owner/$Repository/releases/latest"
    }
    $release = Invoke-RestMethod -Uri $endpoint -Headers $headers -UseBasicParsing
    $tag = [string]$release.tag_name
    if (-not $tag) { throw 'slopcop installer: the release has no tag name' }

    $archives = @($release.assets | Where-Object { $_.name -like "slopcop_*_windows_$target.zip" })
    if ($archives.Count -ne 1) {
        $names = ($release.assets | ForEach-Object { $_.name }) -join ', '
        throw "slopcop installer: release $tag has no single windows-$target archive; assets: $names"
    }
    $archive = $archives[0]
    $sums = @($release.assets | Where-Object { $_.name -eq 'checksums.txt' })
    if ($sums.Count -ne 1) {
        throw "slopcop installer: release $tag is missing checksums.txt"
    }

    $work = Join-Path ([IO.Path]::GetTempPath()) ("slopcop-install-" + [Guid]::NewGuid().ToString('N'))
    New-Item -ItemType Directory -Path $work | Out-Null
    try {
        $zip = Join-Path $work $archive.name
        $sumsPath = Join-Path $work 'checksums.txt'
        Write-Step "downloading $($archive.name)"
        Invoke-WebRequest -Uri $archive.browser_download_url -OutFile $zip -UseBasicParsing
        Invoke-WebRequest -Uri $sums[0].browser_download_url -OutFile $sumsPath -UseBasicParsing

        # checksums.txt lists every release file as "<hash>  <name>".
        $expected = $null
        foreach ($line in Get-Content -LiteralPath $sumsPath) {
            $fields = @($line -split '\s+' | Where-Object { $_ })
            if ($fields.Count -ge 2 -and $fields[1].TrimStart('*') -eq $archive.name) {
                $expected = $fields[0].ToLowerInvariant()
            }
        }
        if (-not $expected) { throw "slopcop installer: checksums.txt has no entry for $($archive.name)" }
        if ($expected -notmatch '^[0-9a-f]{64}$') { throw "slopcop installer: the checksum for $($archive.name) is malformed" }
        $actual = Get-Sha256 $zip
        if ($expected -ne $actual) {
            throw "slopcop installer: checksum mismatch for $($archive.name): expected $expected, got $actual"
        }

        $extracted = Join-Path $work 'extracted'
        Add-Type -AssemblyName System.IO.Compression.FileSystem
        [IO.Compression.ZipFile]::ExtractToDirectory($zip, $extracted)
        $binaries = @(Get-ChildItem -LiteralPath $extracted -Recurse -File -Filter 'slopcop.exe')
        if ($binaries.Count -ne 1) {
            throw "slopcop installer: $($archive.name) must contain exactly one slopcop.exe"
        }
        $reported = & $binaries[0].FullName version
        if ($LASTEXITCODE -ne 0) {
            throw "slopcop installer: the downloaded slopcop.exe failed to run (exit code $LASTEXITCODE)"
        }

        New-Item -ItemType Directory -Path $installDir -Force | Out-Null
        Get-ChildItem -LiteralPath $installDir -Force |
            Where-Object { $_.Name.StartsWith($ReplacedPrefix) -or $_.Name.StartsWith($StagedPrefix) } |
            Remove-Item -Force -ErrorAction SilentlyContinue

        # Windows refuses to overwrite a running executable but lets it be
        # renamed, so the installed binary moves aside before its replacement
        # moves in. Staging in the install directory keeps both renames on one
        # volume.
        $nonce = [Guid]::NewGuid().ToString('N')
        $destination = Join-Path $installDir 'slopcop.exe'
        $staged = Join-Path $installDir "$StagedPrefix$nonce"
        Copy-Item -LiteralPath $binaries[0].FullName -Destination $staged
        if (Test-Path -LiteralPath $destination) {
            $aside = Join-Path $installDir "$ReplacedPrefix$nonce"
            Move-Item -LiteralPath $destination -Destination $aside
            try {
                Move-Item -LiteralPath $staged -Destination $destination
            } catch {
                Move-Item -LiteralPath $aside -Destination $destination
                Remove-Item -LiteralPath $staged -Force -ErrorAction SilentlyContinue
                throw
            }
            Remove-Item -LiteralPath $aside -Force -ErrorAction SilentlyContinue
        } else {
            Move-Item -LiteralPath $staged -Destination $destination
        }
        Write-Step "installed $reported to $installDir"
    } finally {
        Remove-Item -LiteralPath $work -Recurse -Force -ErrorAction SilentlyContinue
    }

    if (Get-Setting 'SLOPCOP_NO_MODIFY_PATH' '') { return }
    $environment = Get-Item -Path 'HKCU:\Environment'
    # Keep entries such as %USERPROFILE%\bin unexpanded when writing Path back.
    $userPath = $environment.GetValue('Path', '', 'DoNotExpandEnvironmentNames')
    $entries = @($userPath -split ';' | Where-Object { $_ })
    $present = $entries | Where-Object {
        [Environment]::ExpandEnvironmentVariables($_).TrimEnd('\') -ieq $installDir.TrimEnd('\')
    }
    if (-not $present) {
        $kind = if ($userPath -match '%') { 'ExpandString' } else { 'String' }
        New-ItemProperty -Path 'HKCU:\Environment' -Name 'Path' -Value (($entries + $installDir) -join ';') `
            -PropertyType $kind -Force | Out-Null
        # Setting any user variable through .NET broadcasts the environment
        # change, so terminals opened from now on see the new Path.
        [Environment]::SetEnvironmentVariable('SLOPCOP_INSTALLER_REFRESH', '1', 'User')
        [Environment]::SetEnvironmentVariable('SLOPCOP_INSTALLER_REFRESH', $null, 'User')
        $env:Path = "$env:Path;$installDir"
        Write-Step "added $installDir to your user Path; open a new terminal, then run slopcop"
    } else {
        Write-Step 'run slopcop to start'
    }
}
