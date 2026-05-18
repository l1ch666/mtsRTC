# XLTD VPN integration notes

Этот olcRTC fork подогнан под URI parser из XLTD VPN project.

Серверный quick script печатает URI в таком формате:

```text
olcrtc://mtslink?videochannel<video-w=640&video-h=360&video-fps=15&video-bitrate=1200k&video-hw=none&video-codec=qrcode&video-qr-recovery=low>@ENCODED_ROOM_URL#64_HEX_KEY$COMMENT
```

Важные детали совместимости:

- `carrier = mtslink`;
- `transport = videochannel`;
- MTS Link room URL percent-encoded после `@`;
- ключ остаётся 64 hex после `#`;
- комментарий профиля идёт после `$`;
- client-id не обязателен, XLTD parser сам ставит `default`.

Android-клиент из XLTD может распарсить профиль и сохранить его. Для реального runtime `videochannel` на Android нужен olcRTC mobile core с ffmpeg-backed videochannel. Windows-клиент из XLTD рассчитан на запуск `videochannel`, если рядом есть `ffmpeg.exe` и собранный `olcrtc.exe` из этого fork.
