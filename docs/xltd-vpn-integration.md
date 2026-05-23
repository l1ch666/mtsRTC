# XLTD VPN Integration Notes

This olcRTC fork is the native core source for XLTD VPN MTS Link profiles.

The quick server script prints XLTD-compatible URIs in this shape:

```text
olcrtc://mtslink?seichannel<fps=30&batch=8&frag=700&ack-ms=10000&liveness-interval=20s&liveness-timeout=60s&liveness-failures=3>@ENCODED_ROOM_URL#64_HEX_KEY$COMMENT
```

Compatibility rules:

- `carrier = mtslink`.
- Recommended VPN transport is `seichannel`.
- `videochannel` is a legacy/diagnostic visual transport and requires ffmpeg.
- The MTS Link room URL is percent-encoded after `@`.
- The key remains a 64-character hex string after `#`.
- The profile comment goes after `$`.
- `client-id` is optional; XLTD VPN defaults it to `default`.

Android and Windows XLTD VPN clients both parse this URI format. Android needs
the combo AAR built with the native media assets for runtime media transports.
Windows packages `ffmpeg.exe` and `olcrtc.exe` next to the GUI.
