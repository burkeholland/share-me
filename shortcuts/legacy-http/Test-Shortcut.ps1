[CmdletBinding()]
param([string] $Path = (Join-Path $PSScriptRoot 'ShareMe.unsigned.shortcut'))
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Plist.ps1')
$script:checks = 0

function Assert-Contract {
    param([bool] $Condition, [string] $Message)
    if (-not $Condition) { throw "Shortcut contract: $Message" }
    $script:checks++
}

function Assert-Header {
    param([System.Xml.XmlNode] $Parameters, [string[]] $Names)
    $headers = @(Get-FieldItems (Get-PlistNode $Parameters 'WFHTTPHeaders'))
    $actual = @($headers | ForEach-Object { Get-FieldName $_ } | Sort-Object)
    Assert-Contract (($actual -join '|') -ceq (($Names | Sort-Object) -join '|')) 'Exact request header names'
    foreach ($header in $headers) {
        Assert-Contract ((Get-PlistText $header 'WFItemType') -eq '0') 'Headers must be text dictionary items'
        $value = Get-PlistNode $header 'WFValue'
        Assert-Contract ((Get-PlistText $value 'WFSerializationType') -eq 'WFTextTokenString') 'Header token string serialization'
        if ((Get-FieldName $header) -eq 'X-Share-Me') {
            Assert-Contract ((Get-TokenString $value) -ceq '1') 'Static X-Share-Me header'
        } else {
            Assert-Contract ((Get-TokenString $value) -ceq [string][char]0xfffc) 'Runtime-only request ID/token header'
            $attachments = Get-PlistNode (Get-PlistNode $value 'Value') 'attachmentsByRange'
            $attachment = Get-PlistNode $attachments '{0, 1}'
            Assert-Contract ((Get-PlistText $attachment 'Type') -eq 'ActionOutput') 'Headers retain their magic variable'
            $expected = if ((Get-FieldName $header) -eq 'X-Share-Me-Token') { 'requestToken' } else { 'requestID' }
            Assert-Contract ((Get-PlistText $attachment 'OutputName') -ceq $expected) "Header references $expected"
        }
    }
}

function Assert-StopGuard {
    param([string] $OutputName, [string] $Condition)
    $lookup = -1
    for ($i = 0; $i -lt $actions.Count; $i++) {
        $p = Get-PlistNode $actions[$i] 'WFWorkflowActionParameters'
        if ($null -ne $p -and (Get-PlistText $p 'CustomOutputName') -ceq $OutputName) { $lookup = $i; break }
    }
    Assert-Contract ($lookup -ge 0) "Lookup exists for $OutputName"
    $guard = $actions[$lookup + 1]
    $p = Get-PlistNode $guard 'WFWorkflowActionParameters'
    Assert-Contract ((Get-PlistText $guard 'WFWorkflowActionIdentifier') -eq 'is.workflow.actions.conditional') "$OutputName is immediately checked"
    Assert-Contract ((Get-PlistText $p 'WFCondition') -eq $Condition) "$OutputName condition"
    $reference = Get-PlistNode (Get-PlistNode (Get-PlistNode $p 'WFInput') 'Variable') 'Value'
    Assert-Contract ((Get-PlistText $reference 'OutputName') -ceq $OutputName) "Guard reads $OutputName"
    Assert-Contract ((Get-PlistText $actions[$lookup + 2] 'WFWorkflowActionIdentifier') -eq 'is.workflow.actions.alert') "$OutputName failure is shown"
    Assert-Contract ((Get-PlistText $actions[$lookup + 3] 'WFWorkflowActionIdentifier') -eq 'is.workflow.actions.exit') "$OutputName failure stops the Shortcut"
}

$document = Read-ShortcutPlist $Path
$root = $document.SelectSingleNode('/plist/dict')
$actions = @((Get-PlistNode $root 'WFWorkflowActions').SelectNodes('dict'))
Assert-Contract ($actions.Count -gt 0) 'Nonempty compiled action graph'
$types = @((Get-PlistNode $root 'WFWorkflowTypes').SelectNodes('string') | ForEach-Object { $_.InnerText })
Assert-Contract ($types -contains 'ActionExtension') 'Share sheet enabled'
$inputClasses = @((Get-PlistNode $root 'WFWorkflowInputContentItemClasses').SelectNodes('string') | ForEach-Object { $_.InnerText })
foreach ($class in @('WFGenericFileContentItem', 'WFImageContentItem', 'WFAVAssetContentItem', 'WFPDFContentItem', 'WFStringContentItem', 'WFURLContentItem')) {
    Assert-Contract ($inputClasses -contains $class) "Input class $class"
}
$noInput = Get-PlistNode $root 'WFWorkflowNoInputBehavior'
Assert-Contract ($noInput.InnerText -match 'Clipboard') 'Clipboard fallback'
$questions = @((Get-PlistNode $root 'WFWorkflowImportQuestions').SelectNodes('dict'))
Assert-Contract ($questions.Count -eq 1) 'Exactly one receiver import question'
Assert-Contract ((Get-PlistText $questions[0] 'ParameterKey') -eq 'WFTextActionText') 'Import question targets a text action'
$indexNode = Get-PlistNode $questions[0] 'ActionIndex'
Assert-Contract ($null -ne $indexNode) 'Explicit import question action index'
$questionIndex = if ($null -eq $indexNode) { 0 } else { [int]$indexNode.InnerText }
Assert-Contract ($questionIndex -ge 0 -and $questionIndex -lt $actions.Count) 'Valid import question action index'
$questionAction = Get-PlistNode $actions[$questionIndex] 'WFWorkflowActionParameters'
Assert-Contract ((Get-PlistText $actions[$questionIndex] 'WFWorkflowActionIdentifier') -eq 'is.workflow.actions.gettext') 'Import question edits receiver text'
Assert-Contract ((Get-TokenString (Get-PlistNode $questionAction 'WFTextActionText')) -eq '') 'No PC address in import question'
Assert-Contract ((Get-PlistText $questions[0] 'DefaultValue') -eq '') 'No default PC address'

$requests = @{}
$groups = [System.Collections.Generic.Stack[string]]::new()
$seenUUIDs = @{}
$seenGroups = @{}
$pollCount = 0
$eachCount = 0
$notifications = 0
$errorLookups = 0
foreach ($action in $actions) {
    $identifier = Get-PlistText $action 'WFWorkflowActionIdentifier'
    $parameters = Get-PlistNode $action 'WFWorkflowActionParameters'
    if ($null -eq $parameters) { continue }
    $uuid = Get-PlistText $parameters 'UUID'
    if ($uuid) {
        Assert-Contract (-not $seenUUIDs.ContainsKey($uuid)) 'Unique action UUID'
        $seenUUIDs[$uuid] = $true
    }
    $mode = Get-PlistNode $parameters 'WFControlFlowMode'
    if ($null -ne $mode) {
        $group = Get-PlistText $parameters 'GroupingIdentifier'
        if ($mode.InnerText -eq '0') {
            Assert-Contract (-not $seenGroups.ContainsKey($group)) 'Distinct control-flow group identifiers'
            $seenGroups[$group] = $true
            $groups.Push($group)
        } else {
            Assert-Contract ($groups.Count -gt 0 -and $groups.Peek() -eq $group) 'Balanced nested control flow'
            if ($mode.InnerText -eq '2') { [void] $groups.Pop() }
        }
    }
    if ($identifier -eq 'is.workflow.actions.repeat.each' -and $mode.InnerText -eq '0') { $eachCount++ }
    if ($identifier -eq 'is.workflow.actions.repeat.count' -and $mode.InnerText -eq '0') {
        Assert-Contract ((Get-PlistText $parameters 'WFRepeatCount') -eq '120') '120 approval checks maximum'
        $pollCount++
    }
    if ($identifier -eq 'is.workflow.actions.delay') {
        Assert-Contract ((Get-PlistText $parameters 'WFDelayTime') -eq '1') 'One-second poll delay'
    }
    if ($identifier -eq 'is.workflow.actions.getvalueforkey' -and (Get-PlistText $parameters 'WFDictionaryKey') -eq 'error') { $errorLookups++ }
    if ($identifier -eq 'is.workflow.actions.notification') { $notifications++ }
    if ($identifier -ne 'is.workflow.actions.downloadurl') { continue }
    $urlNode = Get-PlistNode $parameters 'WFURL'
    $url = Get-TokenString $urlNode
    Assert-Contract ($url.StartsWith([string][char]0xfffc)) 'Every HTTP request starts with configured receiver'
    $urlAttachments = Get-PlistNode (Get-PlistNode $urlNode 'Value') 'attachmentsByRange'
    $receiverReference = Get-PlistNode $urlAttachments '{0, 1}'
    Assert-Contract ((Get-PlistText $receiverReference 'OutputName') -eq 'baseURL') 'HTTP host comes only from receiver configuration'
    Assert-Contract (-not $requests.ContainsKey($url)) 'Expected distinct request actions'
    $requests[$url] = $parameters
}
Assert-Contract ($groups.Count -eq 0) 'All control-flow groups closed'
Assert-Contract ($eachCount -eq 1) 'Repeat With Each input item'
Assert-Contract ($pollCount -eq 1) 'One bounded approval loop per item'
Assert-Contract ($notifications -eq 1) 'One final notification'
Assert-Contract ($errorLookups -eq 3) 'Inspect request, poll, and receipt errors'
Assert-Contract ($requests.Count -eq 4) 'Only request, poll, text upload, and file upload HTTP actions'
$fileNames = @($actions | Where-Object { (Get-PlistText $_ 'WFWorkflowActionIdentifier') -eq 'is.workflow.actions.properties.files' })
Assert-Contract ($fileNames.Count -eq 1) 'One Get Details of Files action'
$nameParameters = Get-PlistNode $fileNames[0] 'WFWorkflowActionParameters'
Assert-Contract ((Get-PlistText $nameParameters 'WFContentItemPropertyName') -eq 'Name') 'Read original filename'
Assert-Contract ($null -eq (Get-PlistNode $nameParameters 'WFFolder')) 'Do not use incorrect WFFolder input key'
$nameInput = Get-PlistNode (Get-PlistNode $nameParameters 'WFInput') 'Value'
Assert-Contract ((Get-PlistText $nameInput 'VariableName') -eq 'item') 'File details explicitly read the current item'
foreach ($lookup in @('requestError', 'pollError', 'receiptError')) { Assert-StopGuard $lookup '100' }
Assert-StopGuard 'receiptID' '101'
$v = [string][char]0xfffc
$request = $requests["$v/api/request"]
$poll = $requests["$v/api/request/$v"]
$text = $requests["$v/api/text"]
$file = $requests["$v/api/upload"]
Assert-Contract ($null -ne $request -and $null -ne $poll -and $null -ne $text -and $null -ne $file) 'Exact backend endpoint paths'
Assert-Header $request @('X-Share-Me')
Assert-Header $poll @('X-Share-Me-Token')
Assert-Header $text @('X-Share-Me', 'X-Share-Me-Request', 'X-Share-Me-Token')
Assert-Header $file @('X-Share-Me', 'X-Share-Me-Request', 'X-Share-Me-Token')
Assert-Contract ((Get-PlistText $request 'WFHTTPMethod') -eq 'POST') 'Request uses POST'
Assert-Contract ((Get-PlistText $request 'WFHTTPBodyType') -eq 'JSON') 'Request uses JSON'
$requestFields = @(Get-FieldItems (Get-PlistNode $request 'WFJSONValues'))
Assert-Contract ((@($requestFields | ForEach-Object { Get-FieldName $_ } | Sort-Object) -join '|') -eq 'kind|name|size|text') 'Exact request JSON fields'
$sizes = @($requestFields | Where-Object { (Get-FieldName $_) -eq 'size' })
Assert-Contract ($sizes.Count -eq 1 -and (Get-PlistText $sizes[0] 'WFItemType') -eq '3') 'Unknown size is a JSON number'
Assert-Contract ((Get-TokenString (Get-PlistNode $sizes[0] 'WFValue')) -eq '-1') 'Unknown size equals -1'
Assert-Contract ((Get-PlistText $poll 'WFHTTPMethod') -eq 'GET') 'Polling uses GET'
foreach ($upload in @($text, $file)) {
    Assert-Contract ((Get-PlistText $upload 'WFHTTPMethod') -eq 'POST') 'Upload uses POST'
    Assert-Contract ((Get-PlistText $upload 'WFHTTPBodyType') -eq 'Form') 'Upload uses Form, never raw File'
}
$textFields = @(Get-FieldItems (Get-PlistNode $text 'WFFormValues'))
Assert-Contract ($textFields.Count -eq 1 -and (Get-FieldName $textFields[0]) -eq 'text') 'Text form field name'
Assert-Contract ((Get-PlistText $textFields[0] 'WFItemType') -eq '0') 'Text form field type'
Assert-Contract ((Get-TokenString (Get-PlistNode $textFields[0] 'WFValue')) -ceq $v) 'Text passed as an unchanged variable'
$textAttachments = Get-PlistNode (Get-PlistNode (Get-PlistNode $textFields[0] 'WFValue') 'Value') 'attachmentsByRange'
Assert-Contract ((Get-PlistText (Get-PlistNode $textAttachments '{0, 1}') 'VariableName') -eq 'textToSend') 'Upload preserves original text variable'
$fileFields = @(Get-FieldItems (Get-PlistNode $file 'WFFormValues'))
Assert-Contract ($fileFields.Count -eq 1 -and (Get-FieldName $fileFields[0]) -eq 'file') 'File form field name'
Assert-Contract ((Get-PlistText $fileFields[0] 'WFItemType') -eq '5') 'Typed multipart File, not a string or array'
$fileState = Get-PlistNode $fileFields[0] 'WFValue'
Assert-Contract ((Get-PlistText $fileState 'WFSerializationType') -eq 'WFTokenAttachmentParameterState') 'File parameter state'
$fileToken = Get-PlistNode $fileState 'Value'
Assert-Contract ((Get-PlistText $fileToken 'WFSerializationType') -eq 'WFTextTokenAttachment') 'File attachment token'
$fileVariable = Get-PlistNode $fileToken 'Value'
Assert-Contract ((Get-PlistText $fileVariable 'Type') -eq 'Variable') 'File uses original variable'
Assert-Contract ((Get-PlistText $fileVariable 'VariableName') -eq 'Repeat Item') 'Preserve each original file'
Assert-Contract ($null -eq (Get-PlistNode $fileVariable 'Aggrandizements')) 'Do not coerce the file to text'
Assert-Contract (-not [IO.File]::ReadAllText($Path).Contains('Content-Type')) 'Multipart boundary belongs to Shortcuts'
$serialized = [IO.File]::ReadAllText($Path)
Assert-Contract ($serialized -notmatch 'https?://(?:\d{1,3}\.){3}\d{1,3}') 'No literal receiver IPv4 address in artifact'
$regexAction = @($actions | Where-Object { (Get-PlistText $_ 'WFWorkflowActionIdentifier') -eq 'is.workflow.actions.text.match' })
Assert-Contract ($regexAction.Count -eq 1) 'One receiver validation expression'
$receiverPattern = Get-PlistText (Get-PlistNode $regexAction[0] 'WFWorkflowActionParameters') 'WFMatchTextPattern'
Assert-StopGuard 'validReceiver' '101'
$normalizers = @($actions | Where-Object {
    $p = Get-PlistNode $_ 'WFWorkflowActionParameters'
    return $null -ne $p -and (Get-PlistText $p 'CustomOutputName') -eq 'baseURL'
})
Assert-Contract ($normalizers.Count -eq 1) 'One receiver URL normalizer'
$normalizer = Get-PlistNode $normalizers[0] 'WFWorkflowActionParameters'
$slashPattern = Get-PlistText $normalizer 'WFReplaceTextFind'
$slashReplacement = Get-PlistText $normalizer 'WFReplaceTextReplace'
Assert-Contract ($slashPattern -ceq '/$' -and $slashReplacement -ceq '') 'Remove exactly one optional trailing slash'
Assert-Contract ((Get-PlistNode $normalizer 'WFReplaceTextRegularExpression').Name -eq 'true') 'Slash removal is a regular expression'
foreach ($valid in @('http://10.0.0.1:49321', 'http://10.255.255.255:49321/', 'http://172.16.0.1:49321/', 'http://172.31.255.255:49321', 'http://192.168.1.1:49321', ' http://192.168.255.255:49321/ ')) {
    $normalized = [regex]::Replace($valid.Trim(), $slashPattern, $slashReplacement)
    Assert-Contract ($normalized -cmatch $receiverPattern) 'Accept RFC1918 receiver with optional trailing slash'
}
foreach ($invalid in @(
    '', 'http://127.0.0.1:49321', 'http://8.8.8.8:49321/',
    'http://169.254.1.1:49321', 'http://100.64.0.1:49321', 'http://9.255.255.255:49321',
    'http://11.0.0.0:49321', 'http://172.15.255.255:49321', 'http://172.32.0.1:49321',
    'http://192.167.255.255:49321', 'http://192.169.0.0:49321',
    'http://192.168.1.999:49321', 'http://192.168.01.1:49321', 'http://192.168.1.1:80',
    'https://192.168.1.1:49321', 'http://192.168.1.1:49321/api/upload',
    'http://192.168.1.1:49321?token=x', 'http://192.168.1.1:49321#fragment',
    'http://192.168.1.1:49321//', 'http://name:password@192.168.1.1:49321',
    'http://192.168.1.1.example.com:49321', 'http://example.com:49321', 'http://[::1]:49321'
)) {
    $normalized = [regex]::Replace($invalid.Trim(), $slashPattern, $slashReplacement)
    Assert-Contract ($normalized -cnotmatch $receiverPattern) 'Reject invalid or non-RFC1918 receiver'
}
Write-Host "PASS: $script:checks static plist/contract assertions. No Shortcut was installed or executed."
