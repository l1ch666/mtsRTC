# mtsRTC

`mtsRTC` is the XLTD-maintained olcRTC fork used for MTS Link carrier work.
It stays close to olcRTC `refactor/universal-carrier`, but carries the MTS Link
guest-bot and H.264/SEI transport patches required by XLTD VPN.

## Branches

| Branch | Purpose |
| --- | --- |
| `mtslink-universal-carrier` | Current production fork used by XLTD VPN builds. |
| `refactor/universal-carrier-mtslink` | Older integration branch kept for reference. |

The repository default branch should remain `mtslink-universal-carrier` while
this fork is the active XLTD VPN core.

## What This Fork Adds

- `auth.provider: mtslink`.
- MTS Link room resolution for permanent `/j/{userId}/{eventId}` URLs and
  expanded `/stream-new/{sessionId}` URLs.
- Guest bootstrap flow: prejoin cookies, guest login, login lookup,
  connection/conference creation, SFU token extraction, peer update, pinning,
  and silent Opus RTP.
- H.264 media shape expected by MTS Link rooms.
- Recommended `mtslink + seichannel` VPN mode, carrying olcRTC data inside
  H.264 SEI payloads.
- `videochannel` legacy/diagnostic mode for visible QR/video experiments.
- Quick server/client scripts with a `5) mtslink` menu entry.
- XLTD VPN compatible `olcrtc://...` URI output.

## Quick Start

```bash
chmod +x script/*.sh
./script/srv.sh --no-cache
```

Choose carrier `5) mtslink`, paste the MTS Link room URL, and copy the printed
URI into XLTD VPN.

Recommended client/server transport:

```text
mtslink + seichannel
```

Recommended SEI profile:

```text
fps=30
batch=8
frag=700
ack-ms=10000
liveness-interval=20s
liveness-timeout=60s
liveness-failures=3
```

For wider lab profiles, both sides may use `fps=60&batch=64&frag=900&ack-ms=2000`.
The current core still caps each smux frame to a small SEI burst so control
ping/pong is not starved by large page loads.

Full setup and diagnostics: [MTSLINK.md](MTSLINK.md).

## Build Source Selection

The quick scripts default to the local checkout or unpacked archive. They only
download a remote repository when `--repo-url=...` is provided. This prevents a
server rebuild from accidentally falling back to upstream olcRTC without the
MTS Link patches.

Remote example:

```bash
./script/srv.sh \
  --repo-url=https://github.com/l1ch666/mtsRTC.git \
  --branch=mtslink-universal-carrier \
  --no-cache
```

## Compatibility Notes

- `seichannel` is the normal MTS Link VPN path.
- `videochannel` is kept for legacy visible-video diagnostics and requires
  ffmpeg.
- `vp8channel` is a legacy probe and is not expected to work reliably in
  H.264-only MTS Link rooms.
- `datachannel` is not part of the MTS Link carrier path.
