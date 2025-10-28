#Requires -Version 5.1

param (
    [string]$SourceRootPath = $PSScriptRoot,  # The root directory to scan
    [string]$OutputFolderName = "TXTport"    # General purpose output folder name
)

# Configure output directory and file paths
$OutputBaseDir = Join-Path -Path (Resolve-Path -LiteralPath $SourceRootPath).ProviderPath -ChildPath $OutputFolderName
$OutputFilePath = Join-Path -Path $OutputBaseDir -ChildPath "all_files_combined.txt"

# Directories and files to exclude
$ExcludedDirNames = @(
    "$OutputFolderName", '.git', '.godot', '.idea', '.svn', '.hg', '.vscode', '.vs', 'node_modules', 'bower_components', 'vendor', 'pycache', '.pytest_cache', '.mypy_cache', '.tox', '.venv', 'venv', 'env', '.env', 'build', 'dist', 'bin', 'obj', 'target', 'out', 'log', 'logs', '.cache', 'temp', 'tmp', 'addons'
)
$ExcludedFileExtensions = @(
    '.exe', '.dll', '.so', '.dylib', '.jar', '.class', '.pyc', '.pyo', '.o', '.a', '.lib', '.xml',
    '.zip', '.tar', '.gz', '.bz2', '.xz', '.rar', '.7z', '.cab', '.iso', '.pck',
    '.png', '.jpg', '.jpeg', '.gif', '.bmp', '.tiff', '.webp', '.svg', '.ico', '.psd', '.ai',
    '.mp3', '.wav', '.ogg', '.aac', '.flac', '.m4a',
    '.mp4', '.mkv', '.mov', '.avi', '.webm', '.flv',
    '.ttf', '.otf', '.woff', '.woff2', '.eot',
    '.pdf', '.doc', '.docx', '.xls', '.xlsx', '.ppt', '.pptx', '.odt', '.ods', '.odp',
    '.db', '.sqlite', '.mdb', '.accdb', '.bak',
    '.lock', '.DS_Store', '.swp', '.swo', '.tmp', '.temp', '.import', '.uid', '.tres', '.git', '.gitingore', '.gitattributes', '.sh', '.editor', '.htm', '.godot', '.gdshader', '.theme', '.gd'
)
$ExcludedFilePathPatterns = @()

# Binary check thresholds
$BinaryCheckPeekBytes = 1024
$BinaryCheckNullThresholdPercent = 10
$BinaryCheckNonPrintableThresholdPercent = 20

# Function: Test if a file is likely binary
function Test-IsLikelyBinary {
    param([string]$FilePath, [int]$PeekBytes = $BinaryCheckPeekBytes, [int]$NullThresholdPercent = $BinaryCheckNullThresholdPercent, [int]$NonPrintableThresholdPercent = $BinaryCheckNonPrintableThresholdPercent)

    try {
        $fileStream = New-Object System.IO.FileStream($FilePath, [System.IO.FileMode]::Open, [System.IO.FileAccess]::Read, [System.IO.FileShare]::ReadWrite)
        $buffer = New-Object byte[] $PeekBytes
        $bytesRead = $fileStream.Read($buffer, 0, $PeekBytes)
        $fileStream.Close()
        $fileStream.Dispose()

        if ($bytesRead -eq 0) { return $false }
        $actualBuffer = $buffer[0..($bytesRead - 1)]

        $nullCount, $nonPrintableCount = 0, 0

        foreach ($byteVal in $actualBuffer) {
            if ($byteVal -eq 0) { $nullCount++ }
            elseif (($byteVal -lt 32 -and $byteVal -notin 9,10,13) -or $byteVal -eq 127) { $nonPrintableCount++ }
        }

        if (($nullCount * 100 / $bytesRead) -gt $NullThresholdPercent) { return $true }
        if (($nonPrintableCount * 100 / $bytesRead) -gt $NonPrintableThresholdPercent) { return $true }
        return $false
    } catch {
        return $true
    }
}

# Initialize Output Directory
if (Test-Path $OutputBaseDir) {
    Write-Warning "Output directory '$OutputBaseDir' already exists. Clearing it."
    try { Get-ChildItem -Path $OutputBaseDir -Recurse | Remove-Item -Recurse -Force -ErrorAction Stop }
    catch { Write-Error "Failed to clear output directory '$OutputBaseDir': $_"; exit 1 }
} else {
    New-Item -ItemType Directory -Path $OutputBaseDir -Force -ErrorAction Stop | Out-Null
}

Write-Host "Starting Universal File to .txt Conversion..."
Write-Host "Source Root: $SourceRootPath"
Write-Host "Output will be saved to: $OutputBaseDir"
Write-Host "Scanning project files (this might take a while for large projects)..."

# Get all files recursively
$AllFiles = Get-ChildItem -Path $SourceRootPath -Recurse -File -Force -ErrorAction SilentlyContinue

$ScriptStartTime = Get-Date
$OutputContent = [System.Collections.Generic.List[string]]::new()

foreach ($file in $AllFiles) {
    $RelativePath = $file.FullName.Substring($SourceRootPath.Length).TrimStart([System.IO.Path]::DirectorySeparatorChar)

    # Check if in an excluded directory
    $parentDirToCheck = $file.DirectoryName
    $isDirExcluded = $false
    while ($parentDirToCheck.Length -ge $SourceRootPath.Length) {
        if ($ExcludedDirNames -icontains (Split-Path -Path $parentDirToCheck -Leaf)) {
            $isDirExcluded = $true; break
        }
        if ($parentDirToCheck -eq $SourceRootPath.TrimEnd([System.IO.Path]::DirectorySeparatorChar)) { break }
        $parentDirToCheck = Split-Path -Path $parentDirToCheck -Parent
    }

    if ($isDirExcluded) {
        continue
    }

    # Check if extension is excluded
    if ($ExcludedFileExtensions -icontains $file.Extension.ToLowerInvariant()) {
        continue
    }

    # Check if path matches an excluded pattern
    if ($ExcludedFilePathPatterns) {
        $pathMatchesPattern = $false
        foreach ($pattern in $ExcludedFilePathPatterns) {
            if ($file.FullName -match $pattern) {
                $pathMatchesPattern = $true; break
            }
        }
        if ($pathMatchesPattern) {
            continue
        }
    }

    # Get relative path
    $RelativePath = $file.FullName.Substring($SourceRootPath.Length).TrimStart([System.IO.Path]::DirectorySeparatorChar)
    $isLikelyBinary = Test-IsLikelyBinary -FilePath $file.FullName

    if ($isLikelyBinary) {
        $OutputContent.Add("[B] $RelativePath")
        continue
    }

    # If not binary, attempt to convert
    try {
        $fileContent = Get-Content -Path $file.FullName -Raw -Encoding Default -ErrorAction Stop
        $OutputContent.Add("[T] $RelativePath")
        $OutputContent.Add("Relative Path: $RelativePath")
        $OutputContent.Add("Last Modified: $($file.LastWriteTimeUtc.ToString('o'))")
        $OutputContent.Add("====")
        $OutputContent.Add("")
        $OutputContent.Add($fileContent)
        $OutputContent.Add("")  # Separator between files
    } catch {
        $OutputContent.Add("[E] $RelativePath")
        $OutputContent.Add("   [!] ERROR processing: $_")
        $OutputContent.Add("")  # Separator between files
    }
}

# Write combined file
$OutputContent | Set-Content -Path $OutputFilePath -Encoding UTF8 -Force -ErrorAction Stop
$ScriptEndTime = Get-Date
$Duration = $ScriptEndTime - $ScriptStartTime

Write-Host "Conversion Complete!"
Write-Host "Total files scanned: $($AllFiles.Count)"
Write-Host "Structure map and converted files are in: $OutputFilePath"
Write-Host "Script execution time: $($Duration.ToString())"
