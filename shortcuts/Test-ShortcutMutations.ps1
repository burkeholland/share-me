[CmdletBinding()]
param([string] $Path = (Join-Path $PSScriptRoot 'ShareMe.unsigned.shortcut'))
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Plist.ps1')
& (Join-Path $PSScriptRoot 'Test-Shortcut.ps1') -Path $Path
$baseline = Read-ShortcutPlist $Path

function Find-SSH {
    param($Document, [string] $Operation)
    foreach ($action in (Get-PlistNode $Document.SelectSingleNode('/plist/dict') 'WFWorkflowActions').SelectNodes('dict')) {
        if ((Get-PlistText $action 'WFWorkflowActionIdentifier') -cne 'is.workflow.actions.runsshscript') { continue }
        $p = Get-PlistNode $action 'WFWorkflowActionParameters'
        if ((Get-TokenString (Get-PlistNode $p 'WFSSHScript')) -cmatch "\Ashareme-v1 $Operation(?: |\z)") { return ,$p }
    }
    throw "Missing SSH action: $Operation"
}
function Find-Named {
    param($Document, [string] $Name)
    foreach ($action in (Get-PlistNode $Document.SelectSingleNode('/plist/dict') 'WFWorkflowActions').SelectNodes('dict')) {
        $p = Get-PlistNode $action 'WFWorkflowActionParameters'
        if ($null -ne $p -and (Get-PlistText $p 'CustomOutputName') -ceq $Name) { return ,$p }
    }
    throw "Missing output: $Name"
}
$mutations = [ordered]@{
    'file-stdin-as-text' = {
        param($d)
        $inputNode = Get-PlistNode (Find-SSH $d 'upload') 'WFInput'
        (Get-PlistNode $inputNode 'WFSerializationType').InnerText = 'WFTextTokenString'
    }
    'implicit-setup-stdin' = {
        param($d)
        $p = Find-SSH $d 'setup'
        $inputNode = Get-PlistNode $p 'WFInput'
        [void]$p.RemoveChild($inputNode.PreviousSibling)
        [void]$p.RemoveChild($inputNode)
    }
    'password-authentication' = {
        param($d)
        (Get-PlistNode (Find-SSH $d 'upload') 'WFSSHAuthenticationType').InnerText = 'Password'
    }
    'shell-command' = {
        param($d)
        (Get-PlistNode (Find-SSH $d 'setup') 'WFSSHScript').InnerText = 'powershell -Command anything'
    }
    'file-interpolated-in-script' = {
        param($d)
        $p = Find-SSH $d 'upload'
        $command = Get-PlistNode (Get-PlistNode $p 'WFSSHScript') 'Value'
        $refs = Get-PlistNode $command 'attachmentsByRange'
        foreach ($ref in $refs.SelectNodes('dict')) {
            if ((Get-PlistText $ref 'VariableName') -ceq 'encodedName') {
                (Get-PlistNode $ref 'VariableName').InnerText = 'item'
            }
        }
    }
    'save-enrollment-field' = {
        param($d)
        $fields = @(Get-FieldItems (Get-PlistNode (Find-Named $d 'safeConfiguration') 'WFItems'))
        $new = $fields[0].CloneNode($true)
        (Get-PlistNode (Get-PlistNode (Get-PlistNode $new 'WFKey') 'Value') 'string').InnerText = 'enrollment'
        [void]$fields[0].ParentNode.AppendChild($new)
    }
    'persist-whole-setup' = {
        param($d)
        $p = Find-Named $d 'safeConfigurationJSON'
        (Get-PlistNode (Get-PlistNode (Get-PlistNode $p 'WFInput') 'Value') 'OutputName').InnerText = 'configuration'
    }
    'public-host-regex' = {
        param($d)
        (Get-PlistNode (Find-Named $d 'validHost') 'WFMatchTextPattern').InnerText = '\A[0-9.]+\z'
    }
    'case-insensitive-setup' = {
        param($d)
        $p = Find-Named $d 'setupPrefix'
        [void]$p.ReplaceChild($d.CreateElement('false'), (Get-PlistNode $p 'WFMatchTextCaseSensitive'))
    }
    'too-many-polls' = {
        param($d)
        $d.SelectSingleNode('//key[text()="WFRepeatCount"]/following-sibling::*[1]').InnerText = '121'
    }
    'whole-file-base64' = {
        param($d)
        $p = Find-Named $d 'EncodeFilename'
        $ref = Get-PlistNode (Get-PlistNode $p 'WFInput') 'Value'
        (Get-PlistNode $ref 'OutputName').InnerText = 'item'
    }
    'no-receipt-guard' = {
        param($d)
        $p = Find-Named $d 'receiptID'
        $guard = Get-PlistNode $p.ParentNode.NextSibling 'WFWorkflowActionParameters'
        (Get-PlistNode $guard 'WFCondition').InnerText = '100'
    }
    'file-request-includes-text' = {
        param($d)
        $textFields = @(Get-FieldItems (Get-PlistNode (Find-Named $d 'textRequest') 'WFItems'))
        $textField = @($textFields | Where-Object { (Get-FieldName $_) -ceq 'text' })[0]
        $fileFields = @(Get-FieldItems (Get-PlistNode (Find-Named $d 'fileRequest') 'WFItems'))
        [void]$fileFields[0].ParentNode.AppendChild($textField.CloneNode($true))
    }
    'duplicate-uuid' = {
        param($d)
        (Get-PlistNode (Find-Named $d 'host') 'UUID').InnerText = Get-PlistText (Find-Named $d 'port') 'UUID'
    }
    'request-before-validation' = {
        param($d)
        $node = (Find-SSH $d 'request').ParentNode
        [void]$node.ParentNode.PrependChild($node)
    }
    'ignore-poll-error' = {
        param($d)
        $p = Find-Named $d 'pollKeys'
        $guard = Get-PlistNode $p.ParentNode.NextSibling 'WFWorkflowActionParameters'
        (Get-PlistNode $guard 'WFConditionalActionString').InnerText = 'ignored'
    }
    'save-without-ready' = {
        param($d)
        $p = Find-Named $d 'validSetupStatus'
        $guard = Get-PlistNode $p.ParentNode.NextSibling 'WFWorkflowActionParameters'
        (Get-PlistNode $guard 'WFCondition').InnerText = '100'
    }
}
$workspace = Join-Path $PSScriptRoot ('.validation-' + [guid]::NewGuid().ToString('N'))
New-Item -ItemType Directory -Path $workspace | Out-Null
$candidate = Join-Path $workspace 'mutated.shortcut'
$rejected = 0
try {
    foreach ($mutation in $mutations.GetEnumerator()) {
        $document = $baseline.CloneNode($true)
        & $mutation.Value $document
        Write-ShortcutPlist $document $candidate
        $failed = $false
        try { & (Join-Path $PSScriptRoot 'Test-Shortcut.ps1') -Path $candidate 6>$null }
        catch { $failed = $true }
        if (-not $failed) { throw "Static validator accepted unsafe mutation: $($mutation.Key)" }
        $rejected++
    }
} finally {
    if (Test-Path -LiteralPath $candidate) { Remove-Item -LiteralPath $candidate }
    Remove-Item -LiteralPath $workspace
}
Write-Host "PASS: rejected $rejected unsafe compiled-plist mutations; baseline artifact unchanged."
