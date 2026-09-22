# Share Me

Send files and text between Windows and an iPhone browser. **Wails 2 + Go**, one Windows executable, no custom iOS app, PWA installation, or certificate setup.

## Use

1. [Download the Windows x64 preview](https://github.com/burkeholland/share-me/releases/download/v0.3.0-preview.1/ShareMe-0.3.0-windows-x64.zip), extract the ZIP, and run `ShareMe.exe`. No build tools are required. The executable is unsigned; do not bypass Windows trust warnings. [Release notes and checksum](https://github.com/burkeholland/share-me/releases/tag/v0.3.0-preview.1).
2. Scan its QR code with the iPhone Camera app.
3. Click **Connect** on Windows to remember this phone's browser.
4. On the phone, choose photos or files, or send text. Click **Accept** on Windows.

For **Windows to phone**, open **Send**, select a paired phone, and choose files or **Send clipboard text**. Open the phone page and press **Accept Transfer**. Files use the browser's normal save controls; text can be copied explicitly. Nothing is silently saved to Photos or the clipboard.

Pairing is once per browser. The initial QR contains a one-use invitation in its URL fragment; it is not sent to Cloudflare. No code needs to be typed. Save the phone page as a bookmark. Use **Add phone** to pair another browser, and **Settings > Phones > Forget** to revoke one. Private browsing, clearing website data, or changing browsers requires pairing again. Do not share an unused pairing QR.

In **Settings > Phones**, choose **Rename** to open the name editor. **Save** applies the new name; **Cancel**, **X**, or **Escape** discards the edit.

Removing the last phone refreshes the pairing QR automatically. To reconnect a removed phone, scan the new QR and approve it again; its previous saved connection is no longer authorized. If other phones are still paired, choose **Add phone** first.

Outgoing files are immutable snapshots for the selected phone, not links to the original files. They survive app restarts and remain available until removed on Windows. **Accepted** means the phone requested the transfer, not proof it finished saving. Revocation disconnects the browser and removes its queued offers.

The phone shows **Waiting for PC approval** before sending any file contents. A rejection shows **Declined on PC**, not a connection error. Cancelling while waiting withdraws that phone's request; it cannot accept a transfer or cancel someone else's request.

Incoming files are saved to `%USERPROFILE%\Downloads\Share Me`. The desktop shows the latest 100 transfers. Text stays in the inbox until you explicitly copy it; clipboard access happens only when you press a copy/send button.

Keep both devices on the same trusted network and the PC awake. **Close hides Share Me in the system tray; receiving continues.** Click its tray icon or open the executable again to bring the window back. Right-click the tray icon and choose **Quit** to stop receiving and exit. New transfer requests bring the approval window forward, even when hidden.

**Settings** opens a full page, grouped into Window, Phones, Network, and Save to. Its top-right **X** or **Escape** returns to the previous transfer view. It includes **Start with Windows** (off by default), **Start minimized to tray** (off by default), and **Minimize button hides to tray** (on by default). The last option controls the title-bar minimize button; Close always hides to tray. There is also a **Minimize to tray** button in Settings. If the tray cannot be created, the window stays visible and Close exits normally.

Startup applies only to the current Windows user and does not need administrator access. It uses the executable's current location, so keep the executable there; opening it from a new location updates an enabled startup entry. Turning startup off removes only Share Me's entry. Windows Startup apps can independently disable automatic launch.

If Windows Firewall asks, allow **Private networks only**. Pause receiving before changing adapters in Settings.

## File safety

Approval is not a malware verdict. Files pass through additional checks:

- Executables, scripts (including Python), macro-enabled Office files, installers, shortcuts, active web content, and disk images are blocked.
- Executable signatures are checked even if a file was renamed.
- **Windows Defender** scans an accepted file before it appears in the inbox. Its custom scan ignores file exclusions and scans archives.
- Windows attachment protection (**Mark of the Web**) is applied before publication.
- Names are sanitized and made unique. Files are never opened or executed automatically.

If scanning or attachment protection fails, the file is not published. Text remains available when Defender is unavailable. Scanner failures are shown to the sender and in Windows.

No scanner guarantees detection of every threat. Only accept files you expect. Defender follows the computer's existing protection policies; Share Me does not change antivirus settings or install another scanner.

## iPhone share-sheet Shortcut

**One generic Shortcut is distributed to everyone. Customers do not create a workflow, edit actions, enter SSH commands, or paste PC addresses.** A website alone cannot register as an iOS share-sheet destination, so Apple Shortcuts provides that entry point.

Once the publisher has signed and released the template:

1. Pair the phone browser normally, then choose **Add to share sheet**.
2. Tap **Add Shortcut** to install the shared template.
3. Return to the phone page and tap **Connect Shortcut**, then **Open Shortcuts**.
4. Confirm the PC's host key if Apple prompts. The Shortcut configures itself and remembers this PC.
5. Use **Photos or Files > Share > Share Me**. Each item still needs acceptance on Windows.

The setup link carries a short-lived, one-use enrollment over the already-paired browser connection and then Apple's local `shortcuts:` URL scheme. The SSH key belongs to Apple Shortcuts; it is not embedded in the template. Only nonsecret PC configuration is saved by the workflow. The enrollment is bound to the paired browser, and **Forget** on Windows revokes its Shortcut access as well.

Share-sheet transfers use an opt-in, file-only encrypted SSH receiver inside Share Me, on the selected private IPv4 address at port **49322**. It does not install Windows OpenSSH or expose a command shell, SFTP, port forwarding, or remote execution. **Settings > Share sheet > Share-sheet transfers** disables it. Normal browser transfers remain on WebRTC. Allow Share Me through Windows Firewall on Private networks if prompted.

Once configured, the Shortcut sends files directly over the local network without using Cloudflare or opening Safari. If the PC's IP address changes, repeat **Connect Shortcut** from the paired browser; no workflow editing is needed.

### Publisher step

**Apple signing/publication and physical-iPhone verification are still required before distribution.** This is one publisher task, not something every customer does. The installer stays unavailable until a release is registered; no fabricated iCloud link or unsigned installation is advertised.

The `shortcuts` directory contains the workflow source, compiled unsigned artifact, static checks, and Mac publisher tooling. Sign the generic artifact with `Publish-Shortcut.command` on a Mac. The signed file's matching `ShareMe.release.json` identifies the `ssh-v1` protocol and hash so an old HTTP workflow cannot be advertised for the encrypted receiver.

After verifying the workflow on an iPhone, register its real iCloud sharing link:

```powershell
.\scripts\register-shortcut.ps1 -ICloudURL "YOUR_REAL_APPLE_ICLOUD_SHORTCUT_URL" -VerifiedOnIPhone
.\scripts\build.ps1
```

Alternatively, include `shortcuts\Share Me.shortcut` and its matching release metadata, then run `.\scripts\register-shortcut.ps1 -SignedArtifact -VerifiedOnIPhone` after completing the device checks. Signing alone leaves the installer unpublished. The verification flag records the publisher's manual confirmation; it is not an automated iPhone test. The build copies the signed bytes unchanged and preserves **Share Me.shortcut** as the download filename. Rebuild and stage/deploy the hosted assets after registering a release. Apple still verifies the actual signature during import.

The Shortcut requests approval, polls for the decision, and sends the original file through SSH stdin only after acceptance. File contents are not embedded in the SSH command or encoded into a browser URL. Shared URLs are sent as text, not fetched. A receipt is returned only after the existing safety/scanning pipeline finishes.

Before publication, verify that a phone **without an existing Shortcuts SSH key** can complete setup without editing an action, and that the final artifact imports with the exact name **Share Me**. Automatic first-use key generation and imported naming are not established by the available implementation evidence. Also check file-provider conversions, first-connect key prompts, large videos, multiple items, cancellation, and iOS background limits; the server's 2 GB file limit is not a promise that iOS can process a 2 GB share reliably.

References: [Apple signing tools](https://support.apple.com/guide/shortcuts-mac/run-shortcuts-from-the-command-line-apd455c82f02/mac), [iPhone sharing](https://support.apple.com/guide/shortcuts/share-shortcuts-apdf01f8c054/ios).

## Boundaries

**Browser transfers are encrypted and direct over WebRTC; the optional Shortcut uses direct encrypted SSH.** The HTTPS phone app and connection broker are hosted at `https://shareme-signaling.burkeholland.workers.dev`. Cloudflare serves the UI and routes encrypted connection setup messages; transferred file contents, transfer text, and Shortcut enrollment credentials never pass through it. No STUN/TURN relay is configured.

Internet access is needed to load the page and establish a connection. Once connected, losing signaling alone does not interrupt a direct transfer. The hosted JavaScript is part of the trusted application, and Cloudflare can see ordinary website/connection metadata. This is not anonymity from the hosting provider.

Only host UDP candidates on the selected private IPv4 subnet are allowed. No HTTP listener is opened in secure mode. The peer API cannot approve transfers, browse the received inbox, execute programs, or invoke desktop functions. Queued outgoing files are available only to their paired recipient. Pairing identifies a browser credential, not uncloneable physical hardware.

Limits: **2 GiB per file**, **64 KiB per text transfer**, **50 queued files** on the phone. Browser approval requests expire after two minutes without an answer. Keep Safari open during a transfer. A lost response can leave an uncertain result: check the PC inbox before retrying.

The HTTPS bookmark stays the same when the PC's LAN address changes. Guest Wi-Fi isolation, VPNs, and firewalls may prevent a direct connection; there is deliberately no cloud file-relay fallback. Keep the phone page open and the PC awake. If the phone connection drops after suspension, use **Reconnect**. Safari on a physical iPhone still needs a final device-specific check; Windows Playwright WebKit supports the streaming save flow but does not implement WebRTC.

Received history/text and outgoing snapshots are stored unencrypted under `%APPDATA%\ShareMe`; browser pairing secrets and the host key are protected with Windows DPAPI. Browser keys are non-extractable WebCrypto keys in IndexedDB. Existing photos, history, and desktop settings are preserved during upgrades. No automatic inbox deletion is performed. Outbox limits are **20 items / 10 GiB total**.

For development compatibility only, an explicit `"transport":"local"` in `%APPDATA%\ShareMe\settings.json` enables the older, unencrypted port **49321** receiver. It has no paired outbox access and must only be used on a trusted LAN. Secure mode is the default and never silently falls back to HTTP.

The app requires the installed **WebView2 Runtime**, normally present on Windows 11. It is not code-signed and does not bypass Windows trust warnings.

## Build

Requirements: Windows, Go 1.25+, Node.js 22.12+, WebView2.

```powershell
.\scripts\build.ps1
```

The build recognizes portable Go at `.tools\go\bin` and uses **Go 1.25.14** with **Wails 2.12.0**. Wails 2's package loader cannot read Go 1.27 export data. Desktop assets are embedded in `build\bin\ShareMe.exe`; there are no external fonts or UI libraries at runtime. The phone UI is hosted on Cloudflare.

The build reads the publisher-owned HTTPS origin from `scripts\service-url.json`; `-ServiceURL https://your-service.workers.dev` overrides it. To publish your own service, build the frontend, run `node scripts\stage-hosted.mjs`, and follow `service\README.md`. The Worker uses the account's existing plan limits; no paid upgrade is required by the configuration.

For a downloadable package, build with `-OutputName ShareMe-0.3.0-windows-x64.exe`, then run `scripts\package-release.ps1 -SourceCommit <40-character-application-source-commit>`. The script validates the production build and packages `ShareMe.exe`, instructions, build metadata, and licenses for Go and every linked module in a ZIP, with a separate SHA-256 checksum. It does not publish, commit, or push anything. Package outputs stay under ignored `build\bin`. Use a source commit matching the built application, and update the landing page's release metadata and measured archive size when publishing a new version.

## Landing page

The standalone `site` directory follows the compact layout of [Mirror Me](https://burkeholland.github.io/mirror-me/): app screenshots, setup, and source links. It is separate from the Cloudflare phone application and never opens a receiver or connects to a paired phone. Download links to the published Windows preview; the Apple Shortcut remains unpublished. `site\assets\release.json` records the published archive's URL, size, checksum, and application-source commit.

Preview and check it from the repository root, using the existing frontend Playwright dependencies:

```powershell
node site\tests\serve.mjs
node --test site\tests\site.test.mjs
node site\tests\browser-review.mjs
```

The preview is at `http://127.0.0.1:4173/`. Publish the static contents of `site` to GitHub Pages or another static host; no frontend build is required to serve it. The site uses Postrboard CSS and Google Fonts with system fallbacks. It has no analytics, embedded app, or transfer API calls.

After changing the application UI, rebuild the frontend and refresh the committed light/dark screenshots with `node site\tests\capture.mjs`. Captures use example content and a harmless `example.invalid` QR, never a live pairing invitation or personal files. Screenshot source hashes use normalized line endings; image and vendored CSS hashes preserve exact bytes. The static checks reject stale captures.

## Development checks

```powershell
go test ./...
go test -race ./...
go vet ./...

# Optional native tray lifecycle check (creates and removes its own temporary tray icon):
$env:SHAREME_TRAY_TEST = "1"
go test -run Tray .
Remove-Item Env:\SHAREME_TRAY_TEST

# Optional check against the installed Windows Defender:
$env:SHAREME_SCAN_TEST = "1"
go test ./internal/safety
```

Browser tests exercise the real receiver with temporary inboxes and local approval decisions over stdin. They do not add an approval endpoint to the network API or disable file scanning.

```powershell
.\scripts\stage-shortcut.ps1
Set-Location frontend
npm ci
npm run build
Set-Location ..
go build -tags production -o .tools\bin\ShareMe-test.exe .
# In another terminal: Set-Location service; npm run dev
node scripts\stage-hosted.mjs
Set-Location frontend
npx playwright install chromium webkit
npm test
```

`--headless --dev-loopback --ip 127.0.0.1 --control-stdio` is for local integration testing. It emits pending snapshots and accepts JSON decisions (`id`, `accept`) through local stdin. That control mode cannot bind a LAN interface. Use an isolated `--data` directory per process; no two receivers may own the same data directory.

`--headless-peer` exercises real pairing, encrypted signaling, and direct transfers with an isolated data directory and local stdin decisions. Its explicit private `--ip` must match the browser's LAN interface. Browser tests select a physical private adapter; override `SHAREME_TEST_IP` if necessary. Set `SHAREME_TEST_ORIGIN` to the deployed HTTPS origin to repeat the secure tests against public signaling. Local test-control commands are never exposed over the network.
