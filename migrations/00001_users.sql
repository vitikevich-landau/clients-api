-- Миграция: таблица пользователей системы.
--
-- Пользователь — это тот, кто ПОЛЬЗУЕТСЯ сервисом (заходит по логину),
-- в отличие от клиента (client) — записи в справочнике, которую ведут.
-- Две разные сущности, не путать.

-- +goose Up

CREATE TABLE users (
    -- UUID генерируется в Go, а не в базе.
    -- Плюс: идентификатор известен ДО вставки, его можно писать в логи
    -- и передавать в другие сервисы, не дожидаясь ответа Postgres.
    id UUID PRIMARY KEY,

    -- Email хранится всегда в нижнем регистре (приводится в Go).
    -- Поэтому обычный UNIQUE достаточен — не нужен индекс по lower(email).
    email TEXT NOT NULL UNIQUE,

    -- Именно ХЕШ пароля (bcrypt), а не пароль.
    -- Сам пароль не хранится нигде и не пишется ни в какие логи.
    password_hash TEXT NOT NULL,

    -- Роль. CHECK на уровне базы — последний рубеж:
    -- даже если баг в коде попробует записать роль "superadmin",
    -- Postgres не даст этого сделать.
    role TEXT NOT NULL CHECK (role IN ('user', 'admin')),

    created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- TIMESTAMPTZ, а не TIMESTAMP: тип с таймзоной хранит момент времени
-- однозначно. TIMESTAMP без зоны — источник вечных багов, когда сервис
-- и база живут в разных часовых поясах.

COMMENT ON TABLE users IS 'System users: regular users and administrators';

-- +goose Down

DROP TABLE users;
