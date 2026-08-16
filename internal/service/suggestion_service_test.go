package service_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
	"github.com/vitikevich-landau/clients-api/internal/model"
	"github.com/vitikevich-landau/clients-api/internal/service"
)

// ptr — указатель на значение (в тестах постоянно нужен *string).
func ptr[T any](v T) *T { return &v }

// mustPayload разбирает JSON в payload или валит тест.
//
// Собирать Optional вручную можно, но через JSON нагляднее: в тесте
// видно ровно тот вход, который придёт от настоящего клиента.
func mustPayload(t *testing.T, raw string) model.SuggestionPayload {
	t.Helper() // при падении покажет строку ВЫЗОВА, а не строку внутри хелпера

	var p model.SuggestionPayload
	require.NoError(t, json.Unmarshal([]byte(raw), &p))
	return p
}

// requireAppErrCode проверяет, что ошибка — наша доменная и с нужным кодом.
func requireAppErrCode(t *testing.T, err error, want apperr.Code) {
	t.Helper()

	require.Error(t, err)

	var appErr *apperr.Error
	require.True(t, errors.As(err, &appErr),
		"ожидалась ошибка типа *apperr.Error, получено: %T (%v)", err, err)
	assert.Equal(t, want, appErr.Code, "неверный код ошибки: %s", appErr.Message)
}

// testFixture — общая обвязка для тестов.
type testFixture struct {
	service     *service.SuggestionService
	clients     *fakeClientRepo
	suggestions *fakeSuggestionRepo
	tx          *fakeTxRunner

	client   *model.Client
	author   model.AuthUser
	reviewer model.AuthUser
}

// newFixture готовит сервис с заглушками и одним клиентом в «базе».
func newFixture(t *testing.T, suggestions ...*model.Suggestion) *testFixture {
	t.Helper()

	client := &model.Client{
		ID:       uuid.New(),
		FullName: "Ivan Petrov",
		Email:    ptr("ivan@example.com"),
		Phone:    ptr("+7 900 000-00-01"),
		Company:  ptr("Acme LLC"),
		Status:   model.ClientStatusActive,
	}

	clientRepo := newFakeClientRepo(client)
	suggestionRepo := newFakeSuggestionRepo(suggestions...)
	tx := &fakeTxRunner{}

	return &testFixture{
		service:     service.NewSuggestionService(suggestionRepo, clientRepo, tx),
		clients:     clientRepo,
		suggestions: suggestionRepo,
		tx:          tx,
		client:      client,
		author:      model.AuthUser{ID: uuid.New(), Email: "user@example.com", Role: model.RoleUser},
		reviewer:    model.AuthUser{ID: uuid.New(), Email: "admin@example.com", Role: model.RoleAdmin},
	}
}

// newPendingSuggestion собирает нерассмотренное предложение.
func newPendingSuggestion(clientID, authorID uuid.UUID, payload model.SuggestionPayload) *model.Suggestion {
	return &model.Suggestion{
		ID:       uuid.New(),
		ClientID: clientID,
		AuthorID: authorID,
		Payload:  payload,
		Status:   model.SuggestionStatusPending,
	}
}

// ---------------------------------------------------------------------------
// Create
// ---------------------------------------------------------------------------

func TestSuggestionService_Create(t *testing.T) {
	ctx := context.Background()

	t.Run("создаёт предложение и НЕ трогает карточку клиента", func(t *testing.T) {
		f := newFixture(t)
		originalPhone := *f.client.Phone

		result, err := f.service.Create(ctx, f.client.ID, f.author,
			mustPayload(t, `{"phone":"+7 999 111-22-33"}`))

		require.NoError(t, err)
		assert.Equal(t, model.SuggestionStatusPending, result.Status)
		assert.Equal(t, f.author.ID, result.AuthorID)

		// ГЛАВНАЯ ПРОВЕРКА ВСЕЙ МЕХАНИКИ:
		// предложение создано, но данные клиента остались прежними.
		assert.Equal(t, originalPhone, *f.clients.Clients[f.client.ID].Phone,
			"карточка клиента не должна меняться до одобрения")
	})

	t.Run("пустой payload отвергается", func(t *testing.T) {
		f := newFixture(t)

		_, err := f.service.Create(ctx, f.client.ID, f.author, mustPayload(t, `{}`))

		requireAppErrCode(t, err, apperr.CodeValidation)
	})

	t.Run("невалидный payload отвергается с разбором по полям", func(t *testing.T) {
		f := newFixture(t)

		_, err := f.service.Create(ctx, f.client.ID, f.author,
			mustPayload(t, `{"email":"не-адрес"}`))

		requireAppErrCode(t, err, apperr.CodeValidation)

		var appErr *apperr.Error
		require.True(t, errors.As(err, &appErr))
		assert.Contains(t, appErr.Details, "email",
			"клиенту должно быть понятно, какое поле не понравилось")
	})

	t.Run("несуществующий клиент даёт 404", func(t *testing.T) {
		f := newFixture(t)

		_, err := f.service.Create(ctx, uuid.New(), f.author,
			mustPayload(t, `{"phone":"+7 999 111-22-33"}`))

		requireAppErrCode(t, err, apperr.CodeNotFound)
	})
}

// ---------------------------------------------------------------------------
// Approve — самый важный сценарий
// ---------------------------------------------------------------------------

func TestSuggestionService_Approve(t *testing.T) {
	ctx := context.Background()

	t.Run("применяет изменения к клиенту и помечает предложение одобренным", func(t *testing.T) {
		payload := mustPayload(t, `{"phone":"+7 999 111-22-33","company":"Globex"}`)
		suggestion := newPendingSuggestion(uuid.Nil, uuid.Nil, payload)

		f := newFixture(t)
		// Привязываем предложение к реальным идентификаторам из фикстуры.
		suggestion.ClientID = f.client.ID
		suggestion.AuthorID = f.author.ID
		f.suggestions.Suggestions[suggestion.ID] = suggestion

		result, err := f.service.Approve(ctx, suggestion.ID, f.reviewer, ptr("looks good"))
		require.NoError(t, err)

		// 1. Предложение переведено в approved.
		assert.Equal(t, model.SuggestionStatusApproved, result.Status)
		assert.Equal(t, f.reviewer.ID, f.suggestions.ReviewedBy)

		// 2. Изменения применены к карточке клиента.
		updated := f.clients.Clients[f.client.ID]
		assert.Equal(t, "+7 999 111-22-33", *updated.Phone)
		assert.Equal(t, "Globex", *updated.Company)

		// 3. Поля, которых не было в payload, не пострадали.
		assert.Equal(t, "ivan@example.com", *updated.Email)
		assert.Equal(t, "Ivan Petrov", updated.FullName)

		// 4. Всё произошло внутри транзакции.
		assert.Equal(t, 1, f.tx.WithTxCalls,
			"одобрение обязано выполняться в транзакции")
	})

	t.Run("явный null очищает поле клиента", func(t *testing.T) {
		f := newFixture(t)
		suggestion := newPendingSuggestion(f.client.ID, f.author.ID,
			mustPayload(t, `{"company":null}`))
		f.suggestions.Suggestions[suggestion.ID] = suggestion

		_, err := f.service.Approve(ctx, suggestion.ID, f.reviewer, nil)
		require.NoError(t, err)

		assert.Nil(t, f.clients.Clients[f.client.ID].Company,
			"null в payload должен очищать поле")
	})

	t.Run("повторное одобрение даёт конфликт", func(t *testing.T) {
		f := newFixture(t)
		suggestion := newPendingSuggestion(f.client.ID, f.author.ID,
			mustPayload(t, `{"phone":"+7 999 111-22-33"}`))
		// Предложение уже рассмотрено.
		suggestion.Status = model.SuggestionStatusApproved
		f.suggestions.Suggestions[suggestion.ID] = suggestion

		_, err := f.service.Approve(ctx, suggestion.ID, f.reviewer, nil)

		requireAppErrCode(t, err, apperr.CodeConflict)
	})

	t.Run("несуществующее предложение даёт 404", func(t *testing.T) {
		f := newFixture(t)

		_, err := f.service.Approve(ctx, uuid.New(), f.reviewer, nil)

		requireAppErrCode(t, err, apperr.CodeNotFound)
	})

	t.Run("удалённый клиент даёт конфликт, а не 500", func(t *testing.T) {
		f := newFixture(t)
		suggestion := newPendingSuggestion(f.client.ID, f.author.ID,
			mustPayload(t, `{"phone":"+7 999 111-22-33"}`))
		f.suggestions.Suggestions[suggestion.ID] = suggestion

		// Клиента удалили, пока предложение висело в очереди.
		delete(f.clients.Clients, f.client.ID)

		_, err := f.service.Approve(ctx, suggestion.ID, f.reviewer, nil)

		requireAppErrCode(t, err, apperr.CodeConflict)
	})

	t.Run("ошибка сохранения клиента откатывает всю операцию", func(t *testing.T) {
		f := newFixture(t)
		suggestion := newPendingSuggestion(f.client.ID, f.author.ID,
			mustPayload(t, `{"email":"taken@example.com"}`))
		f.suggestions.Suggestions[suggestion.ID] = suggestion

		// Такой email уже занят другим клиентом — база вернёт конфликт.
		f.clients.UpdateErr = apperr.Conflict("client with this email already exists")

		_, err := f.service.Approve(ctx, suggestion.ID, f.reviewer, nil)

		requireAppErrCode(t, err, apperr.CodeConflict)

		// Предложение НЕ должно было смениться на approved:
		// в настоящей базе транзакция откатилась бы целиком.
		assert.Equal(t, model.SuggestionStatusPending,
			f.suggestions.Suggestions[suggestion.ID].Status)
		assert.Zero(t, f.suggestions.ReviewCalls,
			"до записи статуса дело дойти не должно")
	})
}

// ---------------------------------------------------------------------------
// Reject
// ---------------------------------------------------------------------------

func TestSuggestionService_Reject(t *testing.T) {
	ctx := context.Background()

	t.Run("отклоняет и не трогает карточку клиента", func(t *testing.T) {
		f := newFixture(t)
		originalPhone := *f.client.Phone

		suggestion := newPendingSuggestion(f.client.ID, f.author.ID,
			mustPayload(t, `{"phone":"+7 999 111-22-33"}`))
		f.suggestions.Suggestions[suggestion.ID] = suggestion

		result, err := f.service.Reject(ctx, suggestion.ID, f.reviewer, "wrong phone format")
		require.NoError(t, err)

		assert.Equal(t, model.SuggestionStatusRejected, result.Status)
		assert.Equal(t, originalPhone, *f.clients.Clients[f.client.ID].Phone,
			"отклонённая правка не должна применяться")
	})

	t.Run("пустой комментарий отвергается", func(t *testing.T) {
		f := newFixture(t)
		suggestion := newPendingSuggestion(f.client.ID, f.author.ID,
			mustPayload(t, `{"phone":"+7 999 111-22-33"}`))
		f.suggestions.Suggestions[suggestion.ID] = suggestion

		// Одни пробелы — тоже пустой комментарий.
		_, err := f.service.Reject(ctx, suggestion.ID, f.reviewer, "   ")

		requireAppErrCode(t, err, apperr.CodeValidation)
	})

	t.Run("повторное отклонение даёт конфликт", func(t *testing.T) {
		f := newFixture(t)
		suggestion := newPendingSuggestion(f.client.ID, f.author.ID,
			mustPayload(t, `{"phone":"+7 999 111-22-33"}`))
		suggestion.Status = model.SuggestionStatusRejected
		f.suggestions.Suggestions[suggestion.ID] = suggestion

		_, err := f.service.Reject(ctx, suggestion.ID, f.reviewer, "again")

		requireAppErrCode(t, err, apperr.CodeConflict)
	})
}

// ---------------------------------------------------------------------------
// Права доступа
// ---------------------------------------------------------------------------

func TestSuggestionService_GetByID_Permissions(t *testing.T) {
	ctx := context.Background()

	t.Run("автор видит своё предложение", func(t *testing.T) {
		f := newFixture(t)
		suggestion := newPendingSuggestion(f.client.ID, f.author.ID,
			mustPayload(t, `{"phone":"+7 999 111-22-33"}`))
		f.suggestions.Suggestions[suggestion.ID] = suggestion

		result, err := f.service.GetByID(ctx, suggestion.ID, f.author)

		require.NoError(t, err)
		assert.Equal(t, suggestion.ID, result.ID)
	})

	t.Run("админ видит чужое предложение", func(t *testing.T) {
		f := newFixture(t)
		suggestion := newPendingSuggestion(f.client.ID, f.author.ID,
			mustPayload(t, `{"phone":"+7 999 111-22-33"}`))
		f.suggestions.Suggestions[suggestion.ID] = suggestion

		result, err := f.service.GetByID(ctx, suggestion.ID, f.reviewer)

		require.NoError(t, err)
		assert.Equal(t, suggestion.ID, result.ID)
	})

	t.Run("чужое предложение маскируется под 404, а не 403", func(t *testing.T) {
		f := newFixture(t)
		suggestion := newPendingSuggestion(f.client.ID, f.author.ID,
			mustPayload(t, `{"phone":"+7 999 111-22-33"}`))
		f.suggestions.Suggestions[suggestion.ID] = suggestion

		stranger := model.AuthUser{ID: uuid.New(), Role: model.RoleUser}

		_, err := f.service.GetByID(ctx, suggestion.ID, stranger)

		// Именно 404, а не 403: код 403 подтвердил бы СУЩЕСТВОВАНИЕ
		// объекта, и перебором идентификаторов можно было бы составить
		// карту чужих данных, не имея к ним доступа.
		requireAppErrCode(t, err, apperr.CodeNotFound)
	})
}
