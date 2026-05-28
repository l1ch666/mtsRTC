# MTS Link Carrier

This fork adds an experimental MTS Link carrier to olcRTC. It joins a public
MTS Link room as a guest, negotiates the browser-like H.264/Opus media shape,
and carries VPN traffic through H.264 SEI payloads.

Recommended VPN transport:

```text
mtslink + seichannel
```

`videochannel` is available for legacy visible-video diagnostics but is not the
default VPN path. Single-channel `seichannel` is the stable supported
configuration; multipath proved unreliable and has been removed.

## Room Link

Use a public MTS Link room URL:

```text
https://my.mts-link.ru/j/167846474/19645959806
```

If automatic session discovery fails, open the room in a browser and copy the
expanded URL that contains `/stream-new/<sessionId>`:

```text
https://my.mts-link.ru/j/167846474/19645959806/stream-new/18867526566
```

## Server YAML

```yaml
mode: srv
auth:
  provider: mtslink
room:
  id: "https://my.mts-link.ru/j/167846474/19645959806"
  channel: default
crypto:
  key: "64_hex_key_here"
net:
  transport: seichannel
  dns: "1.1.1.1:53"
sei:
  fps: 30
  batch_size: 8
  fragment_size: 700
  ack_timeout_ms: 10000
liveness:
  interval: 20s
  timeout: 60s
  failures: 3
ffmpeg: "ffmpeg"
debug: false
```

Run:

```bash
./olcrtc server-mtslink.yaml
```

## Client URI

```text
olcrtc://mtslink?seichannel<fps=30&batch=8&frag=700&ack-ms=10000&liveness-interval=20s&liveness-timeout=60s&liveness-failures=3&mts-peer-update=1&mts-silent-audio=1&mts-force-video=1>@https%3A%2F%2Fmy.mts-link.ru%2Fj%2F167846474%2F19645959806#64_hex_key_here$MTS%20Link
```

The MTS Link room URL is percent-encoded after `@` so `https://...` does not
break the `olcrtc://` parser.

## Stability Defaults

- `seichannel` ACKs individual fragments and retransmits only missing
  fragments.
- Single-channel mode is the stable, tested configuration. Multipath proved
  unreliable and has been removed from all profiles.
- MTS Link `seichannel` liveness defaults: `20s` interval, `60s` timeout,
  `3` failures.
- SEI defaults: `fps=30`, `batch_size=8`, `fragment_size=700`,
  `ack_timeout_ms=10000`.

## Diagnostics

- `MTS_DEBUG=1` prints bootstrap request misses.
- `MTS_VIDEO_TEST=1` publishes a synthetic visible H.264 camera. Use it only to
  check whether the MTS lobby renders the bot tile.
- `MTS_VIDEO_CODEC=h264` is the normal diagnostic camera codec.
- `MTS_FORCE_VIDEO=0`, `MTS_PEER_UPDATE=0`, or `MTS_SILENT_AUDIO=0` disable
  their compatibility paths.

Camera visibility note: the normal VPN path sends SEI carrier frames, not a
human-looking camera image. A static or synthetic visible tile is only a
diagnostic signal; the useful payload is in H.264 SEI.

## XLTD VPN Integration

XLTD VPN builds this core from `l1ch666/mtsRTC` branch
`mtslink-universal-carrier`. Keep URI and option names aligned with the Android
and Windows clients:

- product/client name: `XLTD VPN`;
- olcRTC carrier name: `mtslink`;
- recommended transport: `seichannel` (single channel);
- diagnostic visual transport: `videochannel`;
- release branch for Xray alpha clients: `alpha/xray-0.0.1`.
