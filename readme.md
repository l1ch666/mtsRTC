# olcRTC MTS Link Universal Carrier fork

## 2026-05-19 update

The MTS Link core is updated to the same implementation used by XLTD VPN
`1.9.1` / Windows `0.5.1-beta`: hardened guest bot bootstrap, visible-H.264
diagnostics, post-join peer update, silent Opus RTP, and safer token parsing.
The latest patch also keeps incoming and outgoing MTS video m-lines separate
and frames ffmpeg H.264 output as complete Annex-B access units before WebRTC
send.

For a normal VPN profile keep `videochannel` H.264 defaults. To debug whether
the MTS Link lobby renders the bot camera, run the client/server with
`MTS_VIDEO_TEST=1`; that mode publishes synthetic visible H.264 frames and is
not the normal data-carrying VPN path.

Готовый архив-форк `openlibrecommunity/olcrtc` ветки `refactor/universal-carrier` с дополнительным carrier:

```text
5) mtslink
```

Основной быстрый запуск:

```bash
chmod +x script/*.sh
./script/srv.sh --no-cache
```

В меню выбери `5) mtslink`, вставь ссылку MTS Link комнаты и дождись URI в конце. URI совместим с XLTD VPN parser.

Подробно: [MTSLINK.md](MTSLINK.md), [docs/fast.md](docs/fast.md).

## Почему скрипт отличается от upstream

Оригинальный `srv.sh` клонирует GitHub repo в `/tmp`. Для ZIP-форка это опасно: можно случайно собрать upstream/master без MTS Link. Здесь скрипт по умолчанию копирует текущий локальный исходник из распакованного архива. Удалённый Git используется только если явно указать `--repo-url=...`.
