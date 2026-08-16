package service

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
	"github.com/vitikevich-landau/clients-api/internal/db"
	"github.com/vitikevich-landau/clients-api/internal/logger"
	"github.com/vitikevich-landau/clients-api/internal/model"
)

// SuggestionService — механика модерации правок.
//
// Здесь живёт главное бизнес-правило сервиса: обычный пользователь
// не меняет карточку клиента, он ПРЕДЛАГАЕТ изменение, а применяется оно
// только после одобрения администратором.
type SuggestionService struct {
	suggestions SuggestionRepository
	clients     ClientRepository
	tx          TxRunner
}

// NewSuggestionService собирает сервис предложений.
func NewSuggestionService(
	suggestions SuggestionRepository,
	clients ClientRepository,
	tx TxRunner,
) *SuggestionService {
	return &SuggestionService{suggestions: suggestions, clients: clients, tx: tx}
}

// Create регистрирует предложение правки от пользователя.
//
// Карточка клиента при этом НЕ МЕНЯЕТСЯ. Меняется она только в Approve.
func (s *SuggestionService) Create(
	ctx context.Context,
	clientID uuid.UUID,
	author model.AuthUser,
	payload model.SuggestionPayload,
) (*model.SuggestionDetailed, error) {
	log := logger.FromContext(ctx)

	// --- Проверка содержимого предложения ---

	// Пустое предложение бессмысленно: пользователь прислал {} или
	// объект без единого известного поля.
	if payload.IsEmpty() {
		return nil, apperr.Validation("payload must contain at least one field to change")
	}

	// Правила, которые нельзя выразить тегами binding
	// (см. комментарий к SuggestionPayload.Validate).
	if fieldErrors := payload.Validate(); len(fieldErrors) > 0 {
		return nil, apperr.Validation("payload validation failed").WithDetails(fieldErrors)
	}

	// --- Проверка существования клиента ---
	//
	// Строго говоря, необязательна: внешний ключ в базе и так не даст
	// вставить предложение для несуществующего клиента, и репозиторий
	// превратит это в 404. Но явная проверка даёт понятную ошибку
	// в подавляющем большинстве случаев, а гонку прикрывает внешний ключ.
	exists, err := s.clients.Exists(ctx, s.tx.DB(), clientID)
	if err != nil {
		return nil, fmt.Errorf("check client existence: %w", err)
	}
	if !exists {
		return nil, apperr.NotFound("client")
	}

	suggestion := &model.Suggestion{
		ID:       uuid.New(),
		ClientID: clientID,
		AuthorID: author.ID,
		Payload:  payload,
		Status:   model.SuggestionStatusPending,
	}

	if err := s.suggestions.Create(ctx, s.tx.DB(), suggestion); err != nil {
		// Здесь может прилететь Conflict: у автора уже есть
		// нерассмотренное предложение по этому клиенту.
		return nil, err
	}

	log.Info("suggestion created",
		slog.String("suggestion_id", suggestion.ID.String()),
		slog.String("client_id", clientID.String()),
		slog.String("author_id", author.ID.String()),
		// Список полей — не персональные данные, а метаинформация.
		// Логировать его можно и полезно.
		slog.Any("changed_fields", payload.ChangedFields()),
	)

	// Перечитываем с JOIN, чтобы вернуть клиенту email автора и имя клиента.
	return s.suggestions.GetByID(ctx, s.tx.DB(), suggestion.ID)
}

// List возвращает страницу предложений.
//
// Параметр authorID управляет видимостью:
//
//	nil    — админский режим: видны предложения всех авторов;
//	не nil — пользовательский: только свои.
//
// Решение о режиме принимает ХЕНДЛЕР, исходя из того, в какую группу
// роутов пришёл запрос. Сервис лишь честно исполняет.
func (s *SuggestionService) List(
	ctx context.Context,
	params model.ListSuggestionsQuery,
	authorID *uuid.UUID,
) ([]model.SuggestionDetailed, int, error) {
	params.ApplyDefaults()
	return s.suggestions.List(ctx, s.tx.DB(), params, authorID)
}

// GetByID возвращает предложение с проверкой права его видеть.
//
// Админ видит любое, обычный пользователь — только своё.
//
// # Почему при чужом предложении отдаём 404, а не 403
//
// 403 («вам сюда нельзя») подтверждает, что объект СУЩЕСТВУЕТ.
// Перебирая идентификаторы, можно составить карту чужих данных,
// не имея к ним доступа. 404 не даёт отличить «нет такого»
// от «есть, но не твоё» — и утечки не происходит.
func (s *SuggestionService) GetByID(
	ctx context.Context,
	id uuid.UUID,
	actor model.AuthUser,
) (*model.SuggestionDetailed, error) {
	suggestion, err := s.suggestions.GetByID(ctx, s.tx.DB(), id)
	if err != nil {
		return nil, err
	}

	if !actor.IsAdmin() && suggestion.AuthorID != actor.ID {
		return nil, apperr.NotFound("suggestion")
	}

	return suggestion, nil
}

// Approve одобряет предложение и ПРИМЕНЯЕТ его к карточке клиента.
//
// ============================================================================
//
//	САМЫЙ ВАЖНЫЙ МЕТОД ПРОЕКТА. Здесь происходят два изменения,
//	которые обязаны произойти ВМЕСТЕ ИЛИ НИКАК:
//
//	  1) карточка клиента меняется согласно payload;
//	  2) предложение переводится в статус approved.
//
//	Без транзакции возможен разрыв: упали между шагами — и получили
//	«предложение одобрено, а данные не изменились» либо наоборот.
//	Такие рассинхроны потом ищут неделями, потому что данные выглядят
//	правдоподобно.
//
// ============================================================================
func (s *SuggestionService) Approve(
	ctx context.Context,
	id uuid.UUID,
	reviewer model.AuthUser,
	comment *string,
) (*model.SuggestionDetailed, error) {
	log := logger.FromContext(ctx)

	err := s.tx.WithTx(ctx, func(ctx context.Context, tx db.Querier) error {
		// --- Шаг 1: читаем предложение С БЛОКИРОВКОЙ строки ---
		//
		// FOR UPDATE не даст второму администратору одобрить то же самое
		// предложение параллельно: он будет ждать нашего COMMIT.
		suggestion, err := s.suggestions.GetByIDForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}

		// --- Шаг 2: проверяем состояние ---
		//
		// Проверка ПОСЛЕ блокировки — принципиально. Проверив до неё,
		// мы бы прочитали состояние, которое к моменту записи уже устарело.
		if !suggestion.IsPending() {
			return apperr.Conflict(fmt.Sprintf(
				"suggestion is already %s", suggestion.Status))
		}

		// --- Шаг 3: читаем карточку клиента С БЛОКИРОВКОЙ ---
		//
		// ПОРЯДОК ЗАХВАТА БЛОКИРОВОК ВАЖЕН: сначала предложение,
		// потом клиент. Всегда одинаково, во всех методах.
		//
		// Если один код берёт блокировки в порядке A→B, а другой B→A,
		// две транзакции могут заблокировать друг друга насмерть.
		// Postgres такой дедлок обнаружит и убьёт одну из транзакций,
		// но клиент получит ошибку на ровном месте.
		//
		// Единый порядок захвата — стандартное лекарство от дедлоков.
		client, err := s.clients.GetByIDForUpdate(ctx, tx, suggestion.ClientID)
		if err != nil {
			// Клиента могли мягко удалить, пока предложение висело в очереди.
			// Применять правку не к чему — сообщаем честно.
			if apperr.IsNotFound(err) {
				return apperr.Conflict("client no longer exists or has been deleted")
			}
			return err
		}

		// --- Шаг 4: накладываем изменения на карточку ---
		//
		// Здесь работает семантика Optional: присутствующие поля меняются,
		// явные null очищаются, отсутствующие остаются как были.
		suggestion.Payload.Apply(client)

		// --- Шаг 5: сохраняем карточку ---
		//
		// Может вернуть Conflict, если предложенный email уже занят другим
		// клиентом. Это нормальный исход: транзакция откатится целиком,
		// предложение останется pending, админ увидит внятную ошибку.
		if err := s.clients.Update(ctx, tx, client); err != nil {
			return err
		}

		// --- Шаг 6: помечаем предложение одобренным ---
		//
		// Условие `status = 'pending'` внутри UPDATE — вторая линия защиты
		// поверх блокировки.
		if err := s.suggestions.Review(
			ctx, tx, id, model.SuggestionStatusApproved, reviewer.ID, comment,
		); err != nil {
			return err
		}

		return nil // возврат nil означает COMMIT
	})
	if err != nil {
		return nil, err
	}

	log.Info("suggestion approved",
		slog.String("suggestion_id", id.String()),
		slog.String("reviewer_id", reviewer.ID.String()),
	)

	// Перечитываем уже ПОСЛЕ коммита, обычным запросом.
	// Внутри транзакции этого делать не стоит: чем короче транзакция,
	// тем меньше время удержания блокировок.
	return s.suggestions.GetByID(ctx, s.tx.DB(), id)
}

// Reject отклоняет предложение. Карточка клиента при этом не меняется.
//
// Комментарий обязателен — автор должен понимать причину отказа.
// Требование проверяется дважды: тегом binding в DTO и здесь,
// потому что сервис может быть вызван не только из HTTP-хендлера.
func (s *SuggestionService) Reject(
	ctx context.Context,
	id uuid.UUID,
	reviewer model.AuthUser,
	comment string,
) (*model.SuggestionDetailed, error) {
	log := logger.FromContext(ctx)

	comment = strings.TrimSpace(comment)
	if comment == "" {
		return nil, apperr.Validation("comment is required when rejecting a suggestion")
	}

	// Транзакция здесь формально не обязательна — меняется одна строка,
	// а одиночный UPDATE в Postgres и так атомарен.
	//
	// Она нужна ради БЛОКИРОВКИ: без FOR UPDATE два администратора могли бы
	// одновременно прочитать статус pending, и второй получил бы невнятную
	// ошибку вместо понятного «уже рассмотрено».
	err := s.tx.WithTx(ctx, func(ctx context.Context, tx db.Querier) error {
		suggestion, err := s.suggestions.GetByIDForUpdate(ctx, tx, id)
		if err != nil {
			return err
		}

		if !suggestion.IsPending() {
			return apperr.Conflict(fmt.Sprintf(
				"suggestion is already %s", suggestion.Status))
		}

		return s.suggestions.Review(
			ctx, tx, id, model.SuggestionStatusRejected, reviewer.ID, &comment)
	})
	if err != nil {
		return nil, err
	}

	log.Info("suggestion rejected",
		slog.String("suggestion_id", id.String()),
		slog.String("reviewer_id", reviewer.ID.String()),
	)

	return s.suggestions.GetByID(ctx, s.tx.DB(), id)
}
