# kenosm

API-гейтвей: настраиваешь любой AI API (upstream) + SOCKS5-прокси рядом — и получаешь endpoint, по которому запросы идут к этому API **через прокси**.

## Что это

- Создаёшь «сервис» — связку `upstream API` + `SOCKS5 proxy` (ip, порт, логин, пароль).
- Сервису присваивается `slug`.
- Получаешь endpoint: `http://<host>/proxy/<slug>/<остальной путь>`.
- Каждый запрос на этот endpoint проксируется на upstream через указанный SOCKS5-прокси.
- Ключ API нейросети и пароль прокси хранятся в базе **зашифрованными** (AES-256-GCM).

## Стек

- **Go** (single binary, нативный reverse-proxy, высокая конкурентность).
- **PostgreSQL 16** (Docker) — хранение конфигов и логов запросов.
- **chi** — роутер; **pgx** — драйвер; **goose** — миграции; **x/net/proxy** — SOCKS5.
- Ключ шифрования — `SECRET_KEY` из окружения.

## Структура

```
cmd/server/            точка входа
internal/
  config/              загрузка конфигурации из env
  crypto/              AES-256-GCM шифрование секретов
  db/                  пул соединений + миграции (goose, embed)
  model/               модели данных
  repository/          доступ к данным (services, request_logs)
  transport/           кэш SOCKS5 http.Transport'ов
  handler/             admin CRUD + gateway (reverse proxy)
  logging/             асинхронное логирование запросов
  server/              сборка роутера
```

## Запуск

```bash
# 1. поднять Postgres
make db-up          # docker compose up -d db

# 2. зависимости
make tidy

# 3. запустить (миграции применятся автоматически)
cp .env.example .env   # при желании задать SECRET_KEY
make run               # go run ./cmd/server  (или make build && ./bin/kenosm)
```

Сервис слушает `:8080` (меняется через `ADDR`).

## API

### Создать сервис

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
```

Поля:
- `slug` — обязателен, `[a-z0-9-]`, уникальный.
- `upstream_url` — обязателен, `http(s)://host[/base/path]`.
- `proxy_host`, `proxy_port` — обязательны.
- `auth_header` / `auth_scheme` — по умолчанию `Authorization: Bearer <key>`.
  Для API без ключа оставь `upstream_api_key` пустым.
- `enabled` — по умолчанию `true`.

### Прочие операции

```bash
curl http://localhost:8080/api/v1/services                     # список
curl http://localhost:8080/api/v1/services/<id>                # один
curl -X PUT http://localhost:8080/api/v1/services/<id> \
  -H 'Content-Type: application/json' -d '{ "enabled": false }'  # частичное обновление
curl -X DELETE http://localhost:8080/api/v1/services/<id>      # удалить
```

### Использовать endpoint

```bash
curl http://localhost:8080/proxy/openai/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}'
```

Запрос уйдёт на `https://api.openai.com/v1/chat/completions` **через SOCKS5-прокси**,
с автоматически подставленным заголовком `Authorization: Bearer sk-...`.
Стриминг (SSE) поддерживается.

## Рой (multi-agent orchestration)

Несколько Gemini-эндпоинтов (каждый со своим SOCKS5-прокси) работают как рой:
получают задачу, распределяют подзадачи между собой, выполняют их параллельно и
собирают итог.

- **Агенты** — это включённые (`enabled`) сервисы из `/api/v1/services`: у каждого
  `upstream_url` = базовый URL Gemini, `upstream_api_key` = ключ, `proxy_*` = SOCKS5.
  Ключи и пароли прокси хранятся в БД зашифрованными.
- Модель воркеров задаётся через `GEMINI_MODEL` (по умолчанию `gemini-3.5-flash`),
  модель планировщика — через `ORCHESTRATOR_MODEL`, список моделей для fallback —
  через `AVAILABLE_MODELS`. Отдельный агент может закрепить свою модель полем `model`.

Запустить задачу:

```bash
curl -X POST http://localhost:8080/api/v1/swarm/run \
  -H 'Content-Type: application/json' \
  -d '{"task":"Create a single-page portfolio website (HTML+CSS+JS) with a dark/light toggle."}'
```

Ответ: `plan` (разбивка), `contract` (общий технический контракт), `assignments`
(модель на агента), `subtasks` (кто из агентов что сделал), `final` (итог).

Как работает: агент-планировщик разбивает задачу на подзадачи, выбирает модель
для каждого агента, задаёт зависимости и формирует общий технический контракт
(точные пути файлов, ID, CSS-классы, сигнатуры). Подзадачи выполняются волнами по
зависимостям (dependency-aware waves); каждая следующая волна получает сводку уже
созданных файлов, чтобы не дублировать работу и не расходиться в именах. При 429
`RESOURCE_EXHAUSTED` запрос автоматически повторяется на более лёгкой модели
(сохраняя ключ и прокси), а не падает после ретраев.

## Безопасность

- Секреты (API-ключ, пароль прокси) шифруются AES-256-GCM ключом, производным от `SECRET_KEY` (SHA-256).
- В API-ответах секреты никогда не отдаются; вместо них — флаги `has_api_key` / `has_proxy_password`.
- Смена `SECRET_KEY` делает старые зашифрованные значения нечитаемыми — храни ключ надёжно.

## Дальше (TODO)

- Аутентификация/авторизация на админ-API и на прокси-эндпоинтах (API-ключи, лимиты).
- Метрики (Prometheus); health-проверка зависимостей (Postgres) — сейчас есть только `/healthz` самого процесса.
- Горизонтальное масштабирование (несколько инстансов за балансировщиком).
