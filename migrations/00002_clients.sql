-- Миграция: справочник клиентов — основная сущность сервиса.

-- +goose Up

-- pg_trgm даёт индексы для поиска по подстроке (ILIKE '%текст%').
-- Без него такой поиск ВСЕГДА приводит к полному перебору таблицы:
-- обычный B-tree индекс работает только для префикса ('текст%').
-- Расширение входит в стандартный образ postgres.
CREATE EXTENSION IF NOT EXISTS pg_trgm;

CREATE TABLE clients (
    id UUID PRIMARY KEY,

    full_name TEXT NOT NULL CHECK (length(trim(full_name)) > 0),

    -- email/phone/company/notes допускают NULL: клиента могли завести,
    -- зная только имя, а остальное дозаполнить позже.
    email   TEXT,
    phone   TEXT,
    company TEXT,
    notes   TEXT,

    status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'archived')),

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),

    -- МЯГКОЕ УДАЛЕНИЕ.
    -- NULL — запись живая, не NULL — момент удаления.
    -- Строка физически остаётся в базе: не рвутся внешние ключи
    -- из истории предложений, и удаление можно откатить.
    -- Все выборки обязаны фильтровать по deleted_at IS NULL.
    deleted_at TIMESTAMPTZ
);

-- Частичный индекс (WHERE deleted_at IS NULL) — индексируем только живые
-- строки. Меньше размер, быстрее обновление, и он точно совпадает
-- с условием наших запросов.
CREATE INDEX idx_clients_alive
    ON clients (created_at DESC)
    WHERE deleted_at IS NULL;

-- Email клиента уникален, но только среди ЖИВЫХ записей:
-- удалили клиента — его адрес снова свободен.
-- lower(email) — чтобы Ivan@x.ru и ivan@x.ru считались одним адресом.
CREATE UNIQUE INDEX ux_clients_email_alive
    ON clients (lower(email))
    WHERE deleted_at IS NULL AND email IS NOT NULL;

-- GIN + trigram: индекс для поиска по подстроке в имени и компании.
CREATE INDEX idx_clients_full_name_trgm
    ON clients USING gin (full_name gin_trgm_ops);

CREATE INDEX idx_clients_company_trgm
    ON clients USING gin (company gin_trgm_ops);

-- Индекс под фильтр по статусу.
CREATE INDEX idx_clients_status
    ON clients (status)
    WHERE deleted_at IS NULL;

COMMENT ON TABLE clients IS 'Client directory — the main business entity';
COMMENT ON COLUMN clients.deleted_at IS 'Soft delete marker: NULL means the row is alive';

-- +goose Down

DROP TABLE clients;
-- Расширение намеренно не удаляем: им могут пользоваться другие таблицы.
