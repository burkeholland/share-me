[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string] $InputPath,
    [Parameter(Mandatory)] [string] $OutputPath
)
$ErrorActionPreference = 'Stop'
. (Join-Path $PSScriptRoot 'Plist.ps1')
$document = Read-ShortcutPlist $InputPath
$root = $document.SelectSingleNode('/plist/dict')
$questions = Get-PlistNode $root 'WFWorkflowImportQuestions'
if ($null -ne $questions -and $questions.ChildNodes.Count -ne 0) {
    throw 'Generic SSH workflow must not contain import questions.'
}

# Cherri omits zero-valued Text field types. Make the native dictionary schema explicit.
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
Write-ShortcutPlist $document $OutputPath
