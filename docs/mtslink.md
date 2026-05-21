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
`seichannel`; those frames are H.264 samples with olcRTC data in SEI payloads
and may not look like a human camera in the lobby.
`MTS_VIDEO_TEST=1` switches to synthetic visible H.264 frames only for
diagnosing whether MTS Link renders the bot tile.

## Minimal stability patch

This branch intentionally stays on the original MTS Link fork history. It is
not rebased onto newer upstream olcRTC. The important stability changes are
limited to:

- `seichannel` now ACKs every fragment and retransmits only missing fragments;
- default MTS Link `seichannel` profile is conservative: `fps=30`, `batch=8`,
  `frag=700`, `ack-ms=10000`;
- MTS Link `seichannel` constrains each smux frame to a small SEI burst
  (`fragment_size * 3`, capped at 7 KiB) so control ping/pong is not starved
  by large page loads;
- MTS Link `seichannel` liveness defaults are relaxed to `20s` interval,
  `60s` timeout and `3` failures because media delivery can stall behind
  browser traffic;
- the client limits MTS Link `seichannel` to three concurrent SOCKS tunnels to
  avoid browser preconnect storms overwhelming the media path;
- old whole-message ACK frames are still accepted for compatibility.

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
olcrtc://mtslink?seichannel<fps=30&batch=8&frag=700&ack-ms=10000>@https%3A%2F%2Fmy.mts-link.ru%2Fj%2F167846474%2F19645959806#64hexkey$comment
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
  transport: seichannel
  dns: "8.8.8.8:53"
sei:
  fps: 30
  batch_size: 8
  fragment_size: 700
  ack_timeout_ms: 10000
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
