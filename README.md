# Share Me

Send files and text between Windows and an iPhone browser or Home Screen web app. **Wails 2 + Go**, one Windows executable, no custom iOS app or certificate setup.

## Use

1. Get [Share Me File Transfer from the Microsoft Store](https://apps.microsoft.com/detail/9NNRDQSDZBR0). It is free, signed by Microsoft, and updates automatically. Or [download Share Me 1.0.1 for Windows x64](https://github.com/burkeholland/share-me/releases/download/v1.0.1/ShareMe-1.0.1-windows-x64.zip), extract the ZIP, and run `ShareMe.exe`. No build tools are required. The ZIP executable is unsigned; do not bypass Windows trust warnings. [Release notes and checksum](https://github.com/burkeholland/share-me/releases/tag/v1.0.1).
2. Scan its QR code with the iPhone Camera app.
3. Click **Connect** on Windows to remember this phone's browser.
4. On the phone, choose photos or files, or send text. Click **Accept** on Windows.

For **Windows to phone**, open **Send**, select a paired phone, and choose files or **Send clipboard text**. Open the phone page and press **Accept Transfer**. Files use the browser's normal save controls; text can be copied explicitly. Nothing is silently saved to Photos or the clipboard.

Pairing is once per browser or installed Home Screen app. The initial QR contains a one-use invitation in its URL fragment; it is not sent to Cloudflare. No code needs to be typed. After pairing, save the phone page as a bookmark or choose **Add to Home Screen**. The saved URL contains a fragment-only reconnect credential so iOS can initialize the Home Screen app's separate storage; fragments are not included in HTTP requests or referrers. Treat the saved URL like a password and do not share it. Use **Add phone** to pair another browser, and **Settings > Phones > Forget** to revoke one. Private browsing, clearing website data, or changing browsers requires pairing again.

In **Settings > Phones**, choose **Rename** to open the name editor. **Save** applies the new name; **Cancel**, **X**, or **Escape** discards the edit.

Removing the last phone refreshes the pairing QR automatically while receiving is running. **Rename** and **Forget** also work while receiving is paused; no new QR is created until you select **Resume**. To reconnect a removed phone, scan the new QR and approve it again; its previous saved connection is no longer authorized. If other phones are still paired, choose **Add phone** first.

Outgoing files are immutable snapshots for the selected phone, not links to the original files. They survive app restarts and remain available until removed on Windows. **Accepted** means the phone requested the transfer, not proof it finished saving. Revocation disconnects the browser and removes its queued offers.

The phone shows **Waiting for PC approval** before sending any file contents. A rejection shows **Declined on PC**, not a connection error. Cancelling while waiting withdraws that phone's request; it cannot accept a transfer or cancel someone else's request.

Incoming files are saved to `%USERPROFILE%\Downloads\Share Me`. The desktop shows the latest 100 transfers. Text stays in the inbox until you explicitly copy it; clipboard access happens only when you press a copy/send button.

Keep both devices on the same trusted network and the PC awake. By default, **Close hides Share Me in the system tray; receiving continues.** Click its tray icon or open the executable again to bring the window back. Right-click the tray icon and choose **Quit** to stop receiving and exit. New transfer requests bring the approval window forward, even when hidden. Share Me draws its own title bar: drag the top edge of the window to move it, double-click it to maximize, or right-click it for the window menu.

**Settings** opens a full page, grouped into Window, Phones, Network, and Save to. The **X** next to its heading or **Escape** returns to the previous transfer view. It includes **Start with Windows** (off by default), **Start minimized to tray** (off by default), **Close button hides to tray** (on by default), and **Minimize button hides to tray** (on by default). The Close and Minimize options are independent. There is also a **Minimize to tray** button in Settings. If the tray cannot be created, the window stays visible and Close exits normally.

Startup applies only to the current Windows user and does not need administrator access. It uses the executable's current location, so keep the executable there; opening it from a new location updates an enabled startup entry. Turning startup off removes only Share Me's entry. Windows Startup apps can independently disable automatic launch. The Microsoft Store version uses a Windows startup task instead of a Run entry, and its **Start with Windows** switch follows **Windows Settings > Apps > Startup**. If startup was turned off there, turn it back on there.

If Windows Firewall asks, allow **Private networks only**. Pause receiving before changing adapters in Settings. If the PC's network address changes while receiving is running, Share Me shows a notice instead of rebinding by itself: select **Pause**, then **Resume**. Resume keeps the selected adapter when it is still available; otherwise it uses the first available private network and saves that choice.

## File safety

Approval is not a malware verdict. Files pass through additional checks:

- Executables, scripts (including Python), macro-enabled Office files, installers, shortcuts, active web content, and disk images are blocked.
- Executable signatures are checked even if a file was renamed.
- **Windows Defender** scans an accepted file before it appears in the inbox. Its custom scan ignores file exclusions and scans archives.
- Windows attachment protection (**Mark of the Web**) is applied before publication.
- Names are sanitized and made unique. Files are never opened or executed automatically.

If scanning or attachment protection fails, the file is not published. Text remains available when Defender is unavailable. Scanner failures are shown to the sender and in Windows.

No scanner guarantees detection of every threat. Only accept files you expect. Defender follows the computer's existing protection policies; Share Me does not change antivirus settings or install another scanner.

## Boundaries

**Browser transfers are encrypted and direct over WebRTC.** The HTTPS phone app and connection broker are hosted at `https://shareme-signaling.burkeholland.workers.dev`. Cloudflare serves the UI and routes encrypted connection setup messages; transferred file contents and transfer text never pass through it. No STUN/TURN relay is configured.

Internet access is needed to load the page and establish a connection. Once connected, losing signaling alone does not interrupt a direct transfer. The hosted JavaScript is part of the trusted application, and Cloudflare can see ordinary website/connection metadata. This is not anonymity from the hosting provider.

Only host UDP candidates on the selected private IPv4 subnet are allowed. No HTTP listener is opened in secure mode. The peer API cannot approve transfers, browse the received inbox, execute programs, or invoke desktop functions. Queued outgoing files are available only to their paired recipient. Pairing identifies a browser credential, not uncloneable physical hardware.

Limits: **2 GiB per file**, **64 KiB per text transfer**, **50 queued files** on the phone. Browser approval requests expire after two minutes without an answer. Keep Safari open during a transfer. A lost response can leave an uncertain result: check the PC inbox before retrying.

The HTTPS bookmark stays the same when the PC's LAN address changes. Guest Wi-Fi isolation, VPNs, and firewalls may prevent a direct connection; there is deliberately no cloud file-relay fallback. Keep the phone page open and the PC awake. If the phone connection drops after suspension, use **Reconnect**. The automated tests cannot cover Safari on a physical iPhone: Windows Playwright WebKit supports the streaming save flow but does not implement WebRTC. Check a real iPhone before each release.

Received history/text and outgoing snapshots are stored unencrypted under `%APPDATA%\ShareMe`; each paired browser's secret and the host key are protected on the PC with Windows DPAPI. On the phone, the browser keeps its reconnect secret in IndexedDB and, after pairing, in the fragment of the page address, where code from the Share Me origin can read it. Connections saved by earlier builds keep their non-extractable WebCrypto key. Existing photos, history, and desktop settings are preserved during upgrades. No automatic inbox deletion is performed. At startup, Share Me removes only its own leftovers from an interrupted transfer: unpublished `.shareme-upload-*` files in the inbox's `.shareme-quarantine` folder and `.shareme-state-*` files in the data folder. Outbox limits are **20 items / 10 GiB total**.

For development compatibility only, an explicit `"transport":"local"` in `%APPDATA%\ShareMe\settings.json` enables the older, unencrypted port **49321** receiver. It has no paired outbox access and must only be used on a trusted LAN. Secure mode is the default and never silently falls back to HTTP.

The app requires the installed **WebView2 Runtime**, normally present on Windows 11. The ZIP download is not code-signed and does not bypass Windows trust warnings. Microsoft signs the Microsoft Store package after certification.

## Build

Requirements: Windows, Go 1.26, Node.js 22.12+, WebView2.

```powershell
.\scripts\build.ps1
```

The build recognizes portable Go at `.tools\go\bin` and uses **Go 1.26.8** with **Wails 2.12.0**. The pin lives in `scripts\common.ps1`. Wails 2's package loader cannot read Go 1.27 export data, and Go 1.25 no longer receives security fixes. Desktop assets are embedded in `build\bin\ShareMe.exe`; there are no external fonts or UI libraries at runtime. The phone UI is hosted on Cloudflare.

The build reads the publisher-owned HTTPS origin from `scripts\service-url.json`; `-ServiceURL https://your-service.workers.dev` overrides it. To publish your own service, build the frontend, run `node scripts\stage-hosted.mjs`, and follow `service\README.md`. The Worker uses the account's existing plan limits; no paid upgrade is required by the configuration.

For a downloadable package, build with `-OutputName ShareMe-1.0.1-windows-x64.exe`, then run `scripts\package-release.ps1 -SourceCommit <40-character-application-source-commit>`. To package an executable that is already built, such as the one in the MSIX, add `-Executable build\bin\ShareMe.exe`. The script validates the production build and packages `ShareMe.exe`, instructions, build metadata, and licenses for Go and every linked module in a ZIP, with a separate SHA-256 checksum. It does not publish, commit, or push anything. Package outputs stay under ignored `build\bin`. Use a source commit matching the built application, and update the landing page's release metadata and measured archive size when publishing a new version.

### Microsoft Store package (MSIX)

After `scripts\build.ps1`, `scripts\package-msix.ps1` packages `build\bin\ShareMe.exe` as `build\bin\ShareMe-1.0.1-windows-x64-development.msix`, an unsigned package with a local development identity. It needs PowerShell 7.2 and the Windows 11 SDK (`makeappx.exe`, `makepri.exe`). The script unpacks the result, compares every file with what it staged, and writes a JSON receipt with the identity and SHA-256 hashes next to the package. The package carries `PRIVACY.md` and the third-party notices.

To run the app under package identity, turn on Windows Developer Mode and register the unpacked copy the script verified. Remove it before packaging again, because packaging replaces that folder.

```powershell
Add-AppxPackage -Register .\build\bin\msix\development\verification\AppxManifest.xml
Get-AppxPackage BurkeHolland.ShareMe.Development | Remove-AppxPackage
```

A packaged process gets its `HKCU` registry writes redirected, so a Run entry would never start it. With package identity, **Start with Windows** switches the manifest's startup task through the Windows StartupTask API instead, and the saved preference follows that task. The `TestPackaged...` tests exercise that and only run with package identity, for example with `Invoke-CommandInDesktopPackage` and a `go test -c` binary. On a PC that has never run the ZIP version, Windows keeps `%APPDATA%\ShareMe` in the package's private folder and deletes it on uninstall; received files in `Downloads\Share Me` stay. Where `%APPDATA%\ShareMe` already exists, the packaged app keeps using it in place.

For a Store upload, copy the values from Partner Center's **Product identity** page into a JSON file with `identityName`, `publisher`, `publisherDisplayName`, `displayName` (the reserved product name), and `packageFamilyName`, then run `scripts\package-msix.ps1 -StoreIdentity <file>`. Keep that file out of the repository. The script checks that the identity name and publisher produce the given package family name. Store packages stay unsigned because Microsoft signs them after certification. The MSIX version is the application version from `wails.json` with a fourth `0`.

## Landing page

The standalone `site` directory follows the compact layout of [Mirror Me](https://burkeholland.github.io/mirror-me/): app screenshots, setup, and source links. It is separate from the Cloudflare phone application and never opens a receiver or connects to a paired phone. It links to the Microsoft Store listing, and Download links to the published Windows release. `site\assets\release.json` records the published archive's URL, size, checksum, and application-source commit.

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
Set-Location frontend
npm ci
npm run build
# Node unit tests for the phone's request slots, the theme bootstrap, and the built desktop CSP:
npm run test:unit
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
