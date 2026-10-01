param([string]$SourceDirectory = (Join-Path (Split-Path $PSScriptRoot -Parent) '.testdata/ignore-upstream'))
$ErrorActionPreference='Stop'
$files=@('Ignore.cs','IgnoreRule.cs','Replacer.cs','ReplacerStash.cs')
$expected=@('ef137183a96b22f3eb5e3d4eae8d64c1638c5b51','418a00bfbe47c1e66fb59c922fbc52d254de5f50','49c90cedfc876d879e4a5557a4390556a7fd426e','dad880564b10c1226f1bb7950abdab7ceb908241')
$paths=@()
for($i=0;$i -lt $files.Count;$i++) {
    $path=Join-Path $SourceDirectory $files[$i]
    $hash=(& git hash-object --no-filters -- $path).Trim()
    if($LASTEXITCODE -ne 0 -or $hash -ne $expected[$i]) { throw 'Fixed source hash mismatch' }
    $paths += $path
}
Add-Type -Path $paths
$cases=@'
[
 {"name":"empty-library","rules":[],"path":"/media/a.mkv"},
 {"name":"comment-only","rules":["# comment"],"path":"/media/a.mkv"},
 {"name":"case-fold","rules":["*.MKV"],"path":"/media/a.mkv"},
 {"name":"anchored-full-path","rules":["/a.mkv"],"path":"/media/a.mkv"},
 {"name":"anchored-relative-path","rules":["/a.mkv"],"path":"a.mkv"},
 {"name":"middle-slash-full-path","rules":["folder/a.mkv"],"path":"/media/folder/a.mkv"},
 {"name":"middle-slash-relative-path","rules":["folder/a.mkv"],"path":"folder/a.mkv"},
 {"name":"directory-child","rules":["folder/"],"path":"/media/folder/a.mkv"},
 {"name":"negate-child-direct","rules":["folder/","!**/folder/a.mkv"],"path":"/media/folder/a.mkv"},
 {"name":"group-metacharacters","rules":["(foo|bar).mkv"],"path":"/media/foo.mkv"},
 {"name":"invalid-regex","rules":["["],"path":"/media/a.mkv"},
 {"name":"escaped-hash","rules":["\\#a.mkv"],"path":"/media/#a.mkv"},
 {"name":"leading-star-directory","rules":["*a.mkv"],"path":"/media/sub/a.mkv"}
]
'@ | ConvertFrom-Json
$patterns=@('a/**/b','**/a','a/**','a/**/**/b','foo***bar','foo/','/foo/','a?b','a\?b','foo\ ','foo   ','a\ b','[a-z].mkv','[!a].mkv','**/*.mkv','/media/**/a.mkv','!a.mkv','\!a.mkv','.','a+b','a{2}','(foo|bar)','foo/**.mkv','***')
$pathsToCheck=@('a/b','a/x/b','foo/','/media/a.mkv','/media/sub/a.mkv','/media/foo.mkv','a b','foo ')
$sequence=0
foreach($pattern in $patterns) {
    foreach($path in $pathsToCheck) {
        $sequence++
        $cases += [pscustomobject]@{name="generated-$sequence";rules=@($pattern);path=$path}
    }
}
$standardCount=$cases.Count
$cases += @'
[
 {"name":"numeric-backreference","rules":["(a)\\2"],"path":"/media/aa"},
 {"name":"unicode-digit","rules":["\\d.mkv"],"path":"/media/٣.mkv"},
 {"name":"unicode-word","rules":["\\w.mkv"],"path":"/media/片.mkv"},
 {"name":"unicode-space","rules":["a\\sb"],"path":"/media/a　b"},
 {"name":"unicode-escape","rules":["\\u0061.mkv"],"path":"/media/a.mkv"},
 {"name":"unicode-sigma","rules":["σ.mkv"],"path":"/media/ς.mkv"},
 {"name":"supplementary-question","rules":["?.mkv"],"path":"/media/😀.mkv"},
 {"name":"supplementary-double-question","rules":["??.mkv"],"path":"/media/😀.mkv"},
 {"name":"unicode-property","rules":["\\p{L}.mkv"],"path":"/media/片.mkv"},
 {"name":"unknown-escape","rules":["\\q.mkv"],"path":"/media/q.mkv"}
]
'@ | ConvertFrom-Json
# Keep engine-sensitive cases separate from the RE2-compatible translation matrix.
$foldPairs=@(@('σ','ς'),@('Σ','σ'),@('s','ſ'),@('k','K'),@('i','İ'),@('i','ı'),@('ß','ẞ'))
foreach($pair in $foldPairs) {
    foreach($form in @('literal','class','negated-class')) {
        $pattern=switch($form) { 'literal' {$pair[0]} 'class' {'['+$pair[0]+']'} 'negated-class' {'[^'+$pair[0]+']'} }
        $cases += [pscustomobject]@{name="fold-$form-$($pair[0])-$($pair[1])";rules=@($pattern+'.mkv');path='/media/'+$pair[1]+'.mkv'}
    }
}
$cases += @'
[
 {"name":"pcre-quote","rules":["\\Qa\\E.mkv"],"path":"/media/a.mkv"},
 {"name":"pcre-linebreak","rules":["a\\Rb"],"path":"/media/a\nb"},
 {"name":"pcre-grapheme","rules":["\\X.mkv"],"path":"/media/a.mkv"},
 {"name":"supplementary-literal","rules":["😀.mkv"],"path":"/media/😀.mkv"},
 {"name":"supplementary-class","rules":["[😀].mkv"],"path":"/media/😀.mkv"}
]
'@ | ConvertFrom-Json
$cases += @'
[
 {"name":"supplementary-escaped","rules":["\\😀.mkv"],"path":"/media/😀.mkv"},
 {"name":"supplementary-escaped-class","rules":["[\\😀].mkv"],"path":"/media/😀.mkv"},
 {"name":"supplementary-double-escaped","rules":["\\\\😀.mkv"],"path":"/media/\\😀.mkv"},
 {"name":"supplementary-range","rules":["[😀-😁].mkv"],"path":"/media/😀.mkv"},
 {"name":"supplementary-negated-class","rules":["[^😀].mkv"],"path":"/media/😁.mkv"},
 {"name":"supplementary-quantifier","rules":["😀{2}.mkv"],"path":"/media/😀😀.mkv"},
 {"name":"supplementary-group","rules":["(😀){2}.mkv"],"path":"/media/😀😀.mkv"},
 {"name":"supplementary-surrogate-escapes","rules":["\\uD83D\\uDE00.mkv"],"path":"/media/😀.mkv"},
 {"name":"supplementary-group-name","rules":["(?<😀>a).mkv"],"path":"/media/a.mkv"},
 {"name":"supplementary-category","rules":["\\p{😀}.mkv"],"path":"/media/😀.mkv"},
 {"name":"supplementary-backreference","rules":["(😀)\\2.mkv"],"path":"/media/😀😀.mkv"},
 {"name":"supplementary-deseret-fold","rules":["𐐀.mkv"],"path":"/media/𐐨.mkv"}
]
'@ | ConvertFrom-Json
$results=@(foreach($case in $cases) {
    $matcher=[Ignore.Ignore]::new()
    $errors=@()
    foreach($rule in $case.rules) {
        try { [void]$matcher.Add([string]$rule) }
        catch { $errors += $_.Exception.GetBaseException().GetType().FullName }
    }
    $flags=[Reflection.BindingFlags]'Instance,NonPublic'
    $compiled=@(foreach($rule in [Ignore.Ignore].GetField('rules',$flags).GetValue($matcher)) {
        $regex=[Ignore.IgnoreRule].GetField('parsedRegex',$flags).GetValue($rule)
        [ordered]@{negative=$rule.Negate;regex=if($null -eq $regex){$null}else{$regex.ToString()}}
    })
    [ordered]@{name=$case.name;rules=$case.rules;path=$case.path;errors=$errors;compiled=$compiled;ignored=$matcher.IsIgnored($case.path)}
})
$report=[ordered]@{repository='goelhardik/ignore';commit='f7c6f07d66d0e1043d901a2ab2f58daca1862066';package='Ignore 0.2.1';runtime=[System.Runtime.InteropServices.RuntimeInformation]::FrameworkDescription;culture=[Globalization.CultureInfo]::CurrentCulture.Name;compilation='Unmodified sources, NET8_0_OR_GREATER undefined; runtime Regex branch';cases=@($results | Select-Object -First $standardCount);engineCases=@($results | Select-Object -Skip $standardCount)}
$json=$report | ConvertTo-Json -Depth 10
[IO.File]::WriteAllText((Join-Path $SourceDirectory 'semantics.json'),$json+"`n")
$json
