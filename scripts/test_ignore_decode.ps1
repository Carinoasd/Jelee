param([string]$OutputDirectory = (Join-Path (Split-Path $PSScriptRoot -Parent) '.testdata/ignore-decode-oracle'))
$ErrorActionPreference='Stop'
# The pinned wrapper uses File.ReadAllText without an explicit encoding.
# Read bytes through that actual runtime API, including its replacement fallback.
[IO.Directory]::CreateDirectory($OutputDirectory) | Out-Null
$text="*.片`r`n!😀.mkv`n"
$cases=[Collections.Generic.List[object]]::new()
foreach($entry in @(
    @('utf8',[Text.UTF8Encoding]::new($false)),
    @('utf8-bom',[Text.UTF8Encoding]::new($true)),
    @('utf16le',[Text.UnicodeEncoding]::new($false,$true)),
    @('utf16be',[Text.UnicodeEncoding]::new($true,$true)),
    @('utf32le',[Text.UTF32Encoding]::new($false,$true)),
    @('utf32be',[Text.UTF32Encoding]::new($true,$true)))) {
    $bytes=[byte[]]($entry[1].GetPreamble()+$entry[1].GetBytes($text))
    $cases.Add(@{name=$entry[0];hex=[Convert]::ToHexString($bytes)})
}
$hexes=@('','00','EFBBBF','FFFE','FEFF','FFFE0000','0000FEFF','EFBB','FF','80','C0AF','C280','C2','C241','E0A0','E08080','EDA080','ED9FBF','E1A041','F09080','F0808080','F4908080','F48FBFBF','F5808080','F09F9880','FFFE00D8','FFFE00DC','FFFE00D84100','FFFE00D800D8','FFFE00D800DC','FFFE41','FEFFD8000041','FEFFDC00','FFFE000000D80000','FFFE000000001100','FFFE0000410000','0000FEFF00110000','0000FEFF0000D800','0000FEFF000041','EFBBBF00','FFFE0000EFBBBF00')
foreach($hex in $hexes){$cases.Add(@{name="bytes-$hex";hex=$hex})}
$result=@(foreach($case in $cases){
    $path=Join-Path $OutputDirectory ($case.name+'.bin')
    [IO.File]::WriteAllBytes($path,[Convert]::FromHexString($case.hex))
    $decoded=[IO.File]::ReadAllText($path)
    [pscustomobject]@{name=$case.name;rawHex=$case.hex;utf8Hex=[Convert]::ToHexString([Text.Encoding]::UTF8.GetBytes($decoded))}
})
$fixture=[pscustomobject]@{runtime=[Environment]::Version.ToString();cases=$result}
[IO.File]::WriteAllText((Join-Path $OutputDirectory 'decode.json'),($fixture | ConvertTo-Json -Depth 5)+"`n",[Text.UTF8Encoding]::new($false))
Write-Output "ReadAllText oracle: $($result.Count) cases; runtime $([Environment]::Version)"
