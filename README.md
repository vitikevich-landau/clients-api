# Clients API

Учебный, но написанный по продакшн-правилам HTTP-сервис на Go: **gin + PostgreSQL (pgx) + JWT + slog**.

Ключевая механика — **модерация правок**: обычный пользователь не редактирует карточку клиента напрямую, он **предлагает** изменение, и оно применяется только после одобрения администратором.

Код подробно прокомментирован — не «что делает строка», а **почему сделано именно так**: где гонки, где утечки, где дыры в правах и как их закрывают.

> ### 🧭 [Интерактивный разбор: путь запроса сквозь сервис](docs/request-flow.html)
>
> Пошаговая страница — открывается прямо из файла, без сборки и интернета.
> Три сценария (создание предложения, одобрение в транзакции, ветка ошибки),
> 48 шагов: подсвеченный слой «луковицы», живые объекты в памяти на каждом шаге,
> код и точное место в проекте.
>
> ```bash
> xdg-open docs/request-flow.html    # Linux
> open docs/request-flow.html        # macOS
> ```

---

## Оглавление

- [Быстрый старт](#быстрый-старт)
- [Архитектура](#архитектура)
- [Структура проекта](#структура-проекта)
- [Схема базы данных](#схема-базы-данных)
- [Справочник API](#справочник-api)
- [Сквозной сценарий](#сквозной-сценарий-от-предложения-до-применения)
- [Как устроены middleware](#как-устроены-middleware)
- [Как устроено одобрение правки](#как-устроено-одобрение-правки)
- [Конфигурация](#конфигурация)
- [Миграции](#миграции)
- [Тесты](#тесты)
- [Разработка без Docker](#разработка-без-docker)
- [Технологический стек](#технологический-стек)
- [Что здесь намеренно упрощено](#что-здесь-намеренно-упрощено)
- [Где что искать в коде](#где-что-искать-в-коде)

---

## Быстрый старт

Нужен только Docker.

```bash
git clone https://github.com/vitikevich-landau/clients-api.git
cd clients-api
make up
```

Всё: Postgres поднимется, миграции применятся автоматически, демо-данные зальются.

| Что | Адрес |
|---|---|
| API | http://localhost:8080/api/v1 |
| Swagger UI | http://localhost:8080/swagger/index.html |
| Проба живучести | http://localhost:8080/healthz |
| Проба готовности | http://localhost:8080/readyz |

```bash
make logs    # смотреть логи
make ps      # статус контейнеров
make psql    # залезть в базу
make down    # остановить (данные сохранятся)
make clean   # остановить и стереть данные
make help    # все команды
```

### Демо-учётки

> ⚠️ Заводятся миграцией `00004_seed_demo_data.sql`. **Перед реальным использованием эту миграцию нужно удалить** — пароли опубликованы здесь и потому скомпрометированы.

| Email | Пароль | Роль |
|---|---|---|
| `admin@example.com` | `admin12345` | admin |
| `user@example.com` | `user12345` | user |

Плюс три готовых клиента, чтобы было с чем играть.

---

## Архитектура

### Три слоя

```
      HTTP-запрос
           │
           ▼
┌──────────────────────┐
│  handler (транспорт) │  разобрать вход, вызвать сервис, отдать JSON
│                      │  знает про HTTP, не знает про SQL
└──────────┬───────────┘
           ▼
┌──────────────────────┐
│  service (логика)    │  бизнес-правила, транзакции, права
│                      │  не знает ни про HTTP, ни про SQL
└──────────┬───────────┘
           ▼
┌──────────────────────┐
│  repository (данные) │  только SQL и маппинг в структуры
│                      │  не знает, зачем его позвали
└──────────┬───────────┘
           ▼
      PostgreSQL
```

**Правило:** зависимости идут строго вниз. Handler → Service → Repository. Наоборот — никогда.

Зачем: `service` можно вызвать из CLI, gRPC или фоновой задачи, ничего не переписывая. Postgres можно заменить, не трогая логику. И каждый слой тестируется отдельно.

### Интерфейсы объявлены у потребителя

Идиома Go: интерфейс принадлежит тому, кто его **использует**, а не тому, кто реализует.

- `service/interfaces.go` объявляет, что сервисам нужно от репозиториев
- `handler/interfaces.go` объявляет, что хендлерам нужно от сервисов

Поэтому `service` вообще не импортирует `repository`. И поэтому тесты подставляют заглушки и работают без базы за миллисекунды.

### Путь одного запроса

`POST /api/v1/clients/{id}/suggestions`

```
1. Recovery       ловит панику где угодно ниже
2. RequestID      генерирует X-Request-ID
3. Logger         вшивает request_id в логгер, кладёт его в ctx
4. Timeout        ставит дедлайн на context запроса
5. Auth           разбирает JWT, кладёт AuthUser в контекст
   ↓
6. handler        parseUUIDParam → bindJSON → вызов сервиса
   ↓
7. service        проверка payload, проверка клиента, создание
   ↓
8. repository     INSERT ... RETURNING
   ↓
9. pgx            запрос в Postgres (ctx едет сюда же — отмена работает)
   ↑
   ответ поднимается обратно тем же путём:
   handler кодирует JSON → Logger пишет строку со статусом и длительностью
```

---

## Структура проекта

```
cmd/api/main.go              композиционный корень: собрать всё и запустить

internal/
  config/                    env → структура, валидация на старте
  logger/                    настройка slog + логгер в context
  apperr/                    доменные ошибки и их перевод в HTTP-коды
  response/                  единый формат ответов и ошибок
  db/                        пул pgx, транзакции, миграции
  model/                     доменные структуры + DTO запросов/ответов
  repository/                SQL: users, clients, suggestions
  service/                   бизнес-логика, транзакции, JWT
  handler/                   gin-хендлеры, биндинг, сборка роутов
  middleware/                recovery, requestid, logger, timeout, cors, auth, role

migrations/                  SQL-миграции goose (вшиты в бинарник через //go:embed)
docs/                        сгенерированная спецификация OpenAPI
```

`internal/` — не соглашение, а фича компилятора Go: пакеты оттуда физически нельзя импортировать из чужого модуля.

---

## Схема базы данных

### `users` — кто пользуется системой

| Колонка | Тип | Заметки |
|---|---|---|
| `id` | UUID | генерируется в Go, а не в базе |
| `email` | TEXT UNIQUE | всегда в нижнем регистре |
| `password_hash` | TEXT | bcrypt, cost=12 |
| `role` | TEXT | `user` \| `admin`, с CHECK на уровне базы |
| `created_at`, `updated_at` | TIMESTAMPTZ | |

### `clients` — справочник клиентов

| Колонка | Тип | Заметки |
|---|---|---|
| `id` | UUID | |
| `full_name` | TEXT NOT NULL | CHECK на непустоту |
| `email`, `phone`, `company`, `notes` | TEXT NULL | в Go — `*string` |
| `status` | TEXT | `active` \| `archived` |
| `deleted_at` | TIMESTAMPTZ NULL | **мягкое удаление**: NULL = запись живая |

Индексы:
- `idx_clients_alive` — частичный, только живые строки
- `ux_clients_email_alive` — уникальность email **среди живых**, по `lower(email)`
- `idx_clients_*_trgm` — GIN + `pg_trgm` для поиска по подстроке (`ILIKE '%x%'`)

### `client_suggestions` — предложения правок

| Колонка | Тип | Заметки |
|---|---|---|
| `id` | UUID | |
| `client_id` | UUID FK | ON DELETE CASCADE |
| `author_id` | UUID FK | кто предложил |
| `payload` | **JSONB** | только изменяемые поля |
| `status` | TEXT | `pending` \| `approved` \| `rejected` |
| `reviewer_id` | UUID FK NULL | ON DELETE SET NULL |
| `review_comment` | TEXT NULL | обязателен при отклонении |
| `created_at`, `reviewed_at` | TIMESTAMPTZ | |

Ограничения и индексы:
- `chk_reviewed_consistency` — рассмотренное предложение обязано иметь `reviewed_at`, ожидающее — не иметь
- `ux_suggestions_one_pending_per_author_client` — **частичный** уникальный индекс: один автор не может держать два нерассмотренных предложения по одному клиенту
- составные индексы под запросы «очередь модерации» и «мои предложения»

### Почему payload — JSONB

Пользователь правит один телефон → в payload летит только телефон. С отдельными колонками пришлось бы заводить `new_full_name`, `new_email`, `new_phone`… и добавлять колонку сюда при каждом новом поле клиента.

Главное же — JSONB позволяет различить **три** состояния поля:

```jsonc
{"phone": "+7 900 111-22-33"}  // поменять на это значение
{"phone": null}                // ОЧИСТИТЬ поле
{}                             // не трогать вовсе
```

Обычный `*string` в Go этого не умеет: и «не передали», и «передали null» дают одинаковый `nil`. Поэтому в `model/optional.go` есть дженерик `Optional[T]`, который различает присутствие ключа и его значение. Ровно такая семантика описана стандартом **JSON Merge Patch (RFC 7396)**.

---

## Справочник API

Базовый путь: `/api/v1`. Токен передаётся заголовком `Authorization: Bearer <token>`.

### Без авторизации

| Метод | Путь | Описание |
|---|---|---|
| `GET` | `/healthz` | проба живучести (вне `/api/v1`) |
| `GET` | `/readyz` | проба готовности, проверяет базу (вне `/api/v1`) |
| `POST` | `/auth/register` | регистрация (всегда роль `user`) |
| `POST` | `/auth/login` | получить JWT |

### Любой аутентифицированный пользователь

| Метод | Путь | Описание |
|---|---|---|
| `GET` | `/auth/me` | актуальные данные о себе из базы |
| `GET` | `/clients` | список: поиск, фильтр, сортировка, пагинация |
| `GET` | `/clients/{id}` | карточка клиента |
| `POST` | `/clients/{id}/suggestions` | **предложить** правку |
| `GET` | `/suggestions/my` | мои предложения и их статусы |
| `GET` | `/suggestions/{id}` | одно предложение (своё; админ — любое) |

### Только администратор

| Метод | Путь | Описание |
|---|---|---|
| `POST` | `/admin/clients` | создать клиента |
| `PUT` | `/admin/clients/{id}` | изменить напрямую (полная замена) |
| `DELETE` | `/admin/clients/{id}` | мягко удалить |
| `GET` | `/admin/suggestions` | очередь модерации (все авторы) |
| `POST` | `/admin/suggestions/{id}/approve` | одобрить и **применить** |
| `POST` | `/admin/suggestions/{id}/reject` | отклонить (комментарий обязателен) |

### Параметры списка клиентов

| Параметр | По умолчанию | Допустимо |
|---|---|---|
| `search` | — | подстрока в имени или компании |
| `status` | — | `active`, `archived` |
| `limit` | `20` | 1…100 |
| `offset` | `0` | ≥ 0 |
| `sort_by` | `created_at` | `created_at`, `updated_at`, `full_name` |
| `sort_order` | `desc` | `asc`, `desc` |

> `sort_by` ограничен белым списком **дважды** — тегом `oneof` в DTO и картой в репозитории. Имя колонки нельзя передать в SQL параметром `$1`, его приходится вклеивать в текст запроса, а значит, без белого списка это прямая SQL-инъекция.

### Формат ошибок — одинаковый на всех роутах

```json
{
  "error": {
    "code": "validation_error",
    "message": "request validation failed",
    "details": {
      "full_name": "is required",
      "email": "must be a valid email address"
    },
    "request_id": "3e12e206-0ad4-4608-98bb-878e7502907d"
  }
}
```

| Код | HTTP | Когда |
|---|---|---|
| `validation_error` | 400 | не прошли проверку входные данные |
| `unauthorized` | 401 | нет токена / токен невалиден или истёк |
| `forbidden` | 403 | токен есть, но прав не хватает |
| `not_found` | 404 | объект не существует |
| `method_not_allowed` | 405 | путь есть, метод не тот |
| `conflict` | 409 | нарушена уникальность или бизнес-правило |
| `timeout` | 504 | не уложились в отведённое время |
| `internal_error` | 500 | наша ошибка (подробности только в логе) |

`request_id` дублируется в заголовке `X-Request-ID`. По нему находится вся история запроса в логах.

---

## Сквозной сценарий: от предложения до применения

Скопируй целиком в терминал после `make up`.

### 1. Входим двумя пользователями

```bash
API=http://localhost:8080/api/v1

ADMIN=$(curl -s -X POST $API/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"admin@example.com","password":"admin12345"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["access_token"])')

USER=$(curl -s -X POST $API/auth/login -H 'Content-Type: application/json' \
  -d '{"email":"user@example.com","password":"user12345"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["access_token"])')
```

### 2. Смотрим карточку клиента

```bash
CID=cccccccc-0000-4000-8000-000000000001
curl -s $API/clients/$CID -H "Authorization: Bearer $USER" | python3 -m json.tool
```

```json
{
  "full_name": "Ivan Petrov",
  "email": "ivan.petrov@example.com",
  "phone": "+7 900 000-00-01",
  "company": "Acme LLC",
  "notes": "Key account, prefers email",
  "status": "active"
}
```

### 3. Пользователь предлагает правку

Меняем телефон **и** очищаем компанию. Остальные поля не трогаем — их просто нет в теле запроса.

```bash
SID=$(curl -s -X POST $API/clients/$CID/suggestions \
  -H "Authorization: Bearer $USER" -H 'Content-Type: application/json' \
  -d '{"phone":"+7 999 888-77-66","company":null}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["id"])')
```

**Карточка клиента при этом НЕ изменилась** — проверь тем же GET из шага 2.

### 4. Админ смотрит очередь модерации

```bash
curl -s "$API/admin/suggestions?status=pending" \
  -H "Authorization: Bearer $ADMIN" | python3 -m json.tool
```

```json
{
  "items": [{
    "status": "pending",
    "payload": { "company": null, "phone": "+7 999 888-77-66" },
    "changed_fields": ["company", "phone"],
    "author_email": "user@example.com",
    "client_name": "Ivan Petrov"
  }],
  "pagination": { "limit": 20, "offset": 0, "total": 1 }
}
```

`author_email` и `client_name` приходят из `JOIN` — иначе на 20 предложений было бы 41 запрос к базе (проблема N+1).

### 5. Админ одобряет

```bash
curl -s -X POST $API/admin/suggestions/$SID/approve \
  -H "Authorization: Bearer $ADMIN" -H 'Content-Type: application/json' \
  -d '{"comment":"verified by phone"}' | python3 -m json.tool
```

### 6. Проверяем результат

```bash
curl -s $API/clients/$CID -H "Authorization: Bearer $USER" | python3 -m json.tool
```

```json
{
  "full_name": "Ivan Petrov",              // ← не трогали, осталось
  "email": "ivan.petrov@example.com",      // ← не трогали, осталось
  "phone": "+7 999 888-77-66",             // ← заменено
  "company": null,                         // ← очищено явным null
  "notes": "Key account, prefers email",   // ← не трогали, осталось
  "status": "active"
}
```

Вот ради чего был нужен `Optional[T]`.

### 7. Что не получится сделать

```bash
# Пользователь лезет в админку → 403
curl -s $API/admin/suggestions -H "Authorization: Bearer $USER"
# {"error":{"code":"forbidden","message":"insufficient permissions", ...}}

# Одобрить повторно → 409
curl -s -X POST $API/admin/suggestions/$SID/approve -H "Authorization: Bearer $ADMIN"
# {"error":{"code":"conflict","message":"suggestion is already approved", ...}}

# Два нерассмотренных предложения по одному клиенту → 409
# (после создания первого, до его рассмотрения)
# {"error":{"code":"conflict","message":"you already have a pending suggestion for this client", ...}}

# Мусор во входных данных → 400 с разбором по полям
curl -s -X POST $API/admin/clients -H "Authorization: Bearer $ADMIN" \
  -H 'Content-Type: application/json' -d '{"email":"nope","status":"deleted"}'
# {"error":{"code":"validation_error","details":{
#   "full_name":"is required",
#   "email":"must be a valid email address",
#   "status":"must be one of: active, archived"}}}
```

---

## Как устроены middleware

### Модель «луковицы»

Middleware выполняются не «до» и не «после» хендлера, а **вокруг** него:

```
Запрос →  [Recovery]
            [RequestID]
              [Logger]
                [Timeout]
                  [Auth]
                    [RequireAdmin]
                      → ХЕНДЛЕР ←
                    [RequireAdmin]
                  [Auth]
                [Timeout]
              [Logger]
            [RequestID]
          [Recovery]   → Ответ
```

Внутри middleware `c.Next()` означает «провалиться глубже». Код **до** `c.Next()` — путь внутрь, код **после** — путь наружу. Именно поэтому логгер умеет писать «запрос занял 45 мс»: время он засекает до, а считает после.

`c.Abort()` обрывает цепочку — следующие middleware и хендлер не выполнятся.

> ⚠️ **Классическая ошибка:** `c.Abort()` **не делает** `return` из функции. Он лишь помечает цепочку прерванной. Забыл `return` после `Abort()` — и код ниже продолжит выполняться.

### Что делает каждый

| Middleware | Зачем |
|---|---|
| `Recovery` | паника → 500 вместо падения всего процесса. Отдельно распознаёт обрыв соединения клиентом — это не ошибка сервиса |
| `RequestID` | подхватывает `X-Request-ID` от прокси или генерирует свой. Входящее значение **валидируется** — иначе перевод строки в заголовке позволил бы подделать строки в логах |
| `Logger` | одна структурная запись на запрос. Уровень выбирается по статусу: 5xx → Error, 4xx → Warn, 2xx → Info |
| `Timeout` | подменяет `context` запроса на контекст с дедлайном. Дедлайн доезжает до pgx, и запрос **отменяется в самом Postgres** |
| `CORS` | разрешение для браузерных клиентов с других доменов. Отвечает на preflight `OPTIONS` |
| `Auth` | разбирает JWT, кладёт `AuthUser` в контекст. Отвечает на вопрос «**кто** ты» |
| `RequireRole` | проверяет роль. Отвечает на вопрос «**можно** ли тебе». Это разные вопросы и разные middleware |

### Почему проверка прав в middleware, а не в хендлерах

Middleware вешается на **группу** роутов:

```go
admin := v1.Group("/admin")
admin.Use(middleware.Auth(deps.Tokens), middleware.RequireAdmin())
```

Добавил новый роут в группу — он автоматически защищён. Забыть невозможно. Проверка внутри каждого хендлера, наоборот, забывается легко: скопировал соседний обработчик, удалил «лишнюю» строчку — и роут открыт. Так появляется большинство дыр в правах доступа.

### Логи

Структурные, в stdout. Локально — цветной текст, в проде — JSON:

```json
{"time":"...","level":"INFO","msg":"request handled","request_id":"92a71c99-...",
 "method":"POST","path":"/api/v1/clients/.../suggestions","status":201,
 "duration_ms":12,"response_size":487,"client_ip":"172.18.0.1"}
```

Логгер с уже вшитым `request_id` кладётся в `context`, поэтому **любой** слой пишет через `logger.FromContext(ctx)` и получает те же поля автоматически. Расследование инцидента — один поиск по `request_id`.

**Что в логи не попадает никогда:** пароли, токены, тела запросов, персональные данные клиентов. Логируются идентификаторы (`user_id`, `client_id`), а не содержимое.

---

## Как устроено одобрение правки

`SuggestionService.Approve` — самый важный метод проекта. Два изменения обязаны произойти **вместе или никак**:

1. карточка клиента меняется согласно payload;
2. предложение переводится в статус `approved`.

Без транзакции возможен разрыв: упали между шагами — и получили «предложение одобрено, а данные не изменились». Такие рассинхроны потом ищут неделями.

```go
s.tx.WithTx(ctx, func(ctx context.Context, tx db.Querier) error {
    // 1. читаем предложение С БЛОКИРОВКОЙ строки (SELECT ... FOR UPDATE)
    suggestion, err := s.suggestions.GetByIDForUpdate(ctx, tx, id)

    // 2. проверяем состояние ПОСЛЕ блокировки — до неё оно уже устарело бы
    if !suggestion.IsPending() { return apperr.Conflict(...) }

    // 3. читаем клиента ТОЖЕ с блокировкой
    client, err := s.clients.GetByIDForUpdate(ctx, tx, suggestion.ClientID)

    // 4. накладываем payload (Optional: значение / null / отсутствие)
    suggestion.Payload.Apply(client)

    // 5. сохраняем карточку
    if err := s.clients.Update(ctx, tx, client); err != nil { return err }

    // 6. помечаем предложение одобренным
    return s.suggestions.Review(ctx, tx, id, StatusApproved, reviewer.ID, comment)
})   // nil → COMMIT, ошибка → ROLLBACK
```

Что здесь важного, кроме самой транзакции:

- **`FOR UPDATE`.** Два админа одновременно жмут «Одобрить» — без блокировки оба увидят `pending`, оба применят payload, правка наложится дважды. Блокировка выстраивает их в очередь.
- **Единый порядок захвата блокировок** — сначала предложение, потом клиент. Всегда. Если один код берёт A→B, а другой B→A, две транзакции заблокируют друг друга насмерть.
- **`AND status = 'pending'` внутри UPDATE** — вторая линия защиты поверх блокировки, на случай вызова вне транзакции.
- **Откат по `context.WithoutCancel`.** Если запрос отменили, `ctx` уже мёртв, и обычный `ROLLBACK` по нему просто не отправится — транзакция повиснет на сервере, удерживая блокировки.
- **Перечитывание результата — после коммита.** Чем короче транзакция, тем меньше время удержания блокировок.

---

## Конфигурация

Всё через переменные окружения (12-factor). Полный список с пояснениями — в [`.env.example`](.env.example).

```bash
cp .env.example .env
```

Конфиг читается **один раз при старте** и полностью валидируется. Чего-то не хватает или значения противоречат друг другу — приложение **падает сразу** с внятным сообщением. Лучше не запуститься, чем работать наполовину.

Проверяется в том числе:

- `HTTP_REQUEST_TIMEOUT` обязан быть меньше `HTTP_WRITE_TIMEOUT` — иначе сервер оборвёт соединение раньше, чем мы отдадим аккуратный 504;
- `JWT_SECRET` не короче 32 символов;
- `DB_MIN_CONNS` ≤ `DB_MAX_CONNS`;
- в `APP_ENV=prod` запрещены `CORS="*"` и `DB_SSLMODE=disable`.

### Про размер пула соединений

```
количество_реплик_сервиса × DB_MAX_CONNS  <  max_connections Postgres
```

У Postgres по умолчанию `max_connections ≈ 100`. Три реплики по 50 соединений = 150 → база начнёт отказывать. На этом обжигаются регулярно.

---

## Миграции

Лежат в `migrations/`, применяются `goose`, **вшиты в бинарник** через `//go:embed`. Бинарник самодостаточен: нет ситуации «выкатили сервис, а папку с миграциями забыли».

Сервис применяет их сам при старте, под **консультативной блокировкой Postgres** (`pg_advisory_lock`) — иначе одновременно стартующие реплики подрались бы за схему.

Ручное управление:

```bash
make migrate-status                      # что применено
make migrate-up                          # применить
make migrate-down                        # откатить последнюю
make migrate-create name=add_some_field  # создать новую
```

> В больших системах миграции выносят в отдельный шаг деплоя (init-контейнер, job в CI): миграция может идти дольше, чем health-check ждёт старта пода, а приложению в проде обычно не дают прав на `ALTER TABLE`. Здесь компромисс в пользу простоты запуска.

**Правило:** база никогда не меняется руками через консоль. Только миграцией, которая лежит в git и применяется на всех окружениях одинаково.

---

## Тесты

```bash
make test        # с детектором гонок
make test-cover  # + отчёт о покрытии в coverage.html
```

Три уровня, каждый падает по своей причине:

| Где | Что проверяет |
|---|---|
| `model/suggestion_test.go` | семантика `Optional`: отсутствие / null / значение. Наложение payload, валидация, сериализация в JSONB |
| `service/suggestion_service_test.go` | бизнес-логика с заглушками репозиториев: сценарий одобрения, конфликты, права. **Без базы и Docker** |
| `handler/client_test.go` | HTTP через `httptest`: коды ответов, формат ошибок, работа auth-middleware, защита от утечки внутренних ошибок |

Заглушки написаны руками (`service/mocks_test.go`) — на таком объёме читаются лучше сгенерированных.

Флаг `-race` включает детектор гонок. Он замедляет тесты, но находит ошибки многопоточности, которые иначе всплывают раз в месяц в проде и невоспроизводимы.

---

## Разработка без Docker

Нужен локальный Postgres или только его контейнер:

```bash
docker compose up -d postgres   # только база
cp .env.example .env            # DB_HOST=localhost уже прописан
make run                        # go run ./cmd/api
```

Полезное:

```bash
make build   # бинарник в bin/api с версией из git
make fmt     # форматирование
make vet     # go vet
make lint    # golangci-lint (нужен установленный)
make swag    # перегенерировать Swagger из комментариев
make tidy    # go mod tidy
```

Установка инструментов:

```bash
go install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
go install github.com/swaggo/swag/cmd/swag@latest
go install github.com/pressly/goose/v3/cmd/goose@latest
```

---

## Технологический стек

| Назначение | Библиотека |
|---|---|
| HTTP-фреймворк | `github.com/gin-gonic/gin` |
| Драйвер Postgres | `github.com/jackc/pgx/v5` (нативный режим + `pgxpool`) |
| Маппинг строк в структуры | `github.com/georgysavva/scany/v2` |
| Миграции | `github.com/pressly/goose/v3` |
| Логирование | `log/slog` (стандартная библиотека) + `github.com/lmittmann/tint` для локального вывода |
| Конфигурация | `github.com/caarlos0/env/v11` + `github.com/joho/godotenv` |
| JWT | `github.com/golang-jwt/jwt/v5` |
| Пароли | `golang.org/x/crypto/bcrypt` |
| UUID | `github.com/google/uuid` |
| Валидация | `github.com/go-playground/validator/v10` (встроен в gin) |
| Тесты | `github.com/stretchr/testify` |
| Документация | `github.com/swaggo/swag` + `gin-swagger` |

### Заметки по безопасности

Что сделано и почему — искать по коду:

- **`jwt.WithValidMethods`** (`service/token.go`) — закрывает атаки `alg=none` и подмену RS256→HS256. Без этой строки библиотека доверяет полю `alg` из заголовка токена, то есть данным атакующего.
- **bcrypt cost=12** (`service/auth_service.go`) — `bcrypt.DefaultCost` равен 10, для нового кода этого уже маловато.
- **Выравнивание времени ответа при входе** — если пользователь не найден, всё равно прогоняется сравнение с заглушечным хешем. Иначе по времени ответа (1 мс против 250 мс) атакующий определяет, кто зарегистрирован в системе.
- **Одинаковая ошибка** на «нет пользователя» и «неверный пароль» — форма входа не должна быть инструментом разведки.
- **404 вместо 403** на чужое предложение — 403 подтвердил бы существование объекта, и перебором id составлялась бы карта чужих данных.
- **Белый список колонок сортировки** — защита от SQL-инъекции там, где плейсхолдеры не работают.
- **Внутренние ошибки не уезжают клиенту** — наружу «internal server error», подробности в лог. Текст ошибки Postgres — прямая подсказка о структуре базы.
- **Роль при регистрации всегда `user`** — никогда не берётся из тела запроса.
- **Валидация `X-Request-ID`** — иначе перевод строки во входящем заголовке позволяет подделывать строки в логах.
- **Запуск контейнера от непривилегированного пользователя**, `SetTrustedProxies(nil)`, Swagger UI выключен в проде.

---

## Что здесь намеренно упрощено

Честный список того, чего в проекте нет, и что появилось бы при росте:

| Чего нет | Что делают в реальности |
|---|---|
| Refresh-токены и отзыв токенов | короткоживущий access + refresh в отдельной таблице, чёрный список `jti` |
| Ограничение частоты запросов | rate limiting на вход (защита от подбора паролей) и на API в целом |
| Метрики и трассировка | Prometheus + OpenTelemetry |
| Интеграционные тесты с базой | `testcontainers-go` — поднимает настоящий Postgres на время тестов |
| Курсорная пагинация | `offset` деградирует на больших смещениях: база всё равно проходит и выбрасывает первые N строк |
| Аудит-лог отдельной таблицей | сейчас аудит только в логах; для юридически значимых действий нужна таблица |
| Кэш | Redis перед горячими выборками |
| CI/CD | сборка, линтер, тесты на каждый push |
| Миграции отдельным шагом деплоя | сейчас применяются при старте приложения |

---

## Где что искать в коде

Если разбираешь проект — вот отправные точки. В каждом файле есть развёрнутый комментарий к пакету.

| Хочу понять | Смотреть |
|---|---|
| **Путь запроса целиком, по шагам** | **[`docs/request-flow.html`](docs/request-flow.html)** — открыть в браузере |
| Как всё собирается вместе | `cmd/api/main.go` |
| Как устроены роуты и порядок middleware | `internal/handler/router.go` |
| Как работает «луковица» построчно | `internal/middleware/logger.go` |
| Как ловится паника | `internal/middleware/recovery.go` |
| Как проверяется JWT и какие бывают атаки | `internal/service/token.go` |
| Три состояния поля (значение/null/отсутствие) | `internal/model/optional.go` |
| Транзакция с блокировками | `internal/service/suggestion_service.go` → `Approve` |
| Как устроен менеджер транзакций | `internal/db/tx.go` |
| Настройки пула соединений | `internal/db/postgres.go` |
| Динамический SQL без инъекций | `internal/repository/client_repo.go` → `List` |
| Как избегается проблема N+1 | `internal/repository/suggestion_repo.go` → `suggestionJoins` |
| Ошибки по слоям и их перевод в HTTP | `internal/apperr/apperr.go` |
| Перевод ошибок валидации в понятный вид | `internal/handler/binding.go` |
| Graceful shutdown | `cmd/api/main.go` → `run()`, блок `select` |
| Почему liveness ≠ readiness | `internal/handler/health.go` |

---

## Лицензия

MIT
