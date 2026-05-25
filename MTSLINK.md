# MTS Link Carrier

This fork adds an experimental MTS Link carrier to olcRTC. It joins a public
MTS Link room as a guest, negotiates the browser-like H.264/Opus media shape,
and carries VPN traffic through H.264 SEI payloads.

Recommended VPN transport:

```text
mtslink + seichannel + multipath
```

`videochannel` is still available for legacy visible-video diagnostics, but it
is not the default VPN path.

For browser traffic, one visual SEI stream is too narrow. Use 10-16 lanes for
normal testing. The lane pool starts several independent MTS Link guest bots in
the same room and tags each SEI frame with a lane id, so each smux session sees
only its own H.264 SEI packets. Old single-lane links still work because
`multipath.lanes` is opt-in.

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
  timeout: 15s
  failures: 6
traffic:
  max_payload_size: 5600
  min_delay: 4ms
  max_delay: 18ms
multipath:
  lanes: 12
  control_lanes: 1
  connect_parallelism: 2
  min_ready: 4
  max_streams_per_lane: 3
ffmpeg: "ffmpeg"
debug: false
```

Run:

```bash
./olcrtc server-mtslink.yaml
```

## Client URI

Conservative default:

```text
olcrtc://mtslink?seichannel<fps=30&batch=8&frag=700&ack-ms=10000&liveness-interval=20s&liveness-timeout=15s&liveness-failures=6&traffic-max-payload=5600&traffic-min-delay=4ms&traffic-max-delay=18ms&mc-lanes=12&mc-control-lanes=1&mc-connect-parallel=2&mc-min-ready=4&mc-max-streams-per-lane=3&mts-peer-update=1&mts-silent-audio=1&mts-force-video=1>@https%3A%2F%2Fmy.mts-link.ru%2Fj%2F167846474%2F19645959806#64_hex_key_here$MTS%20Link
```

Wider lab profile:

```text
olcrtc://mtslink?seichannel<fps=60&batch=64&frag=900&ack-ms=2000&liveness-interval=20s&liveness-timeout=15s&liveness-failures=6&traffic-max-payload=7200&traffic-min-delay=4ms&traffic-max-delay=18ms&mc-lanes=16&mc-control-lanes=1&mc-connect-parallel=3&mc-min-ready=8&mc-max-streams-per-lane=3&mts-peer-update=1&mts-silent-audio=1&mts-force-video=1>@https%3A%2F%2Fmy.mts-link.ru%2Fj%2F167846474%2F19645959806#64_hex_key_here$MTS%20Link
```

The MTS Link room URL is percent-encoded after `@` so `https://...` does not
break the `olcrtc://` parser.

## Stability Defaults

The current fork is intentionally conservative for MTS Link:

- `seichannel` ACKs individual fragments and retransmits only missing
  fragments.
- MTS Link `seichannel` liveness defaults in XLTD clients are `20s` interval,
  `15s` timeout, and `6` failures.
- `traffic.max_payload_size` should be at least `fragment_size * 8`. The
  example above uses `700 * 8 = 5600`.
- `multipath.lanes` enables the v2 SEI lane header. Both peers must use the
  same lane count. Leave it unset for legacy single-lane links.

## Multipath Parameters

| URI key | YAML field | Recommended |
| --- | --- | --- |
| `mc-lanes` | `multipath.lanes` | `12`, use `16` for wider lab runs |
| `mc-control-lanes` | `multipath.control_lanes` | `1` |
| `mc-connect-parallel` | `multipath.connect_parallelism` | `2` or `3` |
| `mc-min-ready` | `multipath.min_ready` | `4` for 12 lanes, `8` for 16 lanes |
| `mc-max-streams-per-lane` | `multipath.max_streams_per_lane` | `3` |

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
- recommended transport: `seichannel`;
- diagnostic visual transport: `videochannel`;
- release branch for Xray alpha clients: `alpha/xray-0.0.1`.
