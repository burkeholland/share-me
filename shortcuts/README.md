# Share Me: one reusable Apple Shortcut

**Status: locally compiled and statically validated, NOT a customer release.**
`ShareMe.cherri` is the generic **ssh-v1** workflow. `ShareMe.unsigned.shortcut`
is its unsigned XML plist. Neither Apple signing nor a physical iPhone run has
been performed. There is **no real installation/iCloud link yet**. Do not
enable a customer installation button merely because this unsigned file exists.

Every customer must install the same publisher-signed template. Customers do
**not** build a Shortcut, edit its actions, answer import questions, paste
addresses/keys, enter SSH commands, or sign personalized workflows. One-time
guided pairing supplies runtime configuration, not a personalized installation.

The incompatible, unencrypted HTTP experiment is explicitly isolated in
[`legacy-http`](legacy-http/README.md). It is not an alternative ssh-v1 installer.

## Customer flow, once a verified release exists

1. On the iPhone, scan the PC's Share Me QR and open its paired HTTPS phone page.
2. Choose **Add to share sheet** and install the publisher's signed **Share Me**
   file or genuine iCloud link. Its installed name must be **Share Me**.
3. Choose **Connect Shortcut** on that phone page. The browser opens the
   already-installed template using Apple's local URL scheme:

   ```text
   shortcuts://run-shortcut?name=Share%20Me&input=text&text=<URL-encoded setup prefix plus JSON>
   ```

   This is not an enrollment token in an iCloud URL, signing-service upload, or
   public web query. The whole text parameter is URL-encoded once.
4. Confirm the PC's display name/address and compare its displayed SHA256 SSH
   fingerprint with the PC and Apple's first-connection prompt. **Cancel on
   mismatch, an unexpected changed-host warning, or an incomparable format.**
   Allow Apple's native network/SSH/Files permission prompts as appropriate.
   Never edit an action to generate/paste a key: if the generic template cannot
   use the native device key automatically, this workflow is not release-ready.
5. Share a file, image, media item, PDF, selected text, or URL using **Share Me**.
   Approve each item on Windows. A success notification requires a PC receipt.

Launching without input shows setup/use guidance. There is **no clipboard
fallback**. Missing settings instruct the user to scan the QR and connect; no
shared content is uploaded anywhere in that path. Connecting a different PC
replaces this Shortcut's one saved PC configuration after successful enrollment.

## Runtime contract

### Setup first, before any normal transfer

Only a single **Text** input starting exactly `shareme-setup-v1:` enters setup.
The suffix is JSON with exactly these fields:

```text
{version: 1, host: <private IPv4>, port: 49322, name: <PC display name>,
 enrollment: <43 base64url characters>, fingerprint: "SHA256:<43 base64 characters>"}
```

Before dictionary extraction, a bounded (4,096-character) raw-JSON shape check
requires a flat object, literal numeric version `1`/port `49322`, and only the
listed text fields. This prevents native content coercions from accepting
Boolean numbers or one-element arrays. Field order and normal JSON string
escaping/whitespace are supported; alternate numeric spellings or escaped
property names are intentionally not used by the setup producer.

The source then checks field counts, native scalar types, version, port, RFC1918 IPv4
(`10/8`, `172.16/12`, `192.168/16`), name length 1–128/no ASCII controls,
fingerprint and enrollment formats **before the first network action**. It
rejects URLs, hostnames, public/loopback/link-local IPs, leading-zero octets,
whitespace, newlines, alternate ports and command delimiters. A saved config is
revalidated on every normal run. Invalid JSON or native action errors stop using
Apple's error UI; explicit schema/protocol failures show an explanation and stop.

After a cancellable confirmation naming the PC/address/fingerprint, setup uses:

| Native SSH parameter | Setup value |
|---|---|
| `WFSSHHost`, `WFSSHPort` | Validated host and port |
| `WFSSHAuthenticationType` | `SSH Key` |
| `WFSSHUser` | `setup-` plus the short-lived enrollment token |
| `WFSSHScript` | Exactly `shareme-v1 setup` |
| `WFInput` | Typed reference to an explicit empty Text action |
| `WFSSHKey`, `WFSSHPassword` | **Absent**; no key material/password in the artifact |

Only an error-free JSON response with `status: "ready"` permits saving. The
saved object is **rebuilt from an allowlist**, never copied from the setup input:
`{version, host, port, name, fingerprint}`. The enrollment token is used only
in memory for that one SSH username. The native private key never enters the
workflow data or config.

### Native config storage: actual serialized behavior

The template uses the documented `is.workflow.actions.documentpicker.open`
and `.save` actions with `WFFileStorageService: "iCloud Drive"`. Both paths are
`/ShareMe.json`, relative to the service's **Shortcuts** directory:
**iCloud Drive → Shortcuts → ShareMe.json**.

- Read: `WFShowFilePicker: false`, `WFFileErrorIfNotFound: false`.
- Save: `WFAskWhereToSave: false`, `WFSaveFileOverwrite: true`.
- Save input is typed `WFInput` referencing **only** the JSON serialization of
  the nonsecret allowlisted dictionary.

There is no stored folder bookmark, folder selector, import question or action
editing. The intended permission is native Files/iCloud access; the graph does
not ask where to save on each run. These service/path parameters are real
documented native schemas, not an invented private storage API. Modern iOS
migration, permission prompting and the actual resulting folder still require
physical-device verification. If iCloud Drive is disabled or the migrated action
requires a folder reference, **do not tell customers to edit actions**: block
release until the publisher resolves the generic storage path.

This location is user-visible and may sync the **nonsecret** PC configuration
via iCloud. It does not store enrollment tokens, private keys, request tokens,
text payloads or shared files. It is not an application secret vault. If sharing
settings between Apple devices, each device must still enroll its native key.

### Transfers: no Windows OS shell

Subsequent SSH operations always use username **`shareme`**, native **SSH Key**
authentication, and validated saved host/port. The receiver is a restricted
protocol server, not PowerShell, Bash, `curl`, Python, SFTP or an OS account.
Only these exact commands are serialized:

| Script | Independent stdin |
|---|---|
| `shareme-v1 request` | UTF-8 JSON dictionary serialization |
| `shareme-v1 status <id> <token>` | Explicit empty Text |
| `shareme-v1 cancel <id> <token>` | Explicit empty Text |
| `shareme-v1 text <id> <token>` | Exact original text variable |
| `shareme-v1 upload <id> <token> <base64-filename>` | **Original raw file variable** |

File requests contain exactly `{kind:"file", name:filename, size:-1}` — **no
`text` field**. Text/URL requests contain exactly
`{kind:"text", name:"Text", size:-1, text:originalText}`. Dictionary actions
handle JSON escaping; payloads are not hand-concatenated into JSON or scripts.
URLs take the explicit Text/URL branch and are never fetched. Files are never
implicitly converted into text. The original outer Repeat Item is captured in
`item` before the nested polling loop.

The pending response must contain a lowercase 32-hex `id`, 43-character
base64url `token`, and `status: "pending"`. The loop waits one second before each
pending status request, at most **120 checks**. Once accepted, remaining loop
iterations perform no further waits/network calls. Declined, expired,
cancelled, unknown status and JSON errors stop. Timeout attempts cancellation
with empty stdin, inspects errors and stops. Native SSH failures stop through
Apple's error UI. Latency means 120 checks is **not** a 120-second wall-clock
guarantee; force-quitting the Shortcut cannot guarantee cancellation.

No file bytes are sent before acceptance. Only the **filename** is standard
base64 encoded, with `WFBase64LineBreakMode: "None"`. Upload `WFInput` is a
`WFTextTokenAttachment` with `{Type:"Variable", VariableName:"item"}`, no
coercion/aggrandizement. It is neither a `WFTextTokenString` nor whole-file
base64. The separate `WFSSHScript` contains only fixed protocol words and
validated request ID/token/encoded filename.

Each response is parsed as JSON; the workflow checks **presence of an `error`
key**, even if its value is empty. A successful upload/text response must have
a nonempty `id` before incrementing the sent count. Notification happens only
after the batch has valid receipts. Earlier accepted transfers are not rolled
back if a later item fails.

Limits: **50 items**, server maximum **2 GiB/file**, **64 KiB UTF-8/text**.
The native File Size → Format File Size (Bytes) check rejects known oversize
files; the server remains authoritative. The text precheck counts **characters**,
not UTF-8 bytes, so multibyte text may pass locally and then be rejected by the
server. No reliable native UTF-8 byte-count claim is made. Do not promise iOS can
process multi-gigabyte files: Shortcuts may materialize data in memory and is
subject to provider conversion, lifecycle, timeout and resource limits.

## Local build and static validation

From the application directory:

```powershell
.\shortcuts\Build-Shortcut.ps1
.\shortcuts\Test-Shortcut.ps1
.\shortcuts\Test-ShortcutMutations.ps1
.\shortcuts\Test-Publisher.ps1
```

The build uses `..\.tools\go\bin\go.exe`, or `-GoPath` with Go 1.24+.
It pins Cherri revision `dc82114f346f77e5cf04987cb0ee4e781302fc32` in
`shortcuts\.tools\bin\cherri-<revision>.exe`; compiler/module caches are under
`shortcuts\.tools`. Initial missing-compiler installation downloads compiler
dependencies, never workflow data. Compilation **always** includes
`--skip-sign --no-ansi`. Cherri's default Windows signing behavior can upload
to RoutineHub; **never run it without `--skip-sign`**.

The finalizer only supplies Cherri's omitted zero-valued native Text dictionary
field types. Custom action definitions already emit correct typed SSH `WFInput`
and documented file-store fields. Random unique action/group UUIDs are retained;
`--derive-uuids` is not used because this compiler revision previously generated
colliding nested-loop group IDs. Builds are source-reproducible, not byte-for-byte
deterministic (UUIDs and dictionary ordering can change).

Build outputs:

- `ShareMe.unsigned.shortcut` — generic compiled XML plist, **not installable**.
- `ShareMe.unsigned.shortcut.sha256` — hash for that exact unsigned artifact.
- `ShareMe.build.json` — name, `transport: "ssh-v1"`, setup prefix, port,
  compiler revision/flags, source/artifact hashes, static-validation result,
  `appleSigned: false`, `physicalIPhoneVerified: false`, null installation URL.

Tests inspect the actual plist graph: exact typed stdin/command attachments,
UUID resolution and uniqueness, balanced unique control-flow groups, routing,
schema/address/port gates, confirmation, config allowlist and persistence input,
all six operations, request dictionaries, bounds, acceptance/error/receipt
guards, original-file capture, filename-only base64, and no HTTP/shell/clipboard
actions or file-text interpolation. Mutation tests prove the validator rejects
deliberately corrupted graphs. They are not an Apple runtime/emulator or proof of
large-file byte preservation.

`Test-Publisher.ps1` requires Bash (Git Bash on Windows). It checks the signed
filename and sidecar paths, then exercises the production metadata/checksum
formatters with fixture digests. It never signs or creates release files.

## One-time publisher procedure — blocked without an Apple device

1. Copy this directory's generic source, scripts, build metadata, unsigned file
   and hash to a Mac. Install PowerShell 7 (`pwsh`) for the same static checker;
   Apple's Shortcuts CLI, `plutil` and `shasum` must also be available.
2. Review the generic source. Do not inject customer data or credentials.
3. In that directory, run:

   ```bash
   bash ./Publish-Shortcut.command
   ```

   The script refuses non-macOS systems, verifies the unsigned hash, re-runs the
   plist checker with `-RequireBuildMetadata` (binding the reviewed source and
   artifact to the build hashes), then runs **Apple's** `shortcuts sign --mode anyone`.
   Apple receives the generic workflow for signing; no third-party signing
   service is used. It does not import, install or run the workflow.
4. Outputs after successful Apple signing:
   `Share Me.shortcut`, `Share Me.shortcut.sha256`, `ShareMe.release.json`.
   `ShareMe.release.json` is written beside the signed artifact with
   `schemaVersion: 2`, `transport: "ssh-v1"`, and `sha256` equal to the SHA-256
   of the signed `Share Me.shortcut`, as required by the parent staging contract.
   Additional metadata includes the unsigned SHA-256 digest,
   signature-container header `AEA1` / hex `41454131`, signing time and command.
   The unsigned artifact is preserved; a prior signed artifact is not replaced
   on a failed sign/header check. An `AEA1` header is a **container sanity check**,
   not cryptographic signature verification.
5. Signing, the final output, and the downloaded file retain the canonical
   basename **Share Me.shortcut** (including the space, also recorded as
   `artifact` and `downloadFilename` in metadata). The parent consumes
   `shortcuts\Share Me.shortcut`, but the public URL is `/assets/ShareMe.shortcut`.
   The parent serves it with `Content-Disposition: attachment; filename="Share Me.shortcut"`
   and a matching same-origin `download="Share Me.shortcut"` attribute.
   Do not percent-encode the public asset path or loosen the Worker's
   reserved/encoded-path guards. The sidecar remains `ShareMe.release.json`.
   Verify the actual installed name is **Share Me**. Name preservation during
   signing/import is a physical release gate, not an excuse to ask customers to
   rename/edit their Shortcut. Neither the filename nor signed/link metadata is
   a verified importer naming guarantee. No speculative `WFWorkflowName` field
   is injected. Test import of the **final published file/link**, not just the
   staging file.
6. Install this exact generic signed artifact on test iPhones and complete the
   checklist below. Before public release, open **Add to share sheet** on the
   paired phone page and choose **Connect installed Shortcut**, then **Open
   Shortcuts**. This configures a manually installed signed test copy without
   publishing it or editing its actions. Metadata deliberately stays `physicalIPhoneVerified: false`
   and `installationURL: null`; signing alone is not a release approval.
   Parent staging requires `physicalIPhoneVerified: true` before advertising a
   signed download or iCloud link. After completing the checks, the publisher
   records confirmation with `scripts\register-shortcut.ps1 -SignedArtifact
   -VerifiedOnIPhone`, or `-ICloudURL "<real link>" -VerifiedOnIPhone`.
   These switches record a manual result; they do not perform device testing.
   Only after real testing may the publisher record verification and distribute
   the unchanged file. Optionally use Apple's **Copy iCloud Link** and verify the
   resulting real installation link. Register an iCloud link only after the
   publisher confirms the imported title is exactly **Share Me**, without edits.
   This script never fabricates one.

Release metadata version **2** is separate from the local build metadata and
setup protocol, which remain version **1**. Setup still uses
`shareme-setup-v1:`, port `49322`, and the `name` field for the PC display name;
there are no customer import questions or action edits. The parent owns the
root `shortcut-release.json` iCloud registration
(`schemaVersion: 2`, `transport: "ssh-v1"`, `icloudURL`) and frontend manifest
(`schemaVersion: 2`, `transport: "ssh-v1"`, `name: "Share Me"`,
`state: "published" | "unpublished"`, `url`). This publisher does not create or
modify those root/frontend files.

## Physical-device release checklist

- [ ] Fresh Apple device/account, no preexisting SSH key or known host: install
      without editing any action; native device key is created/usable.
- [ ] Another person's device installs the **identical** template, independently
      enrolls its own key and sends to its own PC. No publisher/user key or
      personalized IP is present in the installation.
- [ ] Exact installed title **Share Me** and documented `run-shortcut` URL work.
      Cancelled setup saves nothing; malformed/version/type/IP/port/token inputs
      make no connection. Test Boolean-vs-Number and array-vs-scalar native coercion.
- [ ] The SSH host prompt exposes a fingerprint comparable to the supplied
      **SHA256** value. Unknown and changed-host mismatches abort; no blind trust.
      Stored fingerprint is informational, not a custom SSH pinning parameter.
- [ ] Exact iCloud Drive/Shortcuts file path and JSON contents verified; no token,
      key, request payload or file body saved. Permission denial, missing/malformed
      config, disabled iCloud Drive and re-pairing behave without action editing.
- [ ] Native types/routing verified in supported iOS languages: plain text, URL
      (including query/fragment), PDF, ZIP/binary including NUL, `.txt`/JSON **files**,
      Unicode filenames, Photos originals/HEIC and video. Compare receiver byte
      count and SHA-256 against the original; no metadata/media conversion assumed.
- [ ] Empty stdin really sends EOF without prior-action content; a zero-byte
      file and whitespace/newline/Unicode text arrive intact.
- [ ] No file bytes before approval; 120-poll timeout/cancel, decline, expiry,
      cancellation, malformed/error JSON (including empty error), native SSH
      failure, missing receipt and network loss do not report success.
- [ ] Batch boundary 50/51, exact UTF-8 text boundary, per-file server cap and
      realistically sized large files tested; report actual limitations instead
      of promising multi-GB reliability. Test interrupted/backgrounded iOS.

### Serialization and platform evidence

These sources inform the generic source; they do **not** certify a physical run:

- [Apple: run a shortcut using a URL scheme](https://support.apple.com/guide/shortcuts/run-a-shortcut-from-a-url-apd624386f42/ios)
- [Apple: command-line shortcuts and signing](https://support.apple.com/guide/shortcuts-mac/run-shortcuts-from-the-command-line-apd455c82f02/mac)
- [Apple: sharing shortcuts](https://support.apple.com/guide/shortcuts/share-shortcuts-apdf01f8c054/ios)
- [Native SSH schema: separate `WFInput`, script, authentication and native key UI](https://github.com/pfgithub/shortcuts3.0-types/blob/shortcuts3.0/docs/actions/runscriptoverssh.md)
- [Native Get File schema](https://github.com/pfgithub/shortcuts3.0-types/blob/shortcuts3.0/docs/actions/getfile.md)
- [Native Save File schema](https://github.com/pfgithub/shortcuts3.0-types/blob/shortcuts3.0/docs/actions/savefile.md)
- [Native Base64 schema, including no-wrap mode](https://github.com/pfgithub/shortcuts3.0-types/blob/shortcuts3.0/docs/actions/base64encode.md)
- [Match Text intent schema: `text` parameter, pattern/case overrides](https://github.com/pfgithub/shortcuts3.0-types/blob/shortcuts3.0/docs/actions/isworkflowactionstextmatch.md)
- [Pinned Cherri source](https://github.com/electrikmilk/cherri/tree/dc82114f346f77e5cf04987cb0ee4e781302fc32)

The third-party decompile of
[`WFRunSSHScriptAction`](https://github.com/EthanArbuckle/iPhone18-3_26.1_23B85_Restore/blob/main/System/Library/PrivateFrameworks/ActionKit.framework/ActionKit/WFRunSSHScriptAction.mm#L250-L308)
shows input file representation → execute fixed command → raw channel write →
EOF. It also shows use of the shared native device key and Apple's known-host
flow. This makes raw file stdin credible, but is **implementation evidence, not
an Apple API guarantee**. Automatic first-use key creation, host prompt digest
format and memory behavior remain critical unresolved physical-device checks.
The implementation of `sharedKeyPair` has not been verified; the SSH action's
nil-key error establishes neither automatic key generation nor its absence.
SSH-key authentication support is not proof of zero-edit key provisioning.
