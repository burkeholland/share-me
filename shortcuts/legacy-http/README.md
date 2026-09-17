# Archived legacy HTTP workflow — not the current installer

This directory preserves the previous `ShareMe.cherri`, compiled unsigned plist,
finalizer and static checker solely for comparison. It uses unencrypted LAN
HTTP on port 49321, a receiver import question, clipboard fallback, and multipart
upload. It is **incompatible** with the current generic SSH pairing contract.
Do not install, publish or link this workflow as the current Share Me template.
There is intentionally no publisher script here.

The historical build remains reproducible, using the parent's pinned Cherri
binary/cache and a separate `legacy-work` build directory:

```powershell
.\shortcuts\legacy-http\Build-Shortcut.ps1
.\shortcuts\legacy-http\Test-Shortcut.ps1
```

These tests cover only the old contract; passing them does not establish
ssh-v1 compatibility or security. The original source's name was retained for
historical fidelity, not for side-by-side customer installation.

For the credential-free reusable workflow, customer pairing, publisher process
and honest physical-device release gates, see [the current README](../README.md).
