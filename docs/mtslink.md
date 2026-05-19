# olcRTC Universal Carrier + MTS Link fork

## 2026-05-19 core status

This document describes the updated MTS Link fork used by XLTD VPN `1.9.1`.
The carrier now follows the tested guest-bot sequence more closely:
prejoin cookies, guestlogin, login lookup, connection/conference creation,
join-token extraction, publish-token fallback, SFU join, peer update, repeated
pinning, and silent Opus RTP.
The current H.264 videochannel path publishes outgoing tunnel video on its own
sendonly m-line and preserves a separate incoming video receiver; ffmpeg raw
H.264 output is also reframed into complete Annex-B access units before send.

Camera visibility note: the normal VPN path sends tunnel frames through H.264
`videochannel`; those frames may not look like a human camera in the lobby.
`MTS_VIDEO_TEST=1` switches to synthetic visible H.264 frames only for
diagnosing whether MTS Link renders the bot tile.

Этот архив — готовый fork `olcrtc` ветки `refactor/universal-carrier`, подогнанный под XLTD VPN project и MTS Link carrier.

Что изменено:

- добавлен `auth.provider: mtslink`;
- добавлен `internal/engine/mtslink`;
- MTS Link подключается к публичной встрече как гость;
- для MTS Link используется `videochannel` через H.264 media;
- `./script/srv.sh` и `./script/cnc.sh` получили пункт `5) mtslink`;
- при выборе MTS Link transport принудительно становится `videochannel`;
- быстрые скрипты теперь умеют собирать текущий распакованный архив локально, не откатываясь в upstream `master`;
- в конце `srv.sh` печатается `olcrtc://...` URI, совместимый с XLTD VPN parser.

## Быстрый запуск сервера

```bash
chmod +x script/*.sh
./script/srv.sh --no-cache
```

Выбери carrier:

```text
5) mtslink
```

Вставь ссылку постоянной встречи:

```text
https://my.mts-link.ru/j/167846474/19645959806
```

Для публичной гостевой встречи `MTS_COOKIE` оставь пустым. Для закрытой комнаты можно вставить cookie, скрипт передаст его в контейнер через env `MTS_COOKIE`.

В конце сервер выдаст URI вида:

```text
olcrtc://mtslink?videochannel<video-w=640&video-h=360&video-fps=15&video-bitrate=1200k&video-hw=none&video-codec=qrcode&video-qr-recovery=low>@https%3A%2F%2Fmy.mts-link.ru%2Fj%2F167846474%2F19645959806#64hexkey$comment
```

Комнатная ссылка percent-encoded специально: это нужно, чтобы `https://...` внутри URI не ломал парсер клиента.

## Быстрый запуск клиента

```bash
chmod +x script/*.sh
./script/cnc.sh --no-cache
```

Выбери те же параметры:

```text
5) mtslink
```

Transport будет `videochannel` автоматически. Room URL и key должны совпадать с сервером.

## Прямой YAML, если нужен

Быстрый режим не требует ручного YAML, но минимальный конфиг выглядит так:

```yaml
mode: srv
auth:
  provider: mtslink
room:
  id: "https://my.mts-link.ru/j/167846474/19645959806"
crypto:
  key: "64_hex_key_here"
net:
  transport: videochannel
  dns: "8.8.8.8:53"
video:
  codec: qrcode
  width: 640
  height: 360
  fps: 15
  bitrate: "1200k"
  hw: none
  qr_size: 0
  qr_recovery: low
data: data
debug: false
```

## GitHub fork usage

Если зальёшь этот архив в свой fork, можно запускать удалённую ветку так:

```bash
./script/srv.sh \
  --repo-url=https://github.com/YOUR_LOGIN/olcrtc.git \
  --branch=mtslink-universal-carrier \
  --no-cache
```

Если запускаешь из распакованного архива, `--repo-url` не нужен: скрипт берёт локальный исходник.


## MTS Link codec note

MTS Link WebRTC expects an H.264/Opus media shape. For this fork the recommended quick-start mode is `seichannel`, because it publishes H.264 video samples and carries olcRTC data in H.264 SEI payloads. `vp8channel` is left only as a legacy/diagnostic mode and is not expected to be accepted by MTS SFU on H.264-only rooms.
