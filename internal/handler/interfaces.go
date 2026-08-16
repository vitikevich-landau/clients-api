package handler

import (
	"context"

	"github.com/google/uuid"

	"github.com/vitikevich-landau/clients-api/internal/model"
)

// Интерфейсы сервисов, объявленные у потребителя — так же, как интерфейсы
// репозиториев объявлены в пакете service.
//
// Практическая польза: тесты хендлеров подставляют сюда заглушки и проверяют
// РОВНО транспортный слой — коды ответов, разбор параметров, формат JSON —
// не поднимая ни базу, ни бизнес-логику. Ошибка в SQL не ломает тесты
// хендлеров, и наоборот. Каждый тест падает по своей причине.

// AuthService — что хендлерам нужно от аутентификации.
type AuthService interface {
	Register(ctx context.Context, req model.RegisterRequest) (*model.AuthResponse, error)
	Login(ctx context.Context, req model.LoginRequest) (*model.AuthResponse, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.User, error)
}

// ClientService — что хендлерам нужно от справочника клиентов.
type ClientService interface {
	List(ctx context.Context, params model.ListClientsQuery) ([]model.Client, int, error)
	GetByID(ctx context.Context, id uuid.UUID) (*model.Client, error)
	Create(ctx context.Context, req model.CreateClientRequest, actor model.AuthUser) (*model.Client, error)
	Update(ctx context.Context, id uuid.UUID, req model.UpdateClientRequest, actor model.AuthUser) (*model.Client, error)
	Delete(ctx context.Context, id uuid.UUID, actor model.AuthUser) error
}

// SuggestionService — что хендлерам нужно от механики модерации.
type SuggestionService interface {
	Create(ctx context.Context, clientID uuid.UUID, author model.AuthUser,
		payload model.SuggestionPayload) (*model.SuggestionDetailed, error)
	List(ctx context.Context, params model.ListSuggestionsQuery,
		authorID *uuid.UUID) ([]model.SuggestionDetailed, int, error)
	GetByID(ctx context.Context, id uuid.UUID, actor model.AuthUser) (*model.SuggestionDetailed, error)
	Approve(ctx context.Context, id uuid.UUID, reviewer model.AuthUser,
		comment *string) (*model.SuggestionDetailed, error)
	Reject(ctx context.Context, id uuid.UUID, reviewer model.AuthUser,
		comment string) (*model.SuggestionDetailed, error)
}
