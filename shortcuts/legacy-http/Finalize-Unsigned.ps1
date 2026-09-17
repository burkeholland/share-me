[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string] $InputPath,
    [Parameter(Mandatory)] [string] $OutputPath
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Plist.ps1')
$document = Read-ShortcutPlist $InputPath
$root = $document.SelectSingleNode('/plist/dict')
$question = (Get-PlistNode $root 'WFWorkflowImportQuestions').SelectSingleNode('dict')
if ($null -eq (Get-PlistNode $question 'ActionIndex')) {
    $key = $document.CreateElement('key')
    $key.InnerText = 'ActionIndex'
    [void] $question.AppendChild($key)
    $index = $document.CreateElement('integer')
    $index.InnerText = '0'
    [void] $question.AppendChild($index)
}
foreach ($items in $document.SelectNodes('//key[text()="WFDictionaryFieldValueItems"]/following-sibling::*[1]')) {
    foreach ($item in $items.SelectNodes('dict')) {
        if ($null -eq (Get-PlistNode $item 'WFItemType')) {
            $key = $document.CreateElement('key')
            $key.InnerText = 'WFItemType'
            [void] $item.AppendChild($key)
            $type = $document.CreateElement('integer')
            $type.InnerText = '0'
            [void] $item.AppendChild($type)
        }
    }
}
$actions = Get-PlistNode $root 'WFWorkflowActions'
$matches = @($actions.SelectNodes('dict') | Where-Object {
    if ((Get-PlistText $_ 'WFWorkflowActionIdentifier') -ne 'is.workflow.actions.downloadurl') { return $false }
    $parameters = Get-PlistNode $_ 'WFWorkflowActionParameters'
    return (Get-TokenString (Get-PlistNode $parameters 'WFURL')).EndsWith('/api/upload')
})
if ($matches.Count -ne 1) { throw 'Expected exactly one file upload action to lower.' }
$parameters = Get-PlistNode $matches[0] 'WFWorkflowActionParameters'
if ((Get-PlistText $parameters 'WFHTTPBodyType') -ne 'Form') { throw 'File upload must use Form.' }
$fields = @(Get-FieldItems (Get-PlistNode $parameters 'WFFormValues'))
if ($fields.Count -ne 1 -or (Get-FieldName $fields[0]) -ne 'file') { throw 'Expected one file form field.' }
$field = $fields[0]
$type = Get-PlistNode $field 'WFItemType'
$oldValue = Get-PlistNode $field 'WFValue'
if ($type.InnerText -ne '0' -or (Get-TokenString $oldValue) -cne [string][char]0xfffc) {
    throw 'Expected an unmodified, single-variable Cherri text form field.'
}
$tokenValue = Get-PlistNode $oldValue 'Value'
$attachments = Get-PlistNode $tokenValue 'attachmentsByRange'
$attachment = Get-PlistNode $attachments '{0, 1}'
if ((Get-PlistText $attachment 'Type') -ne 'Variable' -or (Get-PlistText $attachment 'VariableName') -ne 'Repeat Item') {
    throw 'The file field must reference the outer Repeat Item without text conversion.'
}

# Apple's exported Form File shape uses type 5 and a token-attachment parameter state.
$replacement = $document.CreateElement('dict')
$replacement.InnerXml = '<key>Value</key><dict><key>Value</key><dict/><key>WFSerializationType</key><string>WFTextTokenAttachment</string></dict><key>WFSerializationType</key><string>WFTokenAttachmentParameterState</string>'
$inner = Get-PlistNode $replacement 'Value'
$placeholder = Get-PlistNode $inner 'Value'
[void] $inner.ReplaceChild($attachment.CloneNode($true), $placeholder)
[void] $field.ReplaceChild($replacement, $oldValue)
$type.InnerText = '5'
Write-ShortcutPlist $document $OutputPath
