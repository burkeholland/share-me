Set-StrictMode -Version Latest

function Read-ShortcutPlist {
    param([string] $Path)
    $settings = [System.Xml.XmlReaderSettings]::new()
    $settings.DtdProcessing = [System.Xml.DtdProcessing]::Ignore
    $settings.XmlResolver = $null
    $reader = [System.Xml.XmlReader]::Create($Path, $settings)
    try {
        $document = [System.Xml.XmlDocument]::new()
        $document.XmlResolver = $null
        $document.Load($reader)
        if ($document.DocumentElement.Name -ne 'plist') { throw 'Expected an XML plist.' }
        return ,$document
    } finally {
        $reader.Dispose()
    }
}

function Get-PlistNode {
    param([System.Xml.XmlNode] $Dictionary, [string] $Key)
    if ($null -eq $Dictionary -or $Dictionary.Name -ne 'dict') { throw "Expected a dictionary for '$Key'." }
    foreach ($child in $Dictionary.ChildNodes) {
        if ($child.Name -eq 'key' -and $child.InnerText -ceq $Key) {
            return ,$child.NextSibling
        }
    }
    return $null
}

function Get-PlistText {
    param([System.Xml.XmlNode] $Dictionary, [string] $Key)
    $node = Get-PlistNode $Dictionary $Key
    if ($null -eq $node) { return '' }
    return $node.InnerText
}

function Get-FieldItems {
    param([System.Xml.XmlNode] $Field)
    if ((Get-PlistText $Field 'WFSerializationType') -ne 'WFDictionaryFieldValue') {
        throw 'Expected WFDictionaryFieldValue serialization.'
    }
    $value = Get-PlistNode $Field 'Value'
    $items = Get-PlistNode $value 'WFDictionaryFieldValueItems'
    if ($null -eq $items -or $items.Name -ne 'array') { throw 'Expected dictionary field items.' }
    return @($items.SelectNodes('dict'))
}

function Get-FieldName {
    param([System.Xml.XmlNode] $Field)
    $key = Get-PlistNode $Field 'WFKey'
    $value = Get-PlistNode $key 'Value'
    return Get-PlistText $value 'string'
}

function Get-TokenString {
    param([System.Xml.XmlNode] $Node)
    if ($Node.Name -eq 'string') { return $Node.InnerText }
    if ((Get-PlistText $Node 'WFSerializationType') -ne 'WFTextTokenString') {
        throw 'Expected a string or WFTextTokenString.'
    }
    return Get-PlistText (Get-PlistNode $Node 'Value') 'string'
}

function Write-ShortcutPlist {
    param([System.Xml.XmlDocument] $Document, [string] $Path)
    $settings = [System.Xml.XmlWriterSettings]::new()
    $settings.Encoding = [System.Text.UTF8Encoding]::new($false)
    $settings.Indent = $true
    $settings.NewLineChars = "`n"
    $writer = [System.Xml.XmlWriter]::Create($Path, $settings)
    try { $Document.Save($writer) } finally { $writer.Dispose() }
}
