package service_test

// Заглушки (fakes) для тестов сервисного слоя.
//
// # Почему написаны руками, а не сгенерированы mockgen
//
// На таком количестве методов ручные заглушки читаются лучше
// сгенерированного кода: видно ровно то поведение, которое нужно тесту,
// и не надо держать в голове ещё один инструмент.
//
// Генератор (mockgen, mockery) окупается, когда интерфейсов десятки
// или когда нужна проверка точного порядка вызовов.
//
// # Ради чего всё это
//
// Именно здесь окупаются интерфейсы, объявленные в service/interfaces.go.
// Сервис не знает, что за ними Postgres, — поэтому тесты бизнес-логики
// работают без базы, без Docker и за миллисекунды.

import (
	"context"

	"github.com/google/uuid"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
	"github.com/vitikevich-landau/clients-api/internal/db"
	"github.com/vitikevich-landau/clients-api/internal/model"
)

// ---------------------------------------------------------------------------
// Менеджер транзакций
// ---------------------------------------------------------------------------

// fakeTxRunner изображает транзакцию, просто вызывая переданную функцию.
//
// Настоящих BEGIN/COMMIT здесь нет и быть не может — базы нет вовсе.
// Тест проверяет ЛОГИКУ внутри транзакции, а не саму транзакционность:
// атомарность обеспечивает Postgres, и проверять её надо интеграционным
// тестом с реальной базой, а не заглушкой.
type fakeTxRunner struct {
	// WithTxCalls считает вызовы — позволяет проверить,
	// что операция вообще была обёрнута в транзакцию.
	WithTxCalls int
}

func (f *fakeTxRunner) DB() db.Querier { return nil }

func (f *fakeTxRunner) WithTx(ctx context.Context, fn func(context.Context, db.Querier) error) error {
	f.WithTxCalls++
	// nil в качестве Querier безопасен: заглушки репозиториев
	// этот параметр игнорируют.
	return fn(ctx, nil)
}

// ---------------------------------------------------------------------------
// Репозиторий клиентов
// ---------------------------------------------------------------------------

type fakeClientRepo struct {
	// Clients — «база» в памяти.
	Clients map[uuid.UUID]*model.Client

	// UpdateErr позволяет тесту смоделировать ошибку сохранения —
	// например, конфликт уникальности email.
	UpdateErr error

	// Updated хранит последнюю сохранённую карточку,
	// чтобы тест мог проверить, ЧТО именно записали.
	Updated *model.Client
}

func newFakeClientRepo(clients ...*model.Client) *fakeClientRepo {
	repo := &fakeClientRepo{Clients: make(map[uuid.UUID]*model.Client, len(clients))}
	for _, c := range clients {
		repo.Clients[c.ID] = c
	}
	return repo
}

func (f *fakeClientRepo) Create(_ context.Context, _ db.Querier, c *model.Client) error {
	f.Clients[c.ID] = c
	return nil
}

func (f *fakeClientRepo) GetByID(_ context.Context, _ db.Querier, id uuid.UUID) (*model.Client, error) {
	c, ok := f.Clients[id]
	if !ok {
		return nil, apperr.NotFound("client")
	}
	// Возвращаем КОПИЮ — как это делает настоящий репозиторий,
	// собирая структуру из строки результата. Без копии тест мог бы
	// случайно править «содержимое базы» напрямую и не заметить бага.
	clone := *c
	return &clone, nil
}

func (f *fakeClientRepo) GetByIDForUpdate(ctx context.Context, q db.Querier, id uuid.UUID) (*model.Client, error) {
	// Блокировку строки в памяти не изобразить — поведение то же, что у GetByID.
	return f.GetByID(ctx, q, id)
}

func (f *fakeClientRepo) Update(_ context.Context, _ db.Querier, c *model.Client) error {
	if f.UpdateErr != nil {
		return f.UpdateErr
	}
	if _, ok := f.Clients[c.ID]; !ok {
		return apperr.NotFound("client")
	}

	clone := *c
	f.Clients[c.ID] = &clone
	f.Updated = &clone
	return nil
}

func (f *fakeClientRepo) SoftDelete(_ context.Context, _ db.Querier, id uuid.UUID) error {
	if _, ok := f.Clients[id]; !ok {
		return apperr.NotFound("client")
	}
	delete(f.Clients, id)
	return nil
}

func (f *fakeClientRepo) List(_ context.Context, _ db.Querier, _ model.ListClientsQuery) ([]model.Client, int, error) {
	out := make([]model.Client, 0, len(f.Clients))
	for _, c := range f.Clients {
		out = append(out, *c)
	}
	return out, len(out), nil
}

func (f *fakeClientRepo) Exists(_ context.Context, _ db.Querier, id uuid.UUID) (bool, error) {
	_, ok := f.Clients[id]
	return ok, nil
}

// ---------------------------------------------------------------------------
// Репозиторий предложений
// ---------------------------------------------------------------------------

type fakeSuggestionRepo struct {
	Suggestions map[uuid.UUID]*model.Suggestion

	// Данные последнего вызова Review — для проверки, что предложение
	// перевели в нужный статус нужным рецензентом.
	ReviewedID      uuid.UUID
	ReviewedStatus  model.SuggestionStatus
	ReviewedBy      uuid.UUID
	ReviewedComment *string
	ReviewCalls     int

	CreateErr error
}

func newFakeSuggestionRepo(items ...*model.Suggestion) *fakeSuggestionRepo {
	repo := &fakeSuggestionRepo{Suggestions: make(map[uuid.UUID]*model.Suggestion, len(items))}
	for _, s := range items {
		repo.Suggestions[s.ID] = s
	}
	return repo
}

func (f *fakeSuggestionRepo) Create(_ context.Context, _ db.Querier, s *model.Suggestion) error {
	if f.CreateErr != nil {
		return f.CreateErr
	}
	f.Suggestions[s.ID] = s
	return nil
}

func (f *fakeSuggestionRepo) GetByID(_ context.Context, _ db.Querier, id uuid.UUID) (*model.SuggestionDetailed, error) {
	s, ok := f.Suggestions[id]
	if !ok {
		return nil, apperr.NotFound("suggestion")
	}
	return &model.SuggestionDetailed{
		Suggestion:  *s,
		AuthorEmail: "author@example.com",
		ClientName:  "Test Client",
	}, nil
}

func (f *fakeSuggestionRepo) GetByIDForUpdate(_ context.Context, _ db.Querier, id uuid.UUID) (*model.Suggestion, error) {
	s, ok := f.Suggestions[id]
	if !ok {
		return nil, apperr.NotFound("suggestion")
	}
	clone := *s
	return &clone, nil
}

func (f *fakeSuggestionRepo) Review(
	_ context.Context, _ db.Querier, id uuid.UUID,
	status model.SuggestionStatus, reviewerID uuid.UUID, comment *string,
) error {
	s, ok := f.Suggestions[id]
	if !ok {
		return apperr.NotFound("suggestion")
	}

	// Повторяем поведение настоящего репозитория: условие
	// `AND status = 'pending'` в UPDATE не найдёт строку,
	// если предложение уже рассмотрено.
	if s.Status != model.SuggestionStatusPending {
		return apperr.Conflict("suggestion is not pending anymore")
	}

	s.Status = status
	s.ReviewerID = &reviewerID
	s.ReviewComment = comment

	f.ReviewCalls++
	f.ReviewedID = id
	f.ReviewedStatus = status
	f.ReviewedBy = reviewerID
	f.ReviewedComment = comment

	return nil
}

func (f *fakeSuggestionRepo) List(
	_ context.Context, _ db.Querier, _ model.ListSuggestionsQuery, authorID *uuid.UUID,
) ([]model.SuggestionDetailed, int, error) {
	out := make([]model.SuggestionDetailed, 0, len(f.Suggestions))
	for _, s := range f.Suggestions {
		if authorID != nil && s.AuthorID != *authorID {
			continue
		}
		out = append(out, model.SuggestionDetailed{Suggestion: *s})
	}
	return out, len(out), nil
}
