[CmdletBinding()]
param(
    [string] $Path = (Join-Path $PSScriptRoot 'ShareMe.unsigned.shortcut'),
    [switch] $RequireBuildMetadata
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Plist.ps1')
$script:checks = 0
function Assert-Contract {
    param([bool] $Condition, [string] $Message)
    if (-not $Condition) { throw "Shortcut contract: $Message" }
    $script:checks++
}
function Reference-Name {
    param([System.Xml.XmlNode] $Reference)
    $name = Get-PlistText $Reference 'OutputName'
    if (-not $name) { $name = Get-PlistText $Reference 'VariableName' }
    return $name
}
function Assert-Input {
    param([System.Xml.XmlNode] $Parameters, [string] $Key, [string] $Name, [string] $Type = 'ActionOutput')
    $node = Get-PlistNode $Parameters $Key
    Assert-Contract ($null -ne $node -and $node.Name -eq 'dict') "$Key must be an explicit typed input, never implicit or interpolated text"
    Assert-Contract ((Get-PlistText $node 'WFSerializationType') -ceq 'WFTextTokenAttachment') "$Key uses native variable-picker attachment"
    $ref = Get-PlistNode $node 'Value'
    Assert-Contract ((Reference-Name $ref) -ceq $Name) "$Key references $Name"
    Assert-Contract ((Get-PlistText $ref 'Type') -ceq $Type) "$Key reference type $Type"
    Assert-Contract ($null -eq (Get-PlistNode $ref 'Aggrandizements')) "$Key must not coerce $Name"
}
function Token-Template {
    param([System.Xml.XmlNode] $Node)
    if ($Node.Name -eq 'string') { return $Node.InnerText }
    $text = Get-TokenString $Node
    $value = Get-PlistNode $Node 'Value'
    $attachments = Get-PlistNode $value 'attachmentsByRange'
    if ($null -eq $attachments) { return $text }
    $ranges = @($attachments.SelectNodes('key') | ForEach-Object {
        Assert-Contract ($_.InnerText -cmatch '^\{(\d+), 1\}$') 'One-character attachment range'
        [pscustomobject]@{ Start = [int]$Matches[1]; Reference = $_.NextSibling }
    } | Sort-Object Start -Descending)
    foreach ($range in $ranges) {
        Assert-Contract ($range.Start -lt $text.Length -and $text[$range.Start] -eq [char]0xfffc) 'Attachment range points to an object placeholder'
        $name = Reference-Name $range.Reference
        Assert-Contract (-not [string]::IsNullOrEmpty($name)) 'Named token attachment'
        $text = $text.Remove($range.Start, 1).Insert($range.Start, "{$name}")
    }
    Assert-Contract (-not $text.Contains([char]0xfffc)) 'Every text placeholder has a typed attachment'
    return $text
}
function Named {
    param([string] $Name)
    Assert-Contract ($named.ContainsKey($Name)) "Action output exists: $Name"
    return $named[$Name]
}
function Terms {
    param($Action)
    $compound = Get-PlistNode $Action.P 'WFConditions'
    if ($null -eq $compound) { return ,$Action.P }
    $templates = Get-PlistNode (Get-PlistNode $compound 'Value') 'WFActionParameterFilterTemplates'
    return @($templates.SelectNodes('dict'))
}
function Term-Name {
    param([System.Xml.XmlNode] $Term)
    $inputNode = Get-PlistNode $Term 'WFInput'
    $attachment = Get-PlistNode $inputNode 'Variable'
    return Reference-Name (Get-PlistNode $attachment 'Value')
}
function Term-Value {
    param([System.Xml.XmlNode] $Term)
    $value = Get-PlistNode $Term 'WFConditionalActionString'
    if ($null -eq $value) { $value = Get-PlistNode $Term 'WFNumberValue' }
    if ($null -eq $value) { return '' }
    if ($value.Name -eq 'dict') { return Token-Template $value }
    return $value.InnerText
}
function Is-Term {
    param([System.Xml.XmlNode] $Term, [string] $Name, [string] $Condition, [string] $Value)
    return ((Term-Name $Term) -ceq $Name -and (Get-PlistText $Term 'WFCondition') -ceq $Condition -and (Term-Value $Term) -ceq $Value)
}
function Find-Guard {
    param([string] $Name, [string] $Condition, [string] $Value = '', [int] $Before = $actions.Count)
    $found = @($actions | Where-Object {
        if ($_.Id -ne 'conditional' -or $_.Mode -ne '0' -or $_.Index -ge $Before) { return $false }
        return @((Terms $_) | Where-Object { Is-Term $_ $Name $Condition $Value }).Count -gt 0
    })
    Assert-Contract ($found.Count -gt 0) "Guard $Name $Condition '$Value' exists before action $Before"
    return $found[-1]
}
function Assert-Stops {
    param($Guard)
    Assert-Contract ($actions[$Guard.Index + 1].Id -ceq 'alert') 'Guard immediately explains failure'
    Assert-Contract ($actions[$Guard.Index + 2].Id -ceq 'exit') 'Guard immediately stops on failure'
}
function Assert-EndStops {
    param($Guard)
    $last = $groups[$Guard.Group].End - 1
    while ($actions[$last].Id -ceq 'nothing') { $last-- }
    Assert-Contract ($actions[$last].Id -ceq 'exit') 'Branch cannot fall through into transfer'
}
function Assert-Logic {
    param($Guard, [string] $Prefix, [int] $Count)
    $compound = Get-PlistNode (Get-PlistNode $Guard.P 'WFConditions') 'Value'
    Assert-Contract ((Get-PlistText $compound 'WFActionParameterFilterPrefix') -ceq $Prefix -and @(Terms $Guard).Count -eq $Count) 'Exact Any/All semantics and predicate count'
}
function Assert-ImmediateGuard {
    param([string] $Name, [string] $Condition, [string] $Value = '')
    $lookup = Named $Name
    $guard = Find-Guard $Name $Condition $Value ($lookup.Index + 2)
    Assert-Contract ($guard.Index -eq $lookup.Index + 1) "$Name is immediately checked"
    Assert-Stops $guard
    return $guard
}
function Assert-Ancestor {
    param($Action, $Guard, [string] $Branch = '0')
    Assert-Contract ($Action.Ancestors -contains "$($Guard.Group):$Branch") "$($Action.Id) is gated by branch $Branch of action $($Guard.Index)"
}
function Assert-Fields {
    param([string] $Name, [hashtable] $Expected)
    $action = Named $Name
    Assert-Contract ($action.Id -ceq 'dictionary') "$Name is a native dictionary (JSON escaping, not concatenation)"
    $fields = @(Get-FieldItems (Get-PlistNode $action.P 'WFItems'))
    $names = @($fields | ForEach-Object { Get-FieldName $_ } | Sort-Object)
    Assert-Contract (($names -join '|') -ceq (($Expected.Keys | Sort-Object) -join '|')) "$Name has exact allowlisted fields"
    foreach ($field in $fields) {
        $key = Get-FieldName $field
        $type = if ($Expected[$key] -is [int]) { '3' } else { '0' }
        Assert-Contract ((Get-PlistText $field 'WFItemType') -ceq $type) "$Name.$key has the correct native JSON type"
        Assert-Contract ((Token-Template (Get-PlistNode $field 'WFValue')) -ceq [string]$Expected[$key]) "$Name.$key exact value/source"
    }
}

$document = Read-ShortcutPlist $Path
$root = $document.SelectSingleNode('/plist/dict')
$raw = [IO.File]::ReadAllText((Resolve-Path -LiteralPath $Path))
$nodes = @((Get-PlistNode $root 'WFWorkflowActions').SelectNodes('dict'))
Assert-Contract ($nodes.Count -gt 0) 'Nonempty compiled graph'
Assert-Contract ((Get-PlistNode $root 'WFWorkflowTypes').InnerText -ceq 'ActionExtension') 'Share sheet enabled'
$classes = @((Get-PlistNode $root 'WFWorkflowInputContentItemClasses').SelectNodes('string') | ForEach-Object InnerText | Sort-Object)
Assert-Contract (($classes -join '|') -ceq 'WFAVAssetContentItem|WFGenericFileContentItem|WFImageContentItem|WFPDFContentItem|WFStringContentItem|WFURLContentItem') 'Exact Files/Images/Media/PDF/Text/URLs input classes'
Assert-Contract ((Get-PlistNode $root 'WFWorkflowImportQuestions').ChildNodes.Count -eq 0) 'No import questions or user-edited configuration'
Assert-Contract ((Get-PlistNode $root 'WFWorkflowNoInputBehavior').ChildNodes.Count -eq 0) 'No clipboard or prompt fallback'
Assert-Contract ($raw -cnotmatch 'WFSSHPassword|WFSSHKey|PRIVATE KEY|<string>Clipboard</string>|<key>WFURL|<key>WFHTTP|/api/|https?://(?:\d{1,3}\.){3}') 'No credentials, clipboard input, HTTP, or hardcoded receiver'

$allowed = @('alert','base64encode','conditional','count','delay','detect.dictionary','detect.text','dictionary','documentpicker.open','documentpicker.save','exit','format.filesize','getitemfromlist','getitemtype','gettext','getvalueforkey','math','nothing','notification','number','properties.files','repeat.count','repeat.each','runsshscript','setvariable','text.match','text.replace')
$actions = @()
$named = @{}
$uuids = @{}
$groups = @{}
$stack = [System.Collections.Generic.List[object]]::new()
for ($i = 0; $i -lt $nodes.Count; $i++) {
    $node = $nodes[$i]
    $id = Get-PlistText $node 'WFWorkflowActionIdentifier'
    Assert-Contract ($id.StartsWith('is.workflow.actions.') -and $allowed -ccontains $id.Substring(20)) 'Only audited native actions; no shell, HTTP, URL open/fetch, webview or third-party actions'
    $p = Get-PlistNode $node 'WFWorkflowActionParameters'
    $action = [pscustomobject]@{ Index = $i; Id = $id.Substring(20); P = $p; Mode = ''; Group = ''; Ancestors = @($stack | ForEach-Object { "$($_.Group):$($_.Branch)" }) }
    $actions += $action
    if ($null -eq $p) { continue }
    $uuid = Get-PlistText $p 'UUID'
    if ($uuid) {
        Assert-Contract (-not $uuids.ContainsKey($uuid)) 'Unique action UUID'
        $uuids[$uuid] = $action
    }
    $name = Get-PlistText $p 'CustomOutputName'
    if ($name) {
        Assert-Contract (-not $named.ContainsKey($name)) "Unique output name $name"
        $named[$name] = $action
    }
    $mode = Get-PlistNode $p 'WFControlFlowMode'
    if ($null -eq $mode) { continue }
    $action.Mode = $mode.InnerText
    $action.Group = Get-PlistText $p 'GroupingIdentifier'
    if ($action.Mode -eq '0') {
        Assert-Contract (-not $groups.ContainsKey($action.Group)) 'Unique control-flow group'
        $groups[$action.Group] = @{ Start = $i; End = -1 }
        $stack.Add([pscustomobject]@{ Group = $action.Group; Branch = '0' })
    } else {
        Assert-Contract ($stack.Count -gt 0 -and $stack[-1].Group -ceq $action.Group) 'Properly nested control-flow groups'
        if ($action.Mode -eq '1') { $stack[-1].Branch = '1' }
        elseif ($action.Mode -eq '2') {
            $groups[$action.Group].End = $i
            $stack.RemoveAt($stack.Count - 1)
        } else { Assert-Contract $false 'Unknown control-flow mode' }
    }
}
Assert-Contract ($stack.Count -eq 0) 'All control-flow groups closed'
foreach ($dict in $document.SelectNodes('//dict')) {
    $keys = @($dict.SelectNodes('key') | ForEach-Object InnerText)
    Assert-Contract (@($keys | Sort-Object -Unique).Count -eq $keys.Count) 'No duplicate plist dictionary keys'
    if ((Get-PlistText $dict 'Type') -eq 'ActionOutput') {
        $uuid = Get-PlistText $dict 'OutputUUID'
        Assert-Contract ($uuids.ContainsKey($uuid)) 'Every magic-variable reference resolves'
        Assert-Contract ((Get-PlistText $uuids[$uuid].P 'CustomOutputName') -ceq (Get-PlistText $dict 'OutputName')) 'Magic-variable name matches UUID source'
    }
    if ((Get-PlistText $dict 'WFSerializationType') -eq 'WFTextTokenString') {
        $template = Token-Template $dict
        Assert-Contract ($template -cnotmatch '\{(?:item|items|Repeat Item|ShortcutInput)\}') 'Never interpolate original file/input objects into text'
    }
}
foreach ($action in $actions | Where-Object Id -ceq 'text.match') {
    Assert-Contract ((Get-PlistNode $action.P 'WFMatchTextCaseSensitive').Name -ceq 'true') 'Explicit case-sensitive setup/schema/token matching'
}

$ssh = @($actions | Where-Object Id -ceq 'runsshscript')
Assert-Contract ($ssh.Count -eq 6) 'Exactly six SSH operations'
$operations = @{}
foreach ($action in $ssh) {
    $command = Token-Template (Get-PlistNode $action.P 'WFSSHScript')
    Assert-Contract ($command -cmatch '\Ashareme-v1 (setup|request|status|cancel|text|upload)(?: |\z)') 'Only receiver protocol commands, never a shell program'
    $operation = $Matches[1]
    Assert-Contract (-not $operations.ContainsKey($operation)) 'One action per SSH operation'
    $operations[$operation] = $action
    $expected = switch ($operation) {
        'setup' { 'shareme-v1 setup' }
        'request' { 'shareme-v1 request' }
        'upload' { 'shareme-v1 upload {requestID} {requestToken} {encodedName}' }
        default { "shareme-v1 $operation {requestID} {requestToken}" }
    }
    Assert-Contract ($command -ceq $expected) 'Exact command and only validated ID/token/filename interpolation'
    Assert-Contract ((Get-PlistText $action.P 'WFSSHAuthenticationType') -ceq 'SSH Key') 'Native device SSH-key authentication'
    $user = if ($operation -eq 'setup') { 'setup-{enrollment}' } else { 'shareme' }
    Assert-Contract ((Token-Template (Get-PlistNode $action.P 'WFSSHUser')) -ceq $user) 'Exact enrollment/transfer SSH username'
    Assert-Contract ((Token-Template (Get-PlistNode $action.P 'WFSSHHost')) -ceq '{host}') 'Host comes only from validated configuration'
    Assert-Contract ((Token-Template (Get-PlistNode $action.P 'WFSSHPort')) -ceq '{port}') 'Port comes only from validated configuration'
}
Assert-Contract ($operations.setup.Index -eq $ssh[0].Index) 'Setup handling precedes every normal transfer operation'
Assert-Input $operations.setup.P 'WFInput' 'emptyStdin'
Assert-Input $operations.status.P 'WFInput' 'emptyStdin'
Assert-Input $operations.cancel.P 'WFInput' 'emptyStdin'
Assert-Input $operations.request.P 'WFInput' 'requestJSON' 'Variable'
Assert-Input $operations.text.P 'WFInput' 'textToSend' 'Variable'
Assert-Input $operations.upload.P 'WFInput' 'item' 'Variable'
Assert-Contract ((Get-TokenString (Get-PlistNode (Named 'emptyStdin').P 'WFTextActionText')) -ceq '') 'Explicit zero-byte stdin, not prior action output'

$noInputGuard = Find-Guard 'items' '101' '' $operations.setup.Index
Assert-Stops $noInputGuard
$batchGuard = Assert-ImmediateGuard 'itemCount' '2' '50'
Assert-Contract ($batchGuard.Index -lt $operations.setup.Index) 'Batch limit precedes networking'
$shapeGuard = Assert-ImmediateGuard 'validConfigurationShape' '101'
Assert-Contract ($shapeGuard.Index -lt $operations.setup.Index -and $shapeGuard.Ancestors.Count -eq 0) 'Uncoerced JSON shape gates setup and saved config before networking'
Assert-Contract ((Token-Template (Get-PlistNode (Named 'validConfigurationShape').P 'text')) -ceq '{configurationJSON}') 'Shape check uses original JSON, before dictionary extraction'
[void](Assert-ImmediateGuard 'configurationCharacters' '2' '4096')
Assert-Contract ((Get-PlistText (Named 'setupPrefix').P 'WFMatchTextPattern') -ceq '\Ashareme-setup-v1:') 'Exact case-sensitive setup prefix at the start'
Assert-Contract ((Token-Template (Get-PlistNode (Named 'setupPrefix').P 'text')) -ceq '{possibleSetup}') 'Match Text uses the actual native intent text parameter'
$singleGuard = Find-Guard 'itemCount' '4' '1' (Named 'possibleSetup').Index
$textGuard = Find-Guard 'firstType' '4' 'Text' (Named 'possibleSetup').Index
Assert-Ancestor (Named 'possibleSetup') $singleGuard
Assert-Ancestor (Named 'possibleSetup') $textGuard
Assert-Input (Named 'possibleSetup').P 'WFInput' 'firstItem'
Assert-Input (Named 'firstType').P 'WFInput' 'firstItem'
Assert-Contract ((Get-PlistText (Named 'setupJSON').P 'WFReplaceTextFind') -ceq '\Ashareme-setup-v1:') 'Strip only the exact setup prefix'
Assert-Contract ((Get-PlistText (Named 'setupJSON').P 'WFReplaceTextReplace') -ceq '') 'No other setup rewriting'

$load = @($actions | Where-Object Id -ceq 'documentpicker.open')
$save = @($actions | Where-Object Id -ceq 'documentpicker.save')
Assert-Contract ($load.Count -eq 1 -and $save.Count -eq 1) 'One fixed nonsecret config read and write; no other storage actions'
Assert-Contract ((Get-PlistText $load[0].P 'WFFileStorageService') -ceq 'iCloud Drive' -and (Get-PlistText $save[0].P 'WFFileStorageService') -ceq 'iCloud Drive') 'Documented native iCloud Drive service'
Assert-Contract ((Get-PlistText $load[0].P 'WFGetFilePath') -ceq '/ShareMe.json' -and (Get-PlistText $save[0].P 'WFFileDestinationPath') -ceq '/ShareMe.json') 'Fixed Shortcuts-relative config path'
foreach ($key in @('WFShowFilePicker','WFFileErrorIfNotFound')) { Assert-Contract ((Get-PlistNode $load[0].P $key).Name -ceq 'false') 'No file picker or missing-config native error' }
Assert-Contract ((Get-PlistNode $save[0].P 'WFAskWhereToSave').Name -ceq 'false') 'No repeated save-location prompt'
Assert-Contract ((Get-PlistNode $save[0].P 'WFSaveFileOverwrite').Name -ceq 'true') 'Re-pair replaces nonsecret config'
Assert-Ancestor $load[0] (Find-Guard 'isSetup' '4' '0' $load[0].Index)
[void](Assert-ImmediateGuard 'storedConfiguration' '101')
Assert-Fields 'safeConfiguration' @{ version = 1; host = '{host}'; port = 49322; name = '{pcName}'; fingerprint = '{fingerprint}' }
Assert-Input (Named 'safeConfigurationJSON').P 'WFInput' 'safeConfiguration'
Assert-Input $save[0].P 'WFInput' 'safeConfigurationJSON'
[void](Assert-ImmediateGuard 'savedConfiguration' '101')
$readyGuard = Assert-ImmediateGuard 'validSetupStatus' '101'
Assert-Contract ($save[0].Index -gt $groups[$readyGuard.Group].End -and $readyGuard.Index -gt $operations.setup.Index) 'Config is saved only after ready response'
$setupGuard = Find-Guard 'isSetup' '4' '1' $operations.setup.Index
Assert-Ancestor $operations.setup $setupGuard
Assert-Ancestor $save[0] $setupGuard
Assert-EndStops $setupGuard

foreach ($field in @('version','host','port','pcName','fingerprint')) {
    $lookup = Named $field
    Assert-Input $lookup.P 'WFInput' 'configuration' 'Variable'
    $key = if ($field -eq 'pcName') { 'name' } else { $field }
    Assert-Contract ((Get-PlistText $lookup.P 'WFDictionaryKey') -ceq $key) "Read configuration field $key"
}
foreach ($entry in @{ versionType = 'Number'; hostType = 'Text'; portType = 'Number'; nameType = 'Text'; fingerprintType = 'Text' }.GetEnumerator()) {
    $guard = Find-Guard $entry.Key '5' $entry.Value $operations.setup.Index
    Assert-Stops $guard
    Assert-Contract ($guard.Ancestors.Count -eq 0) 'Schema-type validation gates both setup and transfer'
    Assert-Logic $guard '0' 5
}
foreach ($entry in @{ version = '1'; port = '49322' }.GetEnumerator()) {
    $guard = Find-Guard $entry.Key '5' $entry.Value $operations.setup.Index
    Assert-Stops $guard
    Assert-Contract ($guard.Ancestors.Count -eq 0) 'Protocol version/port validated before any connection'
    Assert-Logic $guard '0' 2
}
foreach ($count in @('5','6')) { Assert-Stops (Find-Guard 'configurationKeyCount' '5' $count $operations.setup.Index) }
foreach ($name in @('validHost','validName','validFingerprint','validEnrollment','validRequestID','validRequestToken')) {
    $guard = Assert-ImmediateGuard $name '101'
    $before = if ($name -in @('validRequestID','validRequestToken')) { $operations.status.Index } else { $operations.setup.Index }
    Assert-Contract ($guard.Index -lt $before) "$name rejection precedes relevant networking"
    $expectedInput = @{ validHost = 'host'; validName = 'pcName'; validFingerprint = 'fingerprint'; validEnrollment = 'enrollment'; validRequestID = 'requestID'; validRequestToken = 'requestToken' }[$name]
    Assert-Contract ((Token-Template (Get-PlistNode (Named $name).P 'text')) -ceq "{$expectedInput}") 'Validation matches the correct explicit metadata input'
}
Assert-Stops (Find-Guard 'enrollmentType' '5' 'Text' $operations.setup.Index)
$confirmation = $actions[$operations.setup.Index - 1]
Assert-Contract ($confirmation.Id -ceq 'alert' -and (Get-PlistNode $confirmation.P 'WFAlertActionCancelButtonShown').Name -ceq 'true') 'Cancellable setup confirmation immediately precedes SSH'
$confirmationText = Token-Template (Get-PlistNode $confirmation.P 'WFAlertActionMessage')
Assert-Contract ($confirmationText.Contains('{pcName}') -and $confirmationText.Contains('{host}:{port}') -and $confirmationText.Contains('{fingerprint}') -and $confirmationText.Contains("first-connection prompt")) 'Confirmation names the PC/address and demands host-fingerprint comparison'

foreach ($pair in @{ setupResponseKeys = 'setupResponse'; requestKeys = 'request'; pollKeys = 'pollResponse'; cancelKeys = 'cancelResponse'; receiptKeys = 'receipt' }.GetEnumerator()) {
    Assert-Contract ((Get-PlistText (Named $pair.Key).P 'WFGetDictionaryValueType') -ceq 'All Keys') 'Inspect error-key presence, even for an empty error value'
    Assert-Input (Named $pair.Key).P 'WFInput' $pair.Value
    [void](Assert-ImmediateGuard $pair.Key '99' 'error')
}
foreach ($entry in @{ setupResponse = 'setupOutput'; request = 'requestOutput'; pollResponse = 'pollOutput'; cancelResponse = 'cancelOutput' }.GetEnumerator()) {
    $decode = Named $entry.Key
    Assert-Contract ($decode.Id -ceq 'detect.dictionary') 'Parse each SSH response as JSON'
    Assert-Input $decode.P 'WFInput' $entry.Value
}
Assert-Input (Named 'receipt').P 'WFInput' 'receiptOutput' 'Variable'
Assert-Fields 'fileRequest' @{ kind = 'file'; name = '{itemName}'; size = -1 }
Assert-Fields 'textRequest' @{ kind = 'text'; name = 'Text'; size = -1; text = '{textToSend}' }
Assert-Input (Named 'itemName').P 'WFInput' 'item' 'Variable'
Assert-Contract ((Get-PlistText (Named 'itemName').P 'WFContentItemPropertyName') -ceq 'Name') 'Read original filename'
Assert-Input (Named 'originalFileSize').P 'WFInput' 'item' 'Variable'
Assert-Contract ((Get-PlistText (Named 'fileBytes').P 'WFFileSizeFormat') -ceq 'Bytes') 'File size checked in bytes'
[void](Assert-ImmediateGuard 'fileBytes' '2' '2147483648')
[void](Assert-ImmediateGuard 'textCharacters' '2' '65536')
Assert-Contract ((Get-PlistText (Named 'textCharacters').P 'WFCountType') -ceq 'Characters') 'Local text check is explicitly characters, server remains UTF-8 byte authority'
$encoders = @($actions | Where-Object Id -ceq 'base64encode')
Assert-Contract ($encoders.Count -eq 1) 'No whole-file base64 conversion'
Assert-Input $encoders[0].P 'WFInput' 'itemName'
Assert-Contract ((Get-PlistText $encoders[0].P 'WFEncodeMode') -ceq 'Encode' -and (Get-PlistText $encoders[0].P 'WFBase64LineBreakMode') -ceq 'None') 'Only filename: standard base64 without wrapping'
$fileRoute = Find-Guard 'itemType' '4' 'Text' (Named 'fileRequest').Index
Assert-Contract (@((Terms $fileRoute) | Where-Object { Is-Term $_ 'itemType' '4' 'URL' }).Count -eq 1) 'Shared URLs take the text route, never fetched'
Assert-Ancestor (Named 'textRequest') $fileRoute
Assert-Ancestor (Named 'fileRequest') $fileRoute '1'
Assert-Logic $fileRoute '0' 2
foreach ($convert in $actions | Where-Object Id -ceq 'detect.text') {
    $ref = Reference-Name (Get-PlistNode (Get-PlistNode $convert.P 'WFInput') 'Value')
    Assert-Contract ($ref -cin @('firstItem','safeConfiguration','textRequest','fileRequest','item','storedConfiguration')) 'Only explicitly routed text, nonsecret config file or allowlisted JSON serialization'
    if ($ref -ceq 'item') { Assert-Ancestor $convert $fileRoute }
}
foreach ($variable in @('requestJSON','receiptOutput','encodedName','item','items')) {
    $writes = @($actions | Where-Object { $_.Id -ceq 'setvariable' -and (Get-PlistText $_.P 'WFVariableName') -ceq $variable })
    $expected = if ($variable -in @('requestJSON','receiptOutput')) { 2 } else { 1 }
    Assert-Contract ($writes.Count -eq $expected) "No hidden reassignment of $variable"
    foreach ($write in $writes) {
        $ref = Get-PlistNode (Get-PlistNode $write.P 'WFInput') 'Value'
        switch ($variable) {
            'requestJSON' {
                $producer = $uuids[(Get-PlistText $ref 'OutputUUID')]
                Assert-Contract ($producer.Id -ceq 'detect.text') 'Request stdin is native JSON serialization'
                $dictionary = Reference-Name (Get-PlistNode (Get-PlistNode $producer.P 'WFInput') 'Value')
                Assert-Contract ($dictionary -cin @('textRequest','fileRequest')) 'Request JSON originates only in audited dictionaries'
            }
            'receiptOutput' {
                $producer = $uuids[(Get-PlistText $ref 'OutputUUID')]
                Assert-Contract ($producer.Index -in @($operations.text.Index,$operations.upload.Index)) 'Receipt originates in transfer SSH stdout only'
            }
            'encodedName' { Assert-Contract ((Get-PlistText $ref 'OutputUUID') -ceq (Get-PlistText $encoders[0].P 'UUID')) 'Upload filename derives only from filename encoder' }
            'items' { Assert-Input $write.P 'WFInput' 'ShortcutInput' 'ExtensionInput' }
        }
    }
}
$each = @($actions | Where-Object { $_.Id -ceq 'repeat.each' -and $_.Mode -ceq '0' })
$poll = @($actions | Where-Object { $_.Id -ceq 'repeat.count' -and $_.Mode -ceq '0' })
Assert-Contract ($each.Count -eq 1 -and $poll.Count -eq 1) 'One item loop and one bounded polling loop'
foreach ($operation in @('request','status','cancel','text','upload')) { Assert-Ancestor $operations[$operation] $each[0] }
Assert-Input $each[0].P 'WFInput' 'items' 'Variable'
$capture = $actions[$each[0].Index + 1]
Assert-Contract ($capture.Id -ceq 'setvariable' -and (Get-PlistText $capture.P 'WFVariableName') -ceq 'item') 'Capture outer Repeat Item before nested polling'
Assert-Input $capture.P 'WFInput' 'Repeat Item' 'Variable'
Assert-Contract ((Get-PlistText $poll[0].P 'WFRepeatCount') -ceq '120') 'At most 120 approval checks'
Assert-Ancestor $operations.status $poll[0]
Assert-Ancestor $operations.status (Find-Guard 'status' '4' 'pending' $operations.status.Index)
$delay = $actions[$operations.status.Index - 1]
Assert-Contract ($delay.Id -ceq 'delay' -and (Get-PlistText $delay.P 'WFDelayTime') -ceq '1') 'Wait one second before each pending status check'
[void](Assert-ImmediateGuard 'validInitialStatus' '101')
[void](Assert-ImmediateGuard 'validPollStatus' '101')
foreach ($entry in @{
    validSetupStatus = @('setupStatus', '\Aready\z')
    validInitialStatus = @('initialStatus', '\Apending\z')
    validPollStatus = @('pollStatus', '\A(?:pending|accepted|declined|expired|cancelled)\z')
}.GetEnumerator()) {
    $validation = Named $entry.Key
    Assert-Contract ((Get-PlistText $validation.P 'WFMatchTextPattern') -ceq $entry.Value[1]) 'Only exact lowercase protocol states'
    Assert-Contract ((Token-Template (Get-PlistNode $validation.P 'text')) -ceq "{$($entry.Value[0])}") 'Protocol state validator reads the corresponding response field'
}
foreach ($status in @('declined','expired','cancelled')) {
    $terminal = Find-Guard 'status' '4' $status $operations.cancel.Index
    Assert-Stops $terminal
    Assert-Logic $terminal '0' 3
}
$unknown = Find-Guard 'status' '5' 'pending' $operations.cancel.Index
Assert-Stops $unknown
Assert-Logic $unknown '1' 2
Assert-Contract (@((Terms $unknown) | Where-Object { Is-Term $_ 'status' '5' 'accepted' }).Count -eq 1) 'Only pending/accepted are nonterminal valid poll statuses'
$timeout = Find-Guard 'status' '5' 'accepted' $operations.cancel.Index
Assert-Contract ($timeout.Index -gt $groups[$poll[0].Group].End) 'Accepted-only upload gate is after polling'
Assert-Ancestor $operations.cancel $timeout
Assert-EndStops $timeout
foreach ($operation in @('text','upload')) {
    Assert-Contract ($operations[$operation].Index -gt $groups[$timeout.Group].End) 'No transfer bytes before accepted-only gate'
    Assert-Ancestor $operations[$operation] $each[0]
}
$receiptGuard = Assert-ImmediateGuard 'receiptID' '101'
$notifications = @($actions | Where-Object Id -ceq 'notification')
Assert-Contract ($notifications.Count -eq 1 -and $notifications[0].Index -gt $receiptGuard.Index) 'Notify only after receipt validation'
$increments = @($actions | Where-Object Id -ceq 'math')
Assert-Contract ($increments.Count -eq 1 -and $increments[0].Index -gt $groups[$receiptGuard.Group].End) 'Count success only after receipt and error guards'
Assert-Input $increments[0].P 'WFInput' 'sent' 'Variable'
Assert-Contract ((Get-PlistText $increments[0].P 'WFMathOperation') -ceq '+' -and (Get-PlistText $increments[0].P 'WFMathOperand') -ceq '1') 'Exactly one success per confirmed item'
Assert-Ancestor $notifications[0] (Find-Guard 'sent' '2' '0' $notifications[0].Index)

$hostPattern = Get-PlistText (Named 'validHost').P 'WFMatchTextPattern'
$shapePattern = Get-PlistText (Named 'validConfigurationShape').P 'WFMatchTextPattern'
$configFixture = [ordered]@{ version = 1; host = '192.168.1.2'; port = 49322; name = 'PC "Unicode" café'; fingerprint = 'SHA256:' + ('a' * 43) }
foreach ($setupFixture in @($false, $true)) {
    if ($setupFixture) { $configFixture['enrollment'] = 'a' * 43 }
    $fixtureJSON = $configFixture | ConvertTo-Json
    Assert-Contract ($fixtureJSON -cmatch $shapePattern) 'Accept generated setup/config JSON in either field order with proper escaping'
    foreach ($bad in @(
        "[$fixtureJSON]",
        ($fixtureJSON -replace '"version": 1', '"version": true'),
        ($fixtureJSON -replace '"version": 1', '"version": "1"'),
        ($fixtureJSON -replace '"port": 49322', '"port": 22'),
        ($fixtureJSON -replace '"port": 49322', '"port": [49322]'),
        ($fixtureJSON -replace '"host": "192.168.1.2"', '"host": ["192.168.1.2"]'),
        ($fixtureJSON -replace '"host": "192.168.1.2"', '"host": {"value":"192.168.1.2"}'),
        ($fixtureJSON -replace '"name": ', '"unknown": ')
    )) { Assert-Contract ($bad -cnotmatch $shapePattern) 'Reject top-level arrays, nested/Boolean/scalar coercions and unexpected fields before dictionary parsing' }
}
foreach ($hostValue in @('10.0.0.1','10.255.255.255','172.16.0.1','172.31.255.255','192.168.0.1','192.168.255.255')) {
    Assert-Contract ($hostValue -cmatch $hostPattern) 'Accept RFC1918 IPv4'
}
foreach ($hostValue in @('','8.8.8.8','127.0.0.1','169.254.1.1','100.64.0.1','172.15.1.1','172.32.0.1','192.169.0.1','192.168.01.1','192.168.1.999','192.168.1.1:49322','https://192.168.1.1','localhost','[::1]','192.168.1.1.example.com','user@192.168.1.1','192.168.1.1/x',"192.168.1.1`n",' 192.168.1.1')) {
    Assert-Contract ($hostValue -cnotmatch $hostPattern) 'Reject nonprivate, malformed, whitespace and injectable host'
}
foreach ($name in @('validEnrollment','validRequestToken','validRequestID','validFingerprint')) {
    $pattern = Get-PlistText (Named $name).P 'WFMatchTextPattern'
    $good = switch ($name) {
        'validRequestID' { 'a' * 32 }
        'validFingerprint' { 'SHA256:' + ('a' * 43) }
        default { 'a' * 43 }
    }
    Assert-Contract ($good -cmatch $pattern) 'Accept well-formed metadata token'
    foreach ($bad in @('', "$good`n", "$good;", "$good ", $good.Substring(1), ($good + 'x'))) {
        Assert-Contract ($bad -cnotmatch $pattern) 'Reject malformed and command-injectable metadata'
    }
}
if ($RequireBuildMetadata) {
    $metadata = Get-Content -Raw -LiteralPath (Join-Path $PSScriptRoot 'ShareMe.build.json') | ConvertFrom-Json
    Assert-Contract ($metadata.schemaVersion -eq 1 -and $metadata.shortcutName -ceq 'Share Me' -and $metadata.transport -ceq 'ssh-v1') 'Publisher metadata identifies the exact generic SSH template'
    Assert-Contract ($metadata.setupPrefix -ceq 'shareme-setup-v1:' -and $metadata.protocolPort -eq 49322) 'Publisher metadata matches setup contract'
    Assert-Contract ($metadata.unsignedSha256 -ceq (Get-FileHash -LiteralPath $Path -Algorithm SHA256).Hash.ToLowerInvariant()) 'Publisher metadata binds the exact unsigned plist'
    Assert-Contract ($metadata.sourceSha256 -ceq (Get-FileHash -LiteralPath (Join-Path $PSScriptRoot 'ShareMe.cherri') -Algorithm SHA256).Hash.ToLowerInvariant()) 'Reviewed source is unchanged since compilation'
    Assert-Contract ($metadata.compiler -ceq 'github.com/electrikmilk/cherri@dc82114f346f77e5cf04987cb0ee4e781302fc32' -and ($metadata.compilerFlags -join '|') -ceq '--skip-sign|--no-ansi') 'Pinned local compiler provenance with third-party signing disabled'
    Assert-Contract ($metadata.staticValidation -ceq 'passed' -and $metadata.appleSigned -eq $false -and $metadata.physicalIPhoneVerified -eq $false -and $null -eq $metadata.installationURL) 'Build metadata cannot misrepresent unsigned or unverified status'
}
Write-Host "PASS: $script:checks static ssh-v1 plist/contract assertions; $($actions.Count) actions. No Shortcut was installed or executed."
