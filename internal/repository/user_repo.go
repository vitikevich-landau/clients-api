// Package repository — слой доступа к данным.
//
// # Единственная обязанность
//
// Выполнять SQL и раскладывать результат по структурам. Здесь НЕТ
// бизнес-правил, НЕТ проверок прав, НЕТ ничего про HTTP.
// Репозиторий не знает, зачем его позвали.
//
// # Почему методы принимают db.Querier
//
// Первым делом после ctx идёт параметр q db.Querier — «чем выполнять запрос».
// Его реализуют и пул, и транзакция, поэтому один и тот же метод работает
// в обоих режимах. Решение «нужна ли здесь транзакция» принимает service,
// и это правильно: только он знает, что операция составная.
//
// # Где определены интерфейсы
//
// Интерфейсов репозиториев в этом пакете нет — они объявлены в пакете
// service, который их ПОТРЕБЛЯЕТ. Это идиома Go: интерфейс принадлежит
// вызывающей стороне, а не реализующей. Так пакет service можно тестировать
// с моками, ничего не зная про Postgres.
package repository

import (
	"context"
	"fmt"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/google/uuid"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
	"github.com/vitikevich-landau/clients-api/internal/db"
	"github.com/vitikevich-landau/clients-api/internal/model"
)

// UserRepository — доступ к таблице users.
//
// Структура пустая: соединение с базой передаётся в каждый метод
// параметром q. Хранить пул внутри репозитория тоже можно, но тогда
// пришлось бы дублировать каждый метод в «транзакционном» варианте.
type UserRepository struct{}

// NewUserRepository создаёт репозиторий пользователей.
func NewUserRepository() *UserRepository {
	return &UserRepository{}
}

// Имя ограничения уникальности из миграции 00001.
// Нужно, чтобы отличить нарушение UNIQUE(email) от других конфликтов.
const constraintUsersEmailKey = "users_email_key"

// Create вставляет нового пользователя.
//
// Обрати внимание: created_at/updated_at не перечислены — за них
// отвечает DEFAULT now() в схеме. Единый источник истины для времени —
// часы сервера базы, а не часы каждого инстанса приложения.
// Разъехавшиеся на секунды часы приложений — обычное дело.
func (r *UserRepository) Create(ctx context.Context, q db.Querier, u *model.User) error {
	const query = `
		INSERT INTO users (id, email, password_hash, role)
		VALUES ($1, $2, $3, $4)
		RETURNING created_at, updated_at`

	// RETURNING отдаёт проставленные базой значения обратно —
	// без второго запроса SELECT. Приятная особенность Postgres.
	err := q.QueryRow(ctx, query, u.ID, u.Email, u.PasswordHash, u.Role).
		Scan(&u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		// ПОЧЕМУ РАЗБИРАЕМ ОШИБКУ БАЗЫ, А НЕ ПРОВЕРЯЕМ ЗАРАНЕЕ.
		//
		// Схема «SELECT ... есть такой email? ... нет ... INSERT» — это гонка.
		// Между проверкой и вставкой другой запрос успеет вставить того же
		// пользователя, и мы получим ошибку там, где «проверили».
		// Единственный надёжный способ — попытаться вставить
		// и разобрать отказ базы.
		if db.IsUniqueViolation(err, constraintUsersEmailKey) {
			return apperr.Conflict("user with this email already exists").Wrap(err)
		}
		return fmt.Errorf("insert user: %w", err)
	}

	return nil
}

// GetByEmail находит пользователя по адресу почты.
//
// Email всегда хранится в нижнем регистре (приводится в service-слое),
// поэтому здесь достаточно обычного сравнения — и работает индекс UNIQUE.
// Если бы сравнивали через lower(email) = lower($1), индекс бы не применился
// и каждый вход в систему сканировал бы всю таблицу.
func (r *UserRepository) GetByEmail(ctx context.Context, q db.Querier, email string) (*model.User, error) {
	const query = `
		SELECT id, email, password_hash, role, created_at, updated_at
		FROM users
		WHERE email = $1`

	var u model.User
	// pgxscan.Get требует ровно одну строку и сам раскладывает её
	// по полям структуры, сверяясь с тегами db:"...".
	if err := pgxscan.Get(ctx, q, &u, query, email); err != nil {
		if pgxscan.NotFound(err) {
			return nil, apperr.NotFound("user")
		}
		return nil, fmt.Errorf("select user by email: %w", err)
	}

	return &u, nil
}

// GetByID находит пользователя по идентификатору.
func (r *UserRepository) GetByID(ctx context.Context, q db.Querier, id uuid.UUID) (*model.User, error) {
	const query = `
		SELECT id, email, password_hash, role, created_at, updated_at
		FROM users
		WHERE id = $1`

	var u model.User
	if err := pgxscan.Get(ctx, q, &u, query, id); err != nil {
		if pgxscan.NotFound(err) {
			return nil, apperr.NotFound("user")
		}
		return nil, fmt.Errorf("select user by id: %w", err)
	}

	return &u, nil
}

// ExistsByEmail — быстрая проверка занятости адреса.
//
// Нужна не для защиты от дублей (её обеспечивает UNIQUE в базе),
// а чтобы отдать понятную ошибку валидации ДО того, как мы потратим
// ~60–100 мс процессорного времени на вычисление bcrypt-хеша.
func (r *UserRepository) ExistsByEmail(ctx context.Context, q db.Querier, email string) (bool, error) {
	// EXISTS дешевле, чем count(*): база останавливается на первой
	// найденной строке, а не считает все.
	const query = `SELECT EXISTS(SELECT 1 FROM users WHERE email = $1)`

	var exists bool
	if err := q.QueryRow(ctx, query, email).Scan(&exists); err != nil {
		return false, fmt.Errorf("check user existence by email: %w", err)
	}

	return exists, nil
}
