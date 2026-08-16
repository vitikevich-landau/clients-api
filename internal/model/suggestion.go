package model

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
)

// SuggestionStatus — состояние предложения правки.
type SuggestionStatus string

const (
	SuggestionStatusPending  SuggestionStatus = "pending"  // ждёт модерации
	SuggestionStatusApproved SuggestionStatus = "approved" // одобрено и применено
	SuggestionStatusRejected SuggestionStatus = "rejected" // отклонено
)

// Valid проверяет, что статус — один из известных.
func (s SuggestionStatus) Valid() bool {
	switch s {
	case SuggestionStatusPending, SuggestionStatusApproved, SuggestionStatusRejected:
		return true
	default:
		return false
	}
}

// String реализует fmt.Stringer.
func (s SuggestionStatus) String() string { return string(s) }

// ---------------------------------------------------------------------------
// Payload — предлагаемые изменения
// ---------------------------------------------------------------------------

// Названия полей в JSON-представлении payload.
// Вынесены в константы, потому что используются в трёх местах:
// при сериализации, при валидации и при сборке списка изменений.
const (
	fieldFullName = "full_name"
	fieldEmail    = "email"
	fieldPhone    = "phone"
	fieldCompany  = "company"
	fieldNotes    = "notes"
	fieldStatus   = "status"
)

// SuggestionPayload — набор предлагаемых изменений карточки клиента.
//
// Хранится в колонке payload типа JSONB. Каждое поле — Optional,
// поэтому payload точно передаёт намерение автора:
// какие поля менять, какие очистить, какие не трогать.
type SuggestionPayload struct {
	FullName Optional[string]       `json:"full_name"`
	Email    Optional[string]       `json:"email"`
	Phone    Optional[string]       `json:"phone"`
	Company  Optional[string]       `json:"company"`
	Notes    Optional[string]       `json:"notes"`
	Status   Optional[ClientStatus] `json:"status"`
}

// MarshalJSON собирает объект ТОЛЬКО из присутствующих полей.
//
// Без этого метода стандартная сериализация выдала бы все шесть ключей,
// и разница между «не трогать» и «очистить» потерялась бы при сохранении
// в базу. Именно поэтому map собирается вручную.
func (p SuggestionPayload) MarshalJSON() ([]byte, error) {
	m := make(map[string]any, 6)

	// Значение кладём как есть: Optional.Value — указатель,
	// и nil в нём превратится в JSON null. Это то, что нужно.
	if p.FullName.Present {
		m[fieldFullName] = p.FullName.Value
	}
	if p.Email.Present {
		m[fieldEmail] = p.Email.Value
	}
	if p.Phone.Present {
		m[fieldPhone] = p.Phone.Value
	}
	if p.Company.Present {
		m[fieldCompany] = p.Company.Value
	}
	if p.Notes.Present {
		m[fieldNotes] = p.Notes.Value
	}
	if p.Status.Present {
		m[fieldStatus] = p.Status.Value
	}

	return json.Marshal(m)
}

// IsEmpty — в payload нет ни одного поля.
// Пустое предложение бессмысленно и должно отвергаться.
func (p SuggestionPayload) IsEmpty() bool {
	return !p.FullName.Present &&
		!p.Email.Present &&
		!p.Phone.Present &&
		!p.Company.Present &&
		!p.Notes.Present &&
		!p.Status.Present
}

// ChangedFields возвращает отсортированный список изменяемых полей.
// Используется в ответе API и в логах — по нему сразу видно,
// что именно предлагают поменять, без разбора всего payload.
func (p SuggestionPayload) ChangedFields() []string {
	fields := make([]string, 0, 6)

	if p.FullName.Present {
		fields = append(fields, fieldFullName)
	}
	if p.Email.Present {
		fields = append(fields, fieldEmail)
	}
	if p.Phone.Present {
		fields = append(fields, fieldPhone)
	}
	if p.Company.Present {
		fields = append(fields, fieldCompany)
	}
	if p.Notes.Present {
		fields = append(fields, fieldNotes)
	}
	if p.Status.Present {
		fields = append(fields, fieldStatus)
	}

	// Сортировка нужна для стабильности: одинаковый вход всегда даёт
	// одинаковый выход. Это важно и для тестов, и для сравнения логов.
	sort.Strings(fields)
	return fields
}

// Validate проверяет содержимое payload.
//
// # Почему валидация здесь, а не тегами binding
//
// go-playground/validator не умеет заглядывать внутрь дженерика Optional[T]:
// для него это просто структура с полями Present и Value. Поэтому правила
// пишем руками.
//
// Это не костыль, а нормальная ситуация: теги хорошо описывают формат
// («это email», «длина до 200»), а бизнес-правила («имя нельзя очистить,
// потому что колонка NOT NULL») всё равно живут в коде.
//
// Возвращает карту поле → сообщение, готовую положить в details ответа.
// Пустая карта означает, что всё в порядке.
func (p SuggestionPayload) Validate() map[string]string {
	errs := make(map[string]string)

	// full_name — колонка NOT NULL, очистить её нельзя.
	if p.FullName.IsNull() {
		errs[fieldFullName] = "cannot be null: full name is required"
	}
	if v, ok := p.FullName.Get(); ok {
		trimmed := strings.TrimSpace(v)
		switch {
		case trimmed == "":
			errs[fieldFullName] = "must not be empty"
		case len(trimmed) > 200:
			errs[fieldFullName] = "must be at most 200 characters"
		}
	}

	// email можно очистить (null), но если передано значение —
	// оно обязано быть похоже на адрес.
	if v, ok := p.Email.Get(); ok {
		trimmed := strings.TrimSpace(v)
		switch {
		case trimmed == "":
			// Пустая строка — это, скорее всего, попытка очистить поле,
			// но выраженная неправильно. Подсказываем, как надо.
			errs[fieldEmail] = "must not be empty: send null to clear the field"
		case !looksLikeEmail(trimmed):
			errs[fieldEmail] = "must be a valid email address"
		case len(trimmed) > 255:
			errs[fieldEmail] = "must be at most 255 characters"
		}
	}

	if v, ok := p.Phone.Get(); ok {
		trimmed := strings.TrimSpace(v)
		switch {
		case trimmed == "":
			errs[fieldPhone] = "must not be empty: send null to clear the field"
		case len(trimmed) < 3:
			errs[fieldPhone] = "must be at least 3 characters"
		case len(trimmed) > 50:
			errs[fieldPhone] = "must be at most 50 characters"
		}
	}

	if v, ok := p.Company.Get(); ok {
		if len(strings.TrimSpace(v)) > 200 {
			errs[fieldCompany] = "must be at most 200 characters"
		}
	}

	if v, ok := p.Notes.Get(); ok {
		if len(v) > 2000 {
			errs[fieldNotes] = "must be at most 2000 characters"
		}
	}

	// status — колонка NOT NULL с CHECK, очистить нельзя.
	if p.Status.IsNull() {
		errs[fieldStatus] = "cannot be null"
	}
	if v, ok := p.Status.Get(); ok && !v.Valid() {
		errs[fieldStatus] = fmt.Sprintf("must be one of: %s, %s",
			ClientStatusActive, ClientStatusArchived)
	}

	return errs
}

// Apply накладывает предлагаемые изменения на карточку клиента.
//
// Вызывается ТОЛЬКО при одобрении предложения администратором,
// внутри транзакции. До этого момента карточка не меняется вообще.
//
// Логика по каждому полю:
//
//	поле отсутствует  → не трогаем
//	явный null        → ставим nil (очищаем), где колонка это допускает
//	есть значение     → записываем его
func (p SuggestionPayload) Apply(c *Client) {
	// full_name и status — NOT NULL, поэтому реагируем только
	// на непустое значение. Валидация уже отсекла попытку прислать null.
	if v, ok := p.FullName.Get(); ok {
		c.FullName = strings.TrimSpace(v)
	}
	if v, ok := p.Status.Get(); ok {
		c.Status = v
	}

	// Необязательные поля: присутствие ключа означает изменение,
	// а Value (nil или значение) кладётся в модель напрямую.
	if p.Email.Present {
		c.Email = trimPtr(p.Email.Value)
	}
	if p.Phone.Present {
		c.Phone = trimPtr(p.Phone.Value)
	}
	if p.Company.Present {
		c.Company = trimPtr(p.Company.Value)
	}
	if p.Notes.Present {
		c.Notes = p.Notes.Value // заметки не тримим: переносы строк осмысленны
	}
}

// trimPtr убирает пробелы по краям, сохраняя nil как nil.
func trimPtr(s *string) *string {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	return &trimmed
}

// looksLikeEmail — простая проверка формата адреса.
//
// Намеренно НЕ используется regexp по RFC 5322: полное регулярное
// выражение для email занимает несколько килобайт, тормозит и всё равно
// пропускает несуществующие адреса. Единственная настоящая проверка
// почты — отправить на неё письмо со ссылкой подтверждения.
// Здесь нам достаточно отсечь очевидный мусор.
func looksLikeEmail(s string) bool {
	at := strings.LastIndex(s, "@")
	if at <= 0 || at == len(s)-1 {
		return false // нет @, или он в самом начале, или в самом конце
	}
	domain := s[at+1:]
	if !strings.Contains(domain, ".") {
		return false // домен без точки
	}
	if strings.ContainsAny(s, " \t\n\r") {
		return false // пробелы внутри адреса
	}
	return true
}

// ---------------------------------------------------------------------------
// Доменная модель
// ---------------------------------------------------------------------------

// Suggestion — предложение правки, как оно лежит в базе.
type Suggestion struct {
	ID       uuid.UUID `db:"id"`
	ClientID uuid.UUID `db:"client_id"`
	AuthorID uuid.UUID `db:"author_id"`

	// Payload лежит в колонке JSONB. pgx сам сериализует структуру
	// в JSON при записи и разбирает обратно при чтении — благодаря
	// методам MarshalJSON/UnmarshalJSON, которые у нас уже есть.
	Payload SuggestionPayload `db:"payload"`

	Status SuggestionStatus `db:"status"`

	// Заполняются при рассмотрении, до этого NULL.
	ReviewerID    *uuid.UUID `db:"reviewer_id"`
	ReviewComment *string    `db:"review_comment"`

	CreatedAt  time.Time  `db:"created_at"`
	ReviewedAt *time.Time `db:"reviewed_at"`
}

// IsPending — предложение ещё ждёт решения.
func (s *Suggestion) IsPending() bool { return s.Status == SuggestionStatusPending }

// SuggestionDetailed — предложение вместе с данными из связанных таблиц.
//
// Встроенная (embedded) структура Suggestion: scany раскладывает поля
// вложенной структуры так же, как поля родительской — то есть колонка
// client_id из результата JOIN попадёт в Suggestion.ClientID.
//
// Зачем отдельный тип: в списке модерации админу нужно видеть, КТО
// предложил и К КАКОМУ клиенту относится правка. Тянуть эти данные
// отдельными запросами на каждую строку — классическая проблема N+1.
type SuggestionDetailed struct {
	Suggestion

	AuthorEmail   string  `db:"author_email"`
	ClientName    string  `db:"client_name"`
	ReviewerEmail *string `db:"reviewer_email"`
}

// ---------------------------------------------------------------------------
// DTO: запросы
// ---------------------------------------------------------------------------

// ListSuggestionsQuery — параметры выборки предложений.
type ListSuggestionsQuery struct {
	// Status — фильтр по статусу. Пусто = все.
	Status string `form:"status" binding:"omitempty,oneof=pending approved rejected"`

	// ClientID — показать предложения только по одному клиенту.
	ClientID string `form:"client_id" binding:"omitempty,uuid"`

	Limit  int `form:"limit"  binding:"omitempty,min=1,max=100"`
	Offset int `form:"offset" binding:"omitempty,min=0"`
}

// ApplyDefaults подставляет значения по умолчанию.
func (q *ListSuggestionsQuery) ApplyDefaults() {
	const defaultLimit = 20
	if q.Limit == 0 {
		q.Limit = defaultLimit
	}
}

// ApproveSuggestionRequest — тело запроса на одобрение.
// Комментарий необязателен: молчаливое согласие — нормально.
type ApproveSuggestionRequest struct {
	Comment *string `json:"comment" binding:"omitempty,max=1000"`
}

// RejectSuggestionRequest — тело запроса на отклонение.
//
// Здесь комментарий ОБЯЗАТЕЛЕН: автор должен понимать, почему его
// правку не приняли. Это не техническое требование, а продуктовое,
// и место ему — в валидации запроса.
type RejectSuggestionRequest struct {
	Comment string `json:"comment" binding:"required,min=1,max=1000"`
}

// ---------------------------------------------------------------------------
// DTO: ответы
// ---------------------------------------------------------------------------

// SuggestionResponse — предложение в том виде, в каком его видит клиент API.
type SuggestionResponse struct {
	ID       uuid.UUID `json:"id"`
	ClientID uuid.UUID `json:"client_id"`
	AuthorID uuid.UUID `json:"author_id"`

	// Payload сериализуется нашим MarshalJSON — то есть содержит
	// только те поля, которые реально предложены к изменению.
	Payload SuggestionPayload `json:"payload"`

	// ChangedFields — плоский список имён полей.
	// Дублирует информацию из payload, но позволяет фронтенду
	// нарисовать "предложено изменить: телефон, заметки"
	// без разбора структуры payload.
	ChangedFields []string `json:"changed_fields"`

	Status SuggestionStatus `json:"status"`

	ReviewerID    *uuid.UUID `json:"reviewer_id"`
	ReviewComment *string    `json:"review_comment"`

	CreatedAt  time.Time  `json:"created_at"`
	ReviewedAt *time.Time `json:"reviewed_at"`

	// Поля из связанных таблиц. Заполняются не всегда,
	// поэтому omitempty — чтобы не слать пустые строки без нужды.
	AuthorEmail   string  `json:"author_email,omitempty"`
	ClientName    string  `json:"client_name,omitempty"`
	ReviewerEmail *string `json:"reviewer_email,omitempty"`
}

// NewSuggestionResponse превращает доменную модель в ответ API.
func NewSuggestionResponse(s *Suggestion) SuggestionResponse {
	return SuggestionResponse{
		ID:            s.ID,
		ClientID:      s.ClientID,
		AuthorID:      s.AuthorID,
		Payload:       s.Payload,
		ChangedFields: s.Payload.ChangedFields(),
		Status:        s.Status,
		ReviewerID:    s.ReviewerID,
		ReviewComment: s.ReviewComment,
		CreatedAt:     s.CreatedAt,
		ReviewedAt:    s.ReviewedAt,
	}
}

// NewSuggestionDetailedResponse — то же самое, но с данными из JOIN.
func NewSuggestionDetailedResponse(s *SuggestionDetailed) SuggestionResponse {
	resp := NewSuggestionResponse(&s.Suggestion)
	resp.AuthorEmail = s.AuthorEmail
	resp.ClientName = s.ClientName
	resp.ReviewerEmail = s.ReviewerEmail
	return resp
}

// NewSuggestionDetailedResponses — то же самое для списка.
func NewSuggestionDetailedResponses(items []SuggestionDetailed) []SuggestionResponse {
	out := make([]SuggestionResponse, 0, len(items))
	for i := range items {
		out = append(out, NewSuggestionDetailedResponse(&items[i]))
	}
	return out
}
