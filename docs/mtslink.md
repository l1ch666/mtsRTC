# olcRTC Universal Carrier + MTS Link fork

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
