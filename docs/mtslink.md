# MTS Link carrier fork

This fork adds an experimental `mtslink` auth provider and engine on top of
`refactor/universal-carrier`. It joins public MTS Link rooms as a guest,
resolves permanent `/j/{userId}/{eventId}` links to the active stream session,
and exposes an H.264 video path for olcRTC transports.

## Recommended transport

For VPN traffic use `seichannel`:

```yaml
mode: srv
auth:
  provider: mtslink
room:
  id: "https://my.mts-link.ru/j/167846474/19776063563/stream-new/19003191495"
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
  max_payload_size: 1200
  min_delay: 4ms
  max_delay: 18ms
data: data
debug: false
```

The matching client URI form is:

```text
olcrtc://mtslink?seichannel<fps=30&batch=8&frag=700&ack-ms=10000&liveness-interval=20s&liveness-timeout=15s&liveness-failures=6&traffic-max-payload=1200&traffic-min-delay=4ms&traffic-max-delay=18ms>@https%3A%2F%2Fmy.mts-link.ru%2Fj%2F167846474%2F19776063563%2Fstream-new%2F19003191495#64_hex_key_here$MTS%20Link
```

The script helpers expose `mtslink` as a provider choice. When `mtslink` and
`seichannel` are selected together, they default to the conservative values
above.

## Why these defaults

MTS Link H.264 video can deliver data, but it is bursty under VPN traffic.
This fork ACKs individual SEI fragments, limits payload size to avoid large
smux writes, and relaxes control-stream liveness so short video stalls do not
tear down an otherwise usable session.

## Videochannel diagnostics

`videochannel` remains useful for visual diagnostics and QR/tile experiments.
It requires `ffmpeg` on the host:

```yaml
ffmpeg: "ffmpeg"
video:
  codec: qrcode
  width: 640
  height: 360
  fps: 15
  bitrate: "1200k"
  hw: none
```

Use `mts-video-test=1` in a client URI only when you need to confirm that the
MTS lobby shows a visible camera frame.
