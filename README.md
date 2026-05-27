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
- Recommended `mtslink + seichannel + multipath` VPN mode, carrying olcRTC
  data inside H.264 SEI payloads across several independent guest-bot lanes.
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
mtslink + seichannel + multipath
```

Recommended SEI profile:

```text
fps=30
batch=8
frag=700
ack-ms=10000
liveness-interval=20s
liveness-timeout=15s
liveness-failures=6
mc-lanes=12
mc-control-lanes=1
mc-connect-parallel=2
mc-min-ready=4
mc-max-streams-per-lane=3
```

For wider lab profiles, both sides may use
`fps=60&batch=64&frag=900&ack-ms=2000&mc-lanes=16&mc-min-ready=8`.
Leave `multipath.lanes` unset only for legacy single-lane compatibility.

Full setup and diagnostics: [MTSLINK.md](MTSLINK.md).

## Recent stability fixes

The mobile API gained the contract the XLTD VPN Android client expects:

- `mobile.SetAutoDNS(candidatesCsv, probeHost) string` — picks the fastest
  responding upstream from a comma-separated DNS list, falls back to the first
  candidate, then to `1.1.1.1:53`. Pairs with `GetAutoDNSUpstream()`. Without
  these symbols `OlcVpnService.runVpnOnce` crashed on every start with
  `NoSuchMethodException`.
- `normalizeTransport` now recognises `videochannel` (and `video`, `vid`,
  `video-channel`, `video_channel`). It used to silently rewrite the carrier
  to `vp8channel` for any unknown value.
- `Check()` and `Ping()` build `TransportOptions` per transport via
  `buildCheckOptions`. SEI probes now pass `seichannel.Options`, video probes
  pass `nil`, VP8/data probes still pass `vp8channel.Options`. Previously a
  hard-coded `vp8channel.Options` value was sent into every transport, which
  the SEI transport rejected with `ErrOptionsTypeMismatch`.

Transport-side hardening:

- `seichannel` remote-track goroutine now polls `closed`/`closeCh` at the top
  of every loop iteration and reads with a 250 ms deadline, bounding the
  per-goroutine exit latency under reconnect storms.
- `internal/engine/mtslink` threads a cancellable context through silent-audio
  pumping, the pin loop, and the shutdown `AudioVideoControl` call; `Pin()`
  is bounded by a 5 s deadline so close cannot strand a 25 s in-flight HTTP.

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
