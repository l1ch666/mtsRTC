# MTS Link Notes

The canonical MTS Link guide now lives in [../MTSLINK.md](../MTSLINK.md).

Short version:

- Use `auth.provider: mtslink`.
- Use `net.transport: seichannel` for VPN traffic (single channel; multipath
  was removed as unstable).
- Keep `videochannel` for legacy visible-video diagnostics only.
- Start with `fps=30`, `batch=8`, `frag=700`, `ack-ms=10000`.
- Use `liveness.interval=20s`, `liveness.timeout=60s`, and
  `liveness.failures=3`.
- Keep MTS Link room URLs percent-encoded inside `olcrtc://` links.
- `script/srv.sh` prompts for liveness values and includes them in both
  `server.yaml` and the printed client URI.

Example URI:

```text
olcrtc://mtslink?seichannel<fps=30&batch=8&frag=700&ack-ms=10000&liveness-interval=20s&liveness-timeout=60s&liveness-failures=3&mts-peer-update=1&mts-silent-audio=1&mts-force-video=1>@https%3A%2F%2Fmy.mts-link.ru%2Fj%2F167846474%2F19645959806#64_hex_key_here$MTS%20Link
```
