-- Миграция: демонстрационные данные, чтобы проект заработал сразу
-- после `make up` — без ручного создания первого админа.
--
-- ============================================================================
--  ВНИМАНИЕ. ЭТО ДЕМО-ДАННЫЕ С ИЗВЕСТНЫМИ ПАРОЛЯМИ.
--  Перед любым реальным использованием эту миграцию нужно УДАЛИТЬ,
--  а первого администратора заводить отдельной командой или вручную.
--  Пароли ниже опубликованы в README — считайте их скомпрометированными.
-- ============================================================================
--
-- Учётные записи:
--   admin@example.com / admin12345  — роль admin
--   user@example.com  / user12345   — роль user
--
-- Хеши получены bcrypt с cost=10 (значение по умолчанию).

-- +goose Up

INSERT INTO users (id, email, password_hash, role) VALUES
    ('aaaaaaaa-0000-4000-8000-000000000001',
     'admin@example.com',
     '$2a$10$2dRi9YApmrMfbXkvbmqsDu8YEfDVwGE8GwhRVKTTGbg/SkFULIyX2',
     'admin'),
    ('bbbbbbbb-0000-4000-8000-000000000001',
     'user@example.com',
     '$2a$10$hVmAAJP6b92ypVCeuZ2Ay.vM/sCs1Xnkme6r/rlHquDiFcmyq98FC',
     'user');

-- Пара клиентов, чтобы было что смотреть и что предлагать править.
INSERT INTO clients (id, full_name, email, phone, company, notes, status) VALUES
    ('cccccccc-0000-4000-8000-000000000001',
     'Ivan Petrov',
     'ivan.petrov@example.com',
     '+7 900 000-00-01',
     'Acme LLC',
     'Key account, prefers email',
     'active'),
    ('cccccccc-0000-4000-8000-000000000002',
     'Maria Sidorova',
     'maria.sidorova@example.com',
     '+7 900 000-00-02',
     'Globex',
     NULL,
     'active'),
    ('cccccccc-0000-4000-8000-000000000003',
     'Sergey Volkov',
     NULL,
     '+7 900 000-00-03',
     'Initech',
     'Contact details need verification',
     'archived');

-- +goose Down

DELETE FROM clients WHERE id IN (
    'cccccccc-0000-4000-8000-000000000001',
    'cccccccc-0000-4000-8000-000000000002',
    'cccccccc-0000-4000-8000-000000000003'
);

DELETE FROM users WHERE id IN (
    'aaaaaaaa-0000-4000-8000-000000000001',
    'bbbbbbbb-0000-4000-8000-000000000001'
);
