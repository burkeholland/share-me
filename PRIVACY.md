# Share Me privacy notice

Effective October 1, 2026. This notice covers Share Me 1.0 and later for
Windows, the phone page it opens, and the Share Me website.

Share Me sends files and text straight between your Windows PC and a phone
browser on the same network. It has no account, advertising, analytics, or
telemetry. The publisher never receives your files, your text, your file
names, or your device names.

## What you transfer

Files and text travel directly between the paired phone browser and your PC
over an encrypted WebRTC connection on your local network. They are not
uploaded to, stored on, or relayed through any server.

Nothing is transferred without a decision. Each file or text sent from the
phone must be accepted on the PC. Each file or text sent from the PC must be
accepted on the phone.

Share Me reads the Windows clipboard only when you select **Send clipboard
text**, and writes to it only when you select a copy button.

## The connection service

The phone page is served from
`https://shareme-signaling.burkeholland.workers.dev`, a Cloudflare Worker run
by the publisher. The same service passes the small setup messages the two
devices need to find each other.

The service can see the IP addresses of the PC and the phone, the time of the
connection, a random identifier for your PC, and encrypted setup messages it
cannot read. It cannot see files, text, file names, device names, or pairing
secrets. Pairing invitations and saved reconnect credentials live in the part
of the address after `#`, which browsers do not send to servers.

To limit abuse, the service counts connection attempts with a short-lived key
made from a hash of the IP address and the current minute. It keeps no log of
requests, uses no analytics, and stores no transfer data. Cloudflare, as the
hosting provider, handles ordinary connection information under the
[Cloudflare privacy policy](https://www.cloudflare.com/privacypolicy/).

## Files on your PC

- Received files are saved in `%USERPROFILE%\Downloads\Share Me` and stay
  there until you delete them.
- `%APPDATA%\ShareMe\state.json` lists received items: name, size, type,
  time, and saved location. It also holds the full contents of received text.
- `%APPDATA%\ShareMe\outbox` holds a copy of each file or text you queued for
  a phone, until you remove it or forget that phone.
- `%APPDATA%\ShareMe\settings.json` holds your preferences and the selected
  network address.
- `%APPDATA%\ShareMe\peer-identity.dpapi` holds the PC's identity key and each
  paired phone's name and reconnect secret. Windows encrypts this file for
  your Windows account.

The history, text, and outbox copies are not encrypted by Share Me. They rely
on your Windows account permissions and any disk encryption you use. Share Me
does not delete received files or history automatically. When it starts, it
removes only its own unfinished temporary files left by an interrupted
transfer.

The interface uses Microsoft Edge WebView2, which keeps its own browser data
on the PC. **Start with Windows** adds a startup entry for your Windows
account only.

### Microsoft Store version

On a PC that has never run the ZIP version, Windows keeps the `%APPDATA%`
files above in the app's private folder under `%LOCALAPPDATA%\Packages`.
Uninstalling the app deletes that folder, including paired phones and queued
items. Files already saved in `Downloads\Share Me` are not removed.

If the ZIP version ran on the PC before, the Store version keeps using the
existing `%APPDATA%\ShareMe` folder, and uninstalling leaves it in place.
Delete it yourself if you no longer need it.

Microsoft delivers and updates the Store version. Through Partner Center,
Microsoft gives the developer aggregate reports such as installs, usage, and
crashes. Windows collects that information under the
[Microsoft privacy statement](https://privacy.microsoft.com/privacystatement)
and your Windows diagnostic data settings. Share Me itself sends no analytics
or telemetry.

## Data on your phone

The phone's browser stores what it needs to reconnect: the identifier of your
PC, an identifier and name for the phone, and a reconnect secret. It also
installs a service worker that lets the browser save incoming files.

If you bookmark the page or add it to the Home Screen, the saved address
contains the reconnect secret. Treat that address like a password and do not
share it. Clearing the website's data removes the saved connection.
**Settings > Phones > Forget** on the PC revokes a phone at any time.

## File scanning

Before a received file is saved, Share Me asks Microsoft Defender on your PC
to scan it and marks the file as coming from another computer. Defender works
under your PC's existing protection settings, which can include Microsoft's
cloud protection and sample submission. Share Me does not change those
settings.

## Your local network

Other devices on the same network can observe that a connection is being made.
Approval prompts on the PC show the phone's local IP address and the name its
browser reported, such as "iPhone". That name is not proof of identity.

## Website

The Share Me website is hosted by GitHub Pages and loads its fonts from Google
Fonts. It sets no cookies and has no analytics of its own. GitHub and Google
receive ordinary request information such as your IP address under their own
privacy policies.

## Changes and questions

Changes to this notice are published in this file with a new effective date.

Questions can be raised in the
[Share Me issue tracker](https://github.com/burkeholland/share-me/issues).
Issues are public. Do not attach personal files, saved phone addresses, or the
contents of the `%APPDATA%\ShareMe` folder.
