# olcRTC MTS Link Universal Carrier fork

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
