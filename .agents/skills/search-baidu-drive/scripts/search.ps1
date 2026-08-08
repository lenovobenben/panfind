[CmdletBinding()]
param(
    [string]$Root = 'baidu:/',

    [string]$Account,

    [ValidateSet('any', 'file', 'directory')]
    [string]$Kind = 'any',

    [string[]]$NameContains = @(),

    [string[]]$NameAny = @(),

    [string[]]$NamePattern = @(),

    [string[]]$Extensions = @(),

    [string[]]$PathContains = @(),

    [string]$LargerThan,

    [string]$SmallerThan,

    [string]$ModifiedAfter,

    [string]$ModifiedBefore,

    [ValidateRange(0, 2147483647)]
    [int]$MinDepth,

    [ValidateRange(0, 2147483647)]
    [int]$MaxDepth,

    [ValidateRange(1, 200)]
    [int]$Limit = 20,

    [switch]$CaseSensitive,

    [string]$PanFindPath
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

function Resolve-PanFindExecutable {
    param(
        [string]$ExplicitPath,
        [string]$RepositoryRoot
    )

    if ($ExplicitPath) {
        $resolvedExplicit = $ExecutionContext.SessionState.Path.GetUnresolvedProviderPathFromPSPath($ExplicitPath)
        if (-not (Test-Path -LiteralPath $resolvedExplicit -PathType Leaf)) {
            throw "PanFind executable does not exist: $resolvedExplicit"
        }
        return $resolvedExplicit
    }

    $candidates = @(
        (Join-Path $RepositoryRoot 'panfind-windows-amd64.exe'),
        (Join-Path $RepositoryRoot 'panfind.exe'),
        (Join-Path $RepositoryRoot 'bin\panfind.exe'),
        (Join-Path $RepositoryRoot 'dist\panfind-windows-amd64.exe')
    )

    foreach ($candidate in $candidates) {
        if (Test-Path -LiteralPath $candidate -PathType Leaf) {
            return [IO.Path]::GetFullPath($candidate)
        }
    }

    $command = Get-Command panfind.exe, panfind -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($command) {
        return $command.Source
    }

    throw 'PanFind was not found. Download panfind-windows-amd64.exe to the repository root or build panfind.exe first.'
}

function Assert-TextValues {
    param(
        [string]$ParameterName,
        [string[]]$Values
    )

    foreach ($value in $Values) {
        if ([string]::IsNullOrWhiteSpace($value)) {
            throw "$ParameterName cannot contain an empty value."
        }
        if ($value.Contains([char]0)) {
            throw "$ParameterName cannot contain a NUL character."
        }
    }
}

function Add-OrPredicates {
    param(
        [System.Collections.Generic.List[string]]$Arguments,
        [string]$Predicate,
        [string[]]$Patterns
    )

    if ($Patterns.Count -eq 0) {
        return
    }

    if ($Patterns.Count -gt 1) {
        $Arguments.Add('(')
    }

    for ($index = 0; $index -lt $Patterns.Count; $index++) {
        if ($index -gt 0) {
            $Arguments.Add('-o')
        }
        $Arguments.Add($Predicate)
        $Arguments.Add($Patterns[$index])
    }

    if ($Patterns.Count -gt 1) {
        $Arguments.Add(')')
    }
}

function Format-ByteSize {
    param([long]$Bytes)

    $units = @('B', 'KiB', 'MiB', 'GiB', 'TiB')
    $value = [double]$Bytes
    $unitIndex = 0
    while ($value -ge 1024 -and $unitIndex -lt $units.Count - 1) {
        $value /= 1024
        $unitIndex++
    }

    if ($unitIndex -eq 0) {
        return "$Bytes B"
    }
    return ('{0:0.##} {1}' -f $value, $units[$unitIndex])
}

function Get-BaiduLocation {
    param(
        [string]$Path,
        [string]$Type
    )

    $lastSlash = $Path.LastIndexOf('/')
    $name = if ($lastSlash -ge 0) { $Path.Substring($lastSlash + 1) } else { $Path }
    if ([string]::IsNullOrEmpty($name) -and $Path -eq 'baidu:/') {
        $name = 'baidu:/'
    }

    if ($Type -eq 'directory') {
        $directoryPath = $Path
    }
    elseif ($lastSlash -le 'baidu:'.Length) {
        $directoryPath = 'baidu:/'
    }
    else {
        $directoryPath = $Path.Substring(0, $lastSlash)
    }

    $cloudDirectory = $directoryPath.Substring('baidu:'.Length)
    if (-not $cloudDirectory.StartsWith('/')) {
        $cloudDirectory = '/' + $cloudDirectory
    }
    $encodedDirectory = [Uri]::EscapeDataString($cloudDirectory)

    [pscustomobject]@{
        name = $name
        parent_path = $directoryPath
        web_url = "https://pan.baidu.com/disk/main#/index?category=all&path=$encodedDirectory"
    }
}

if ($Root -notmatch '^baidu:/') {
    throw "Root must start with baidu:/: $Root"
}

Assert-TextValues -ParameterName 'NameContains' -Values $NameContains
Assert-TextValues -ParameterName 'NameAny' -Values $NameAny
Assert-TextValues -ParameterName 'NamePattern' -Values $NamePattern
Assert-TextValues -ParameterName 'Extensions' -Values $Extensions
Assert-TextValues -ParameterName 'PathContains' -Values $PathContains

$sizePattern = '^\d+[cwbkMG]$'
if ($LargerThan -and $LargerThan -notmatch $sizePattern) {
    throw "LargerThan must be an integer followed by c, w, b, k, M, or G: $LargerThan"
}
if ($SmallerThan -and $SmallerThan -notmatch $sizePattern) {
    throw "SmallerThan must be an integer followed by c, w, b, k, M, or G: $SmallerThan"
}
if ($ModifiedAfter -and $ModifiedAfter -notmatch '^\d{4}-\d{2}-\d{2}$') {
    throw "ModifiedAfter must use YYYY-MM-DD: $ModifiedAfter"
}
if ($ModifiedBefore -and $ModifiedBefore -notmatch '^\d{4}-\d{2}-\d{2}$') {
    throw "ModifiedBefore must use YYYY-MM-DD: $ModifiedBefore"
}
if ($PSBoundParameters.ContainsKey('MinDepth') -and $PSBoundParameters.ContainsKey('MaxDepth') -and $MinDepth -gt $MaxDepth) {
    throw 'MinDepth cannot exceed MaxDepth.'
}

$repoRoot = [IO.Path]::GetFullPath((Join-Path $PSScriptRoot '..\..\..\..'))
$panfind = Resolve-PanFindExecutable -ExplicitPath $PanFindPath -RepositoryRoot $repoRoot
$arguments = [System.Collections.Generic.List[string]]::new()
$arguments.Add('query')
$arguments.Add($Root)

if ($Account) {
    $arguments.Add('--account')
    $arguments.Add($Account)
}
if ($PSBoundParameters.ContainsKey('MinDepth')) {
    $arguments.Add('-mindepth')
    $arguments.Add($MinDepth.ToString([Globalization.CultureInfo]::InvariantCulture))
}
if ($PSBoundParameters.ContainsKey('MaxDepth')) {
    $arguments.Add('-maxdepth')
    $arguments.Add($MaxDepth.ToString([Globalization.CultureInfo]::InvariantCulture))
}

if ($Kind -eq 'file') {
    $arguments.Add('-type')
    $arguments.Add('f')
}
elseif ($Kind -eq 'directory') {
    $arguments.Add('-type')
    $arguments.Add('d')
}

$namePredicate = if ($CaseSensitive) { '-name' } else { '-iname' }
$pathPredicate = if ($CaseSensitive) { '-path' } else { '-ipath' }

foreach ($substring in $NameContains) {
    $arguments.Add($namePredicate)
    $arguments.Add("*$substring*")
}

$nameAnyPatterns = @($NameAny | ForEach-Object { "*$_*" })
Add-OrPredicates -Arguments $arguments -Predicate $namePredicate -Patterns $nameAnyPatterns
Add-OrPredicates -Arguments $arguments -Predicate $namePredicate -Patterns $NamePattern

$extensionPatterns = @($Extensions | ForEach-Object {
    $extension = $_.Trim().TrimStart('.')
    if ($extension -notmatch '^[A-Za-z0-9]+$') {
        throw "Extension must contain only letters and digits: $_"
    }
    "*.$extension"
})
Add-OrPredicates -Arguments $arguments -Predicate $namePredicate -Patterns $extensionPatterns

foreach ($substring in $PathContains) {
    $arguments.Add($pathPredicate)
    $arguments.Add("*$substring*")
}
if ($LargerThan) {
    $arguments.Add('-size')
    $arguments.Add("+$LargerThan")
}
if ($SmallerThan) {
    $arguments.Add('-size')
    $arguments.Add("-$SmallerThan")
}
if ($ModifiedAfter) {
    $arguments.Add('-newermt')
    $arguments.Add($ModifiedAfter)
}
if ($ModifiedBefore) {
    $arguments.Add('!')
    $arguments.Add('-newermt')
    $arguments.Add($ModifiedBefore)
}
$arguments.Add('--json')

$previousErrorAction = $ErrorActionPreference
$ErrorActionPreference = 'Continue'
$rawOutput = @(& $panfind @arguments 2>&1)
$exitCode = $LASTEXITCODE
$ErrorActionPreference = $previousErrorAction

if ($exitCode -notin 0, 1) {
    $message = ($rawOutput | ForEach-Object { $_.ToString() }) -join [Environment]::NewLine
    throw "PanFind query failed with exit code $exitCode. $message"
}

$items = @(
    $rawOutput |
        ForEach-Object { $_.ToString() } |
        Where-Object { -not [string]::IsNullOrWhiteSpace($_) } |
        ForEach-Object { $_ | ConvertFrom-Json }
)

$returnedItems = @($items | Select-Object -First $Limit)
$matchedSizeBytes = 0L
foreach ($item in $items) {
    if ($item.PSObject.Properties['size']) {
        $matchedSizeBytes += [long]$item.size
    }
}
$results = @(
    foreach ($item in $returnedItems) {
        $location = Get-BaiduLocation -Path $item.path -Type $item.type
        $sizeBytes = if ($item.PSObject.Properties['size']) { [long]$item.size } else { 0L }
        $modifiedAt = if ($item.PSObject.Properties['modified_at']) { $item.modified_at } else { $null }
        $result = [ordered]@{
            name = $location.name
            type = $item.type
            size_bytes = $sizeBytes
            size_human = Format-ByteSize -Bytes $sizeBytes
            modified_at = $modifiedAt
            path = $item.path
            parent_path = $location.parent_path
            web_url = $location.web_url
            web_url_kind = 'experimental-directory-route'
        }
        if ($item.PSObject.Properties['hash'] -and -not [string]::IsNullOrWhiteSpace([string]$item.hash)) {
            $result.hash = [string]$item.hash
        }
        $result
    }
)

[ordered]@{
    provider = 'baidu-local'
    executable = $panfind
    query = @($arguments)
    total = $items.Count
    matched_size_bytes = $matchedSizeBytes
    matched_size_human = Format-ByteSize -Bytes $matchedSizeBytes
    returned = $results.Count
    truncated = $items.Count -gt $results.Count
    location_note = 'web_url opens an undocumented Baidu parent-directory route; use path as the authoritative location.'
    results = $results
} | ConvertTo-Json -Depth 6
