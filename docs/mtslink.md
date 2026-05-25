# MTS Link Notes

The canonical MTS Link guide now lives in [../MTSLINK.md](../MTSLINK.md).

Short version:

- Use `auth.provider: mtslink`.
- Use `net.transport: seichannel` plus `multipath.lanes: 12` for normal VPN
  traffic.
- Keep `videochannel` for legacy visible-video diagnostics only.
- Start with `fps=30`, `batch=8`, `frag=700`, `ack-ms=10000`.
- Use `liveness.interval=20s`, `liveness.timeout=15s`, and
  `liveness.failures=6`.
- Keep `traffic.max_payload_size >= sei.fragment_size * 8`.
- URI multipath keys are `mc-lanes`, `mc-control-lanes`,
  `mc-connect-parallel`, `mc-min-ready`, and `mc-max-streams-per-lane`.
- Keep MTS Link room URLs percent-encoded inside `olcrtc://` links.

Example URI:

```text
olcrtc://mtslink?seichannel<fps=30&batch=8&frag=700&ack-ms=10000&liveness-interval=20s&liveness-timeout=15s&liveness-failures=6&traffic-max-payload=5600&traffic-min-delay=4ms&traffic-max-delay=18ms&mc-lanes=12&mc-control-lanes=1&mc-connect-parallel=2&mc-min-ready=4&mc-max-streams-per-lane=3&mts-peer-update=1&mts-silent-audio=1&mts-force-video=1>@https%3A%2F%2Fmy.mts-link.ru%2Fj%2F167846474%2F19645959806#64_hex_key_here$MTS%20Link
```
