package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/google/uuid"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
	"github.com/vitikevich-landau/clients-api/internal/db"
	"github.com/vitikevich-landau/clients-api/internal/model"
)

// SuggestionRepository — доступ к таблице client_suggestions.
type SuggestionRepository struct{}

// NewSuggestionRepository создаёт репозиторий предложений.
func NewSuggestionRepository() *SuggestionRepository {
	return &SuggestionRepository{}
}

// Имя частичного уникального индекса из миграции 00003:
// один автор — одно НЕРАССМОТРЕННОЕ предложение на клиента.
const constraintOnePendingPerAuthorClient = "ux_suggestions_one_pending_per_author_client"

// suggestionColumns — колонки самой таблицы, с префиксом s.
// Префикс обязателен: в запросах с JOIN без него Postgres не поймёт,
// из какой таблицы брать id.
const suggestionColumns = `s.id, s.client_id, s.author_id, s.payload, s.status,
	s.reviewer_id, s.review_comment, s.created_at, s.reviewed_at`

// suggestionColumnsPlain — те же колонки без префикса таблицы,
// для запросов без JOIN. Продублировано намеренно: вырезать префикс
// через strings.ReplaceAll было бы короче, но такой «умный» код ломается
// молча, стоит появиться колонке с буквами "s." внутри имени.
const suggestionColumnsPlain = `id, client_id, author_id, payload, status,
	reviewer_id, review_comment, created_at, reviewed_at`

// suggestionDetailedColumns — то же плюс поля из связанных таблиц.
//
// Псевдонимы (AS author_email) обязаны совпадать с тегами db:"..."
// в структуре model.SuggestionDetailed — по ним scany и раскладывает
// результат.
const suggestionDetailedColumns = suggestionColumns + `,
	author.email    AS author_email,
	c.full_name     AS client_name,
	reviewer.email  AS reviewer_email`

// suggestionJoins — общая часть FROM для «подробных» выборок.
//
// # Зачем JOIN, а не отдельные запросы
//
// Админу в очереди модерации нужно видеть, кто предложил правку
// и по какому клиенту. Если тянуть это отдельными запросами на каждую
// строку списка, получится проблема N+1: одна выборка списка + N выборок
// автора + N выборок клиента. Двадцать предложений превращаются
// в сорок один запрос к базе. JOIN делает это одним.
//
// LEFT JOIN для reviewer — потому что у нерассмотренных предложений
// его просто нет. Обычный (INNER) JOIN выкинул бы такие строки
// из результата целиком, и очередь модерации оказалась бы всегда пустой.
const suggestionJoins = `
	FROM client_suggestions s
	JOIN users   author   ON author.id   = s.author_id
	JOIN clients c        ON c.id        = s.client_id
	LEFT JOIN users reviewer ON reviewer.id = s.reviewer_id`

// Create вставляет новое предложение правки.
func (r *SuggestionRepository) Create(ctx context.Context, q db.Querier, s *model.Suggestion) error {
	const query = `
		INSERT INTO client_suggestions (id, client_id, author_id, payload, status)
		VALUES ($1, $2, $3, $4, $5)
		RETURNING created_at`

	// $4 — структура model.SuggestionPayload. pgx видит, что колонка
	// имеет тип jsonb, и сериализует значение через json.Marshal,
	// то есть через наш MarshalJSON. Ничего конвертировать вручную не нужно.
	err := q.QueryRow(ctx, query, s.ID, s.ClientID, s.AuthorID, s.Payload, s.Status).
		Scan(&s.CreatedAt)
	if err != nil {
		if db.IsUniqueViolation(err, constraintOnePendingPerAuthorClient) {
			return apperr.Conflict(
				"you already have a pending suggestion for this client").Wrap(err)
		}
		// Внешний ключ нарушен — значит, клиента с таким id не существует.
		// Он мог быть удалён между проверкой и вставкой (та же гонка,
		// что и с уникальностью).
		if db.IsForeignKeyViolation(err) {
			return apperr.NotFound("client").Wrap(err)
		}
		return fmt.Errorf("insert suggestion: %w", err)
	}

	return nil
}

// GetByID возвращает предложение вместе с данными автора, клиента и рецензента.
func (r *SuggestionRepository) GetByID(
	ctx context.Context,
	q db.Querier,
	id uuid.UUID,
) (*model.SuggestionDetailed, error) {
	query := `SELECT ` + suggestionDetailedColumns + suggestionJoins + `
		WHERE s.id = $1`

	var s model.SuggestionDetailed
	if err := pgxscan.Get(ctx, q, &s, query, id); err != nil {
		if pgxscan.NotFound(err) {
			return nil, apperr.NotFound("suggestion")
		}
		return nil, fmt.Errorf("select suggestion by id: %w", err)
	}

	return &s, nil
}

// GetByIDForUpdate читает предложение с блокировкой строки до конца транзакции.
//
// # Зачем
//
// Два администратора одновременно нажали «Одобрить» на одном предложении.
// Без блокировки оба увидят статус pending, оба применят payload к карточке
// клиента — правка наложится ДВАЖДЫ, и в истории появятся два решения.
//
// FOR UPDATE выстраивает их в очередь: второй дождётся коммита первого,
// увидит статус approved и получит понятную ошибку 409.
//
// Тонкость: FOR UPDATE нельзя ставить на запрос с LEFT JOIN без указания
// таблицы — Postgres откажется блокировать «внешнюю» сторону соединения.
// Поэтому здесь простой SELECT без JOIN, только по нужной таблице.
func (r *SuggestionRepository) GetByIDForUpdate(
	ctx context.Context,
	q db.Querier,
	id uuid.UUID,
) (*model.Suggestion, error) {
	query := `SELECT ` + suggestionColumnsPlain + `
		FROM client_suggestions
		WHERE id = $1
		FOR UPDATE`

	var s model.Suggestion
	if err := pgxscan.Get(ctx, q, &s, query, id); err != nil {
		if pgxscan.NotFound(err) {
			return nil, apperr.NotFound("suggestion")
		}
		return nil, fmt.Errorf("select suggestion by id for update: %w", err)
	}

	return &s, nil
}

// Review проставляет результат рассмотрения предложения.
//
// Условие `AND status = 'pending'` в WHERE — вторая линия защиты
// поверх блокировки FOR UPDATE. Даже если кто-то вызовет этот метод
// вне транзакции, повторно рассмотреть уже рассмотренное предложение
// не получится: запрос просто не найдёт строку.
//
// Такой приём называется оптимистической проверкой состояния прямо
// в UPDATE — дёшево и надёжно.
func (r *SuggestionRepository) Review(
	ctx context.Context,
	q db.Querier,
	id uuid.UUID,
	status model.SuggestionStatus,
	reviewerID uuid.UUID,
	comment *string,
) error {
	const query = `
		UPDATE client_suggestions
		SET status         = $2,
		    reviewer_id    = $3,
		    review_comment = $4,
		    reviewed_at    = now()
		WHERE id = $1 AND status = 'pending'`

	tag, err := q.Exec(ctx, query, id, status, reviewerID, comment)
	if err != nil {
		return fmt.Errorf("update suggestion review: %w", err)
	}

	if tag.RowsAffected() == 0 {
		// Строки нет либо она уже не pending.
		// Различить эти случаи здесь нельзя — этим займётся service,
		// который читал предложение перед вызовом.
		return apperr.Conflict("suggestion is not pending anymore")
	}

	return nil
}

// List возвращает страницу предложений и общее количество подходящих.
//
// Параметр authorID задаёт режим работы:
//
//	nil      — админский режим: видны предложения всех авторов;
//	не nil   — пользовательский: видны только свои.
//
// # Почему фильтр «только свои» именно здесь, а не в хендлере
//
// Если бы отбор шёл после выборки, база сначала отдала бы чужие данные
// в память приложения. Одна ошибка в коде выше — и они уедут клиенту.
// Ограничение на уровне SQL означает, что чужие строки просто
// не покидают базу.
func (r *SuggestionRepository) List(
	ctx context.Context,
	q db.Querier,
	params model.ListSuggestionsQuery,
	authorID *uuid.UUID,
) ([]model.SuggestionDetailed, int, error) {
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 5)

	if authorID != nil {
		args = append(args, *authorID)
		conditions = append(conditions, fmt.Sprintf("s.author_id = $%d", len(args)))
	}

	if params.Status != "" {
		args = append(args, params.Status)
		conditions = append(conditions, fmt.Sprintf("s.status = $%d", len(args)))
	}

	if params.ClientID != "" {
		args = append(args, params.ClientID)
		conditions = append(conditions, fmt.Sprintf("s.client_id = $%d", len(args)))
	}

	// WHERE TRUE — приём, позволяющий не городить условие
	// «если фильтров нет, то WHERE вообще не писать».
	// Планировщик Postgres выбрасывает TRUE и никакой платы за это нет.
	where := "TRUE"
	if len(conditions) > 0 {
		where = strings.Join(conditions, " AND ")
	}

	// Считаем по самой таблице, без JOIN: связанные данные на количество
	// не влияют (JOIN здесь всегда однозначные), а лишние соединения
	// таблиц замедлили бы подсчёт.
	var total int
	countQuery := `SELECT count(*) FROM client_suggestions s WHERE ` + where
	if err := q.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count suggestions: %w", err)
	}

	if total == 0 {
		return []model.SuggestionDetailed{}, 0, nil
	}

	args = append(args, params.Limit)
	limitPlaceholder := len(args)
	args = append(args, params.Offset)
	offsetPlaceholder := len(args)

	// Сортировка жёстко зафиксирована: свежие сверху.
	// Пользовательской сортировки здесь нет, поэтому и белый список
	// колонок не нужен — подставлять нечего.
	listQuery := fmt.Sprintf(`
		SELECT %s %s
		WHERE %s
		ORDER BY s.created_at DESC, s.id ASC
		LIMIT $%d OFFSET $%d`,
		suggestionDetailedColumns, suggestionJoins, where,
		limitPlaceholder, offsetPlaceholder,
	)

	var items []model.SuggestionDetailed
	if err := pgxscan.Select(ctx, q, &items, listQuery, args...); err != nil {
		return nil, 0, fmt.Errorf("select suggestions: %w", err)
	}

	return items, total, nil
}
