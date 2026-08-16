// Package service — слой бизнес-логики.
//
// # Что здесь есть и чего здесь нет
//
// ЕСТЬ: правила предметной области. «Нельзя рассмотреть уже рассмотренное
// предложение», «одобрение обязано быть атомарным», «регистрация никогда
// не выдаёт роль администратора».
//
// НЕТ: ничего про HTTP. В этом пакете не встретится ни gin.Context,
// ни http.StatusNotFound, ни разбора JSON. Благодаря этому ту же логику
// можно вызвать из CLI, из gRPC или из фоновой задачи, ничего не переписывая.
package service

import (
	"context"

	"github.com/google/uuid"

	"github.com/vitikevich-landau/clients-api/internal/db"
	"github.com/vitikevich-landau/clients-api/internal/model"
)

// ---------------------------------------------------------------------------
// Интерфейсы репозиториев
// ---------------------------------------------------------------------------
//
// # Почему интерфейсы объявлены ЗДЕСЬ, а не в пакете repository
//
// Идиома Go: интерфейс принадлежит тому, кто его ПОТРЕБЛЯЕТ,
// а не тому, кто реализует. Причины:
//
//  1. Пакет service объявляет ровно то, что ему нужно — ни методом больше.
//     Если бы интерфейс жил в repository, туда бы стащили все методы «на всякий
//     случай», и каждый мок в тестах пришлось бы реализовывать целиком.
//
//  2. service ничего не импортирует из repository — зависимость направлена
//     в одну сторону. Postgres можно заменить на что угодно, не трогая логику.
//
//  3. Моки для тестов пишутся против маленького интерфейса и не ломаются
//     при добавлении методов в реальный репозиторий.
//
// Реализуют эти интерфейсы структуры из пакета repository — автоматически,
// без слова implements: в Go соответствие интерфейсу структурное.

// UserRepository — то, что нужно сервисам от таблицы users.
type UserRepository interface {
	Create(ctx context.Context, q db.Querier, u *model.User) error
	GetByEmail(ctx context.Context, q db.Querier, email string) (*model.User, error)
	GetByID(ctx context.Context, q db.Querier, id uuid.UUID) (*model.User, error)
	ExistsByEmail(ctx context.Context, q db.Querier, email string) (bool, error)
}

// ClientRepository — то, что нужно сервисам от таблицы clients.
type ClientRepository interface {
	Create(ctx context.Context, q db.Querier, c *model.Client) error
	GetByID(ctx context.Context, q db.Querier, id uuid.UUID) (*model.Client, error)
	GetByIDForUpdate(ctx context.Context, q db.Querier, id uuid.UUID) (*model.Client, error)
	Update(ctx context.Context, q db.Querier, c *model.Client) error
	SoftDelete(ctx context.Context, q db.Querier, id uuid.UUID) error
	List(ctx context.Context, q db.Querier, params model.ListClientsQuery) ([]model.Client, int, error)
	Exists(ctx context.Context, q db.Querier, id uuid.UUID) (bool, error)
}

// SuggestionRepository — то, что нужно сервисам от таблицы client_suggestions.
type SuggestionRepository interface {
	Create(ctx context.Context, q db.Querier, s *model.Suggestion) error
	GetByID(ctx context.Context, q db.Querier, id uuid.UUID) (*model.SuggestionDetailed, error)
	GetByIDForUpdate(ctx context.Context, q db.Querier, id uuid.UUID) (*model.Suggestion, error)
	Review(ctx context.Context, q db.Querier, id uuid.UUID,
		status model.SuggestionStatus, reviewerID uuid.UUID, comment *string) error
	List(ctx context.Context, q db.Querier, params model.ListSuggestionsQuery,
		authorID *uuid.UUID) ([]model.SuggestionDetailed, int, error)
}

// TxRunner — то, что нужно сервисам от менеджера транзакций.
//
// Реализуется структурой db.TxManager. Отдельный интерфейс нужен затем же,
// зачем и остальные: чтобы в тестах подменить транзакцию заглушкой,
// которая просто вызывает переданную функцию.
type TxRunner interface {
	// DB отдаёт исполнителя запросов вне транзакции.
	DB() db.Querier

	// WithTx выполняет fn в транзакции: nil → COMMIT, ошибка → ROLLBACK.
	WithTx(ctx context.Context, fn func(ctx context.Context, tx db.Querier) error) error
}
