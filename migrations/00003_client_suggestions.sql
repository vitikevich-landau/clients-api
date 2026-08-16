-- Миграция: предложения правок — сердце механики модерации.
--
-- Обычный пользователь не меняет карточку клиента напрямую.
-- Он создаёт ПРЕДЛОЖЕНИЕ, которое висит в очереди, пока админ
-- не одобрит или не отклонит его.

-- +goose Up

CREATE TABLE client_suggestions (
    id UUID PRIMARY KEY,

    -- Какого клиента предлагают поправить.
    -- ON DELETE CASCADE: если клиента удалят физически (не мягко,
    -- а совсем — например, по требованию об удалении персональных данных),
    -- предложения по нему уедут следом. Висячих ссылок не остаётся.
    client_id UUID NOT NULL REFERENCES clients (id) ON DELETE CASCADE,

    -- Кто предложил.
    author_id UUID NOT NULL REFERENCES users (id) ON DELETE CASCADE,

    -- ПРЕДЛАГАЕМЫЕ ИЗМЕНЕНИЯ в формате JSONB.
    --
    -- Почему JSONB, а не по колонке на каждое поле клиента:
    -- пользователь правит один телефон — в payload летит только телефон.
    -- С колонками пришлось бы завести new_full_name, new_email, new_phone...
    -- и на каждое новое поле клиента добавлять колонку сюда тоже.
    --
    -- Важное различие: отсутствие ключа в payload = "поле не трогаем",
    -- а ключ со значением null = "очистить поле". Это разные операции,
    -- и JSONB позволяет их различать, в отличие от NULL-колонок.
    --
    -- JSONB (а не JSON): хранится в разобранном бинарном виде,
    -- быстрее читается и поддерживает индексы.
    payload JSONB NOT NULL,

    status TEXT NOT NULL DEFAULT 'pending'
        CHECK (status IN ('pending', 'approved', 'rejected')),

    -- Кто рассмотрел. NULL, пока предложение в очереди.
    -- ON DELETE SET NULL: удалили админа — история решения остаётся,
    -- просто без указания конкретного человека.
    reviewer_id UUID REFERENCES users (id) ON DELETE SET NULL,

    -- Комментарий модератора (обязателен при отклонении — проверяется в коде).
    review_comment TEXT,

    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    reviewed_at TIMESTAMPTZ,

    -- Целостность на уровне базы: рассмотренное предложение обязано
    -- иметь отметку времени рассмотрения, а ожидающее — не иметь.
    -- Такие инварианты лучше держать в базе: код можно обойти
    -- (миграцией, ручным UPDATE), базу — нет.
    CONSTRAINT chk_reviewed_consistency CHECK (
        (status = 'pending'  AND reviewed_at IS NULL)
        OR
        (status <> 'pending' AND reviewed_at IS NOT NULL)
    )
);

-- Главный запрос админки: "покажи очередь на модерацию, свежие сверху".
-- Составной индекс (status, created_at DESC) закрывает и фильтр, и сортировку.
CREATE INDEX idx_suggestions_status_created
    ON client_suggestions (status, created_at DESC);

-- Запрос пользователя: "покажи мои предложения".
CREATE INDEX idx_suggestions_author_created
    ON client_suggestions (author_id, created_at DESC);

-- Запрос карточки: "все предложения по этому клиенту".
CREATE INDEX idx_suggestions_client
    ON client_suggestions (client_id);

-- Один пользователь не может держать два НЕРАССМОТРЕННЫХ предложения
-- по одному клиенту — иначе очередь засоряется дублями.
-- Частичный уникальный индекс: ограничение действует только для pending,
-- а рассмотренных предложений может быть сколько угодно.
CREATE UNIQUE INDEX ux_suggestions_one_pending_per_author_client
    ON client_suggestions (client_id, author_id)
    WHERE status = 'pending';

COMMENT ON TABLE client_suggestions IS 'Moderated change requests for client records';
COMMENT ON COLUMN client_suggestions.payload IS 'Partial client update: only the fields being changed';

-- +goose Down

DROP TABLE client_suggestions;
