# kenosm

Один бинарь — настольное приложение, HTTP API + reverse-proxy и рой автономных
агентов с настоящим браузером. Агенты сами верстают сайты, ходят по страницам,
делают скриншоты и проверяют свою работу. Настраиваешь любой AI API (Gemini или
любой OpenAI-совместимый, например DeepSeek) — и получаешь готовый endpoint, а
поверх него — многоагентный оркестратор с веб-интерфейсом.

## Возможности

- **Настольное приложение** (`kenosm app`): веб-интерфейс в нативном окне
  (WebKitGTK / Cocoa / Edge WebView2). При первом запуске само генерирует `.env`,
  поднимает PostgreSQL и стартует сервер — без ручной настройки ключей.
- **API-гейтвей**: связка `upstream API` + (опционально) `SOCKS5 proxy`, endpoint
  вида `/proxy/<slug>/<путь>`. Секреты (ключ API и пароль прокси) шифруются
  AES-256-GCM. Прокси необязателен — можно добавлять сервисы напрямую.
- **Рой агентов**: включённые сервисы становятся агентами; планировщик разбивает
  задачу на подзадачи и раздаёт их параллельно (work-stealing, без «волн»).
- **Настоящий браузер** у каждого агента (headless Chromium, живая сессия):
  навигация, клики, ввод текста, скролл, скриншоты, чтение страницы и CAPTCHA.
- **Рабочий стол** (опционально): запуск программ, фокус окон, скриншоты экрана,
  ввод мыши/клавиатуры через `xdotool`/`ydotool`/`uinput` (X11 и Wayland).
- **Выбор рабочей папки**: как в Claude Code — можно указать любую директорию на
  диске, где агенты будут читать и писать файлы (браузер папок в UI).
- **Режим Polish**: саморецензирование — рой переделывает результат, пока
  ревьюер не одобрит (или не кончится бюджет раундов).
- **Дизайн-библиотека** в поставке: ~7500 SVG-иконок (Lucide + Tabler), анимированные
  спиннеры, шаблоны, дизайн-система (OKLCH-токены, 11 эстетик) и поисковая
  UI/UX-база (BM25 по CSV). Агенты обязаны выбирать эстетику и не выдают «AI slop».
- **Живая статистика**: что агенты читают/пишут, стоимость по модели/агенту/запуску,
  таймлайн параллельной работы.
- **Toggle-модули инструментов**: files / shell / browser / desktop — включаются
  отдельно (веб-интерфейс или `DISABLED_MODULES`).
- **Голосовой ввод** (опционально): запись с микрофона и транскрипция через
  внешний `STT_CMD` (`stt/transcribe.py`, faster-whisper).
- **SSE-стрим** прогресса каждого запуска; кнопка «follow» для автопрокрутки.

## Установка и запуск

### Настольное приложение (самый простой путь)

```bash
make build            # соберёт bin/kenosm (CGO) и bin/kenosm-server
./bin/kenosm app      # откроет UI в нативном окне
```

`kenosm app` при первом запуске сам:
1. создаст `.env` (сгенерирует `SECRET_KEY` и `KENOSM_API_KEY`), если его нет;
2. поднимет PostgreSQL через `docker compose up -d db`;
3. применит миграции и запустит сервер;
4. откроет окно с уже авторизованным веб-интерфейсом.

После этого приложение просто запускается и работает — ничего настраивать не надо.

### Терминал / сервер

```bash
make db-up            # Postgres (docker compose up -d db)
make build            # bin/kenosm + bin/kenosm-server
./bin/kenosm          # = kenosm serve — API + гейтвей + веб-интерфейс
./bin/kenosm chat     # терминальный чат с роем (Claude-Code style)
./bin/kenosm chat --serve   # сначала поднять сервер, потом чат
./bin/kenosm setup    # интерактивный мастер (.env + база)
./bin/kenosm version  # версия
```

Веб-интерфейс — на `http://localhost:8080` (вкладки **Chat**, **Tools**, **Stats**).
Сервер слушает `:8080` (меняется через `ADDR`).

Установка в `PATH`: `make install` — скопирует `bin/kenosm` в `~/.local/bin`.

### Быстрый старт без приложения

```bash
make db-up                 # 1. Postgres
cp .env.example .env       # 2. конфиг (опционально — есть дефолты)
make run                   # 3. go run ./cmd/kenosm (миграции применятся сами)
```

Если `KENOSM_API_KEY` пуст, API и прокси работают без авторизации (режим
разработки). Чтобы закрыть доступ — задай ключ (или запусти `kenosm setup`,
который сгенерирует его сам).

## Стек

- **Go** — один бинарь: нативный reverse-proxy, оркестратор, встроенный веб-интерфейс,
  нативное окно (cgo + WebKitGTK/Cocoa/WebView2).
- **PostgreSQL 16** (Docker) — конфиги сервисов, чаты, логи запросов и статистика.
- **chi** (роутер), **pgx** (драйвер), **goose** (миграции), **x/net/proxy** (SOCKS5).
- **chromedp** — headless Chromium для браузера агентов.
- Ключ шифрования — `SECRET_KEY` из окружения (AES-256-GCM).

## Структура

```
cmd/
  kenosm/        точка входа: serve / chat / app / setup / version
  server/        тонкий алиас для `kenosm serve` (бинарник kenosm-server)
  rekey/         перешифровка секретов при смене SECRET_KEY
  cursortest/    одноразовый тестер курсора/кликов рабочего стола (вне сборки)
internal/
  app/           сборка: конфиг, БД, сервисы, роутер, graceful shutdown
  config/        конфигурация из env (+ .env), дефолты
  crypto/        AES-256-GCM
  db/            пул соединений + миграции (goose, embed)
  model/         модели данных
  repository/    доступ к данным (services, request_logs, usage, chats, activity)
  transport/     SOCKS5-кэш http.Transport'ов + локальный SOCKS5-relay (auth-инъекция)
  llm/           клиенты Gemini / OpenAI-совместимые (DeepSeek), фолбэк моделей
  agent/         агент: системный промпт, инструменты, цикл, лимит итераций
  swarm/         оркестратор: планирование, work-stealing, polish-режим
  browser/       браузер агента (chromedp) + прокси на агента
  desktop/       запуск/скриншоты/ввод мыши и клавиатуры
  workspace/     песочница рабочей папки: файлы, shell, cleanup процессов
  handler/       HTTP: admin CRUD, gateway, swarm, chat, workspace, voice, stats
  logging/       асинхронное логирование прокси-запросов
  pricing/       таблица цен моделей + расчёт стоимости
  server/        сборка роутера
  webui/         встроенный фронтенд (embed)
  webview/       cgo-обёртка нативного окна
stt/             опциональный Python-хелпер speech-to-text (venv не коммитится)
assets/          дизайн-библиотека (версионируется, сеется в workspace)
workspace/       runtime-каталог агентов (gitignored, восстанавливается)
```

## Конфигурация

| Переменная | По умолчанию | Назначение |
|---|---|---|
| `ADDR` | `:8080` | адрес HTTP-сервера |
| `DATABASE_URL` | `postgres://kenosm:kenosm@localhost:5432/kenosm?sslmode=disable` | строка подключения |
| `SECRET_KEY` | dev-key (с предупреждением) | мастер-ключ шифрования секретов |
| `KENOSM_API_KEY` | пусто (без авторизации) | bearer-ключ для `/api/v1/*` и `/proxy/*` |
| `GEMINI_MODEL` | `gemini-3.8-flash` | модель воркеров роя |
| `ORCHESTRATOR_MODEL` | `gemini-3.8-flash` | модель планировщика |
| `AVAILABLE_MODELS` | список flash-моделей | модели для фолбэка при 429 |
| `DISABLED_MODULES` | пусто | модули, выключенные при старте (`files,shell,browser,desktop`) |
| `WORKSPACE_DIR` | `workspace` | runtime-каталог агентов |
| `ASSETS_DIR` | `assets` | каталог дизайн-библиотеки (сеется в workspace) |
| `COMMAND_TIMEOUT` | `30s` | таймаут `run_command` агента |
| `SHUTDOWN_TIMEOUT` | `30s` | дедлайн graceful-shutdown |
| `MAX_CONCURRENT_RUNS` | `1` | лимит одновременных запусков роя |
| `POLISH_MAX_ROUNDS` | `5` | максимум раундов саморецензирования |
| `AGENT_MAX_ITERATIONS` | `50` | максимум шагов (tool-call) агента на подзадачу |
| `LOG_RETENTION_DAYS` | `30` | срок хранения логов прокси (0 — не чистить) |
| `MODEL_PRICES` | встроенная таблица | переопределение цен (USD/1M токенов) |
| `STT_CMD` | пусто (голос выключен) | команда транскрипции аудио (см. `stt/transcribe.py`) |

`kenosm app` и `kenosm setup` генерируют `SECRET_KEY`/`KENOSM_API_KEY` в `.env`
автоматически, так что вручную их задавать не нужно.

## API

### Сервисы (агенты)

```bash
curl -X POST http://localhost:8080/api/v1/services \
  -H 'Content-Type: application/json' \
  -d '{
    "slug": "openai",
    "name": "OpenAI via proxy",
    "upstream_url": "https://api.openai.com/v1",
    "upstream_api_key": "sk-...",
    "auth_header": "Authorization",
    "auth_scheme": "Bearer",
    "proxy_host": "1.2.3.4",
    "proxy_port": 1080,
    "proxy_username": "user",
    "proxy_password": "pass"
  }'

curl http://localhost:8080/api/v1/services                # список
curl http://localhost:8080/api/v1/services/<id>           # один
curl -X PUT http://localhost:8080/api/v1/services/<id> \
  -H 'Content-Type: application/json' -d '{ "enabled": false }'  # частичное обновление
curl -X DELETE http://localhost:8080/api/v1/services/<id> # удалить
```

Поля: `slug` (`[a-z0-9-]`, уникальный), `upstream_url` (`http(s)://host[/base]`).
`proxy_host`/`proxy_port` **необязательны** — без них запросы идут напрямую.
`auth_header`/`auth_scheme` по умолчанию `Authorization: Bearer <key>`; `enabled` —
по умолчанию `true`. Секреты в ответах не возвращаются — вместо них флаги
`has_api_key`/`has_proxy_password`.

### Прокси-гейтвей

```bash
curl http://localhost:8080/proxy/openai/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}'
```

Запрос уходит на `https://api.openai.com/v1/chat/completions` (при заданном прокси —
через SOCKS5) с автоматически подставленным `Authorization: Bearer sk-...`.
Стриминг (SSE) поддержан.

### Запуск задачи роем

```bash
curl -X POST http://localhost:8080/api/v1/swarm/run \
  -H 'Content-Type: application/json' \
  -d '{"task":"Сделай одностраничный сайт-портфолио (HTML+CSS+JS) с переключателем темы.",
       "mode":"swarm"}'   # single | swarm | polish (по умолчанию swarm)
```

Ответ: `{"run_id": "..."}`. Прогресс — SSE:

```bash
curl -N http://localhost:8080/api/v1/swarm/run/<run_id>/events
curl -X POST http://localhost:8080/api/v1/swarm/run/<run_id>/stop   # остановить
curl http://localhost:8080/api/v1/models                            # доступные модели
```

### Генерация промпта

Сырую идею можно отдать модели, чтобы она перефразировала её в подробный,
самодостаточный промпт для роя (в веб-интерфейсе — кнопка **Generate prompt**
в поле ввода чата; результат можно скопировать или сразу подставить в задачу):

```bash
curl -X POST http://localhost:8080/api/v1/prompt/generate \
  -H 'Content-Type: application/json' \
  -d '{"idea":"хочу лендинг для крипто-кошелька"}'
```

Ответ: `{"prompt": "…"}`. Используется модель планировщика; это одиночный вызов
LLM — без воркеров и без браузера.

### Чаты (постоянные диалоги)

```bash
curl http://localhost:8080/api/v1/chats                     # список
curl -X POST http://localhost:8080/api/v1/chats             # новый чат
curl http://localhost:8080/api/v1/chats/<id>                # чат с сообщениями
curl -X POST http://localhost:8080/api/v1/chats/<id>/messages \
  -H 'Content-Type: application/json' \
  -d '{"text":"Привет","mode":"single"}'                   # single | swarm | polish
curl -X DELETE http://localhost:8080/api/v1/chats/<id>      # удалить
```

Необязательное поле `agent` (slug сервиса) сужает работу до одного конкретного
агента (аналог `mode:"single"`, но с явным выбором модели). Режим сообщения:
- **single** — работает один агент (быстро, с полной историей).
- **swarm** — весь рой: планировщик делит задачу на параллельные подзадачи.
- **polish** — рой + цикл саморецензирования до одобрения.

### Workspace, рабочая папка и статистика

```bash
curl http://localhost:8080/api/v1/workspace/files            # список файлов
curl http://localhost:8080/api/v1/workspace/files/<path>     # файл (картинки — base64)
curl -X POST http://localhost:8080/api/v1/workspace/clear    # очистить workspace

# выбор рабочей папки (как в Claude Code) — любая директория на диске
curl http://localhost:8080/api/v1/workspace/root             # текущая рабочая папка
curl -X POST http://localhost:8080/api/v1/workspace/root \
  -H 'Content-Type: application/json' -d '{"path":"/home/me/project"}'  # сменить
curl http://localhost:8080/api/v1/fs/home                    # домашний каталог пользователя
curl "http://localhost:8080/api/v1/fs/dirs?path=/home/me"    # подпапки (браузер папок)

curl http://localhost:8080/api/v1/activity?limit=100         # активность (read/write/…)
curl http://localhost:8080/api/v1/activity?run_id=<id>       # активность одного запуска
curl http://localhost:8080/api/v1/runs/<id>/timeline         # таймлайн параллельной работы
curl http://localhost:8080/api/v1/stats                      # суммарные траты
```

Смена рабочей папки отклоняется, пока идёт запуск (чтобы не оставить агентов без
файлов на середине задачи).

### Голосовой ввод

```bash
curl -X POST http://localhost:8080/api/v1/voice/start        # начать запись с микрофона
curl -X POST http://localhost:8080/api/v1/voice/stop         # остановить и транскрибировать
curl -X POST http://localhost:8080/api/v1/transcribe         # транскрибировать готовый клип
```

Работает в нативном окне (WebKitGTK), где браузерный `getUserMedia` запрещён.
Требует внешнюю команду в `STT_CMD` (см. готовый `stt/transcribe.py`, faster-whisper);
пока она не задана, голосовой ввод возвращает «not configured».

## Рой: как это работает

**Агенты** — это включённые (`enabled`) сервисы: `upstream_url` = базовый URL модели,
`upstream_api_key` = ключ, `proxy_*` = SOCKS5 (опционально). У каждого агента свой
браузер со своим прокси (через локальный SOCKS5-relay, подставляющий авторизацию).

Планировщик сначала решает, нужен ли рой: разговорный запрос («Привет», короткий
вопрос) отвечает напрямую. Иначе строит **структуру** — подзадачи, зависимости,
модель на агента и общий технический контракт (точные пути, ID, CSS-классы,
сигнатуры), стараясь дать минимум по подзадаче на агента.

Подзадачи выполняются **всегда параллельно**: единая очередь готовых задач
(work-stealing) без барьера «волн». Освободившийся агент сразу берёт следующую
готовую подзадачу. Если готовых задач нет, а работа осталась (слишком
последовательный план), планировщик **на лету** перераскладывает её мельче — так
рой масштабируется от 3 до 50+ агентов. Результаты пишутся в общий журнал, который
читают все; при 429 `RESOURCE_EXHAUSTED` запрос повторяется на более лёгкой модели
(ключ и прокси сохраняются).

У агента есть настоящий **браузер** (живая сессия на весь запуск): навигация,
клики, ввод, скролл, скриншоты. Перед выдачей результата агент обязан проверить
работу — открыть страницу, «протыкать» кнопки/ссылки, прокрутить и заскринить
каждый шаг. Задача «сделай скриншот/прочитай страницу» **обязана** идти через
браузер: агентам запрещено подменять работу `curl`/`urllib`/PIL или копировать
старые скриншоты.

**Рабочий стол** (модуль `desktop`, опционально): запуск программ, поиск/фокус окон,
скриншоты, ввод мыши и клавиатуры. Окна и скриншоты — через `xdotool` + скриншотер
(`maim`/`scrot`/`gnome-screenshot`/`grim`/`import`); клавиатура — через `ydotool`
(ядро/uinput, надёжно на Wayland); мышь — абсолютный указатель `/dev/uinput`
(работает и на X11, и на Wayland). Зависимости: `pacman -S xdotool maim ydotool xprop`.
Отсутствующие части отключаются с предупреждением, не ломая старт.

## Режим Polish

`mode:"polish"` (или переключатель в чате) запускает цикл саморецензирования:
рой выполняет задачу, отдельный ревьюер оценивает результат
(`{"approved":bool,"feedback":"..."}`) и, если не одобрено, рой переделывает с
полученным фидбеком. До `POLISH_MAX_ROUNDS` раундов (по умолчанию 5). Полезно для
«вылизанных» результатов: агент часами докручивает, пока ревьюер не скажет «ок».

## Дизайн-библиотека

В каталоге `assets/` лежит версионируемая дизайн-библиотека, которая при старте и
после `workspace/clear` копируется в `<workspace>/assets/` (переменная `ASSETS_DIR`).
Агенты читают её и обязаны строить «дорогие», а не AI-slop-страницы:

- `assets/icons/lucide/` (~2000) и `assets/icons/tabler/` (~5500) — SVG-иконки.
- `assets/spinners/` — анимированные SVG-спиннеры.
- `assets/templates/` — эталонные страницы (3D-hero, glass-dashboard, landing).
- `assets/DESIGN.md` — дизайн-система: 11 эстетик, анти-паттерны, OKLCH-токены,
  типографика, движение, чек-лист полировки.
- `assets/uiux/` — поисковая UI/UX-база (BM25 по CSV, stdlib-only Python):

```bash
python3 assets/uiux/scripts/search.py "<запрос>" --domain style   # style|color|typography|product|ux|landing|chart|icons|google-fonts|react|web|gsap
python3 assets/uiux/scripts/search.py "<продукт>" --design-system --variance 7 --motion 5 --density 6
```

Правило агентов: сначала выбрать **одну** эстетику и заявить её, затем цвет/шрифт/
отступы/тени из дизайн-системы; цвета — OKLCH-переменные от одного `--brand-hue`;
реальная пара шрифтов с Google Fonts; иконки инлайнятся (`stroke="currentColor"`);
настоящее 3D — Three.js с cdnjs. Готовую страницу агент обязан открыть в браузере и
заскринить.

## Безопасность

- Секреты шифруются AES-256-GCM ключом от `SECRET_KEY` (SHA-256); в ответах не отдаются.
- Смена `SECRET_KEY` делает старые значения нечитаемыми — храни ключ; для смены
  без потери данных есть `go run ./cmd/rekey` (env: `OLD_SECRET`, `NEW_SECRET`).
- `KENOSM_API_KEY` закрывает `/api/v1/*` и `/proxy/*`: заголовок
  `Authorization: Bearer <key>` или `X-API-Key` (SSE — `?key=`). Пустой ключ — открытый режим.
  `kenosm app`/`kenosm setup` генерируют ключ в `.env` автоматически.
- `.env` (с ключами и строкой БД) — в `.gitignore`, в репозиторий не попадает.
- `MAX_CONCURRENT_RUNS=1` по умолчанию не даёт агентам разных запусков конфликтовать
  за общий workspace.
- Логи прокси (`request_logs`) подчищаются старше `LOG_RETENTION_DAYS`.

## Дальше (TODO)

- Rate limiting на клиентов (IP/ключ).
- Метрики Prometheus; health-проверка зависимостей (Postgres) — сейчас только `/healthz`.
- Горизонтальное масштабирование (несколько инстансов за балансировщиком).
