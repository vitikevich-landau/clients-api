package model

import (
	"time"

	"github.com/google/uuid"
)

// ClientStatus — состояние карточки клиента.
type ClientStatus string

const (
	ClientStatusActive   ClientStatus = "active"   // работаем с клиентом
	ClientStatusArchived ClientStatus = "archived" // в архиве, но не удалён
)

// Valid проверяет, что статус — один из известных.
func (s ClientStatus) Valid() bool {
	return s == ClientStatusActive || s == ClientStatusArchived
}

// String реализует fmt.Stringer.
func (s ClientStatus) String() string { return string(s) }

// ---------------------------------------------------------------------------
// Доменная модель
// ---------------------------------------------------------------------------

// Client — карточка клиента, как она лежит в таблице clients.
//
// # Почему указатели у необязательных полей
//
// В базе email/phone/company/notes допускают NULL. В Go есть три способа
// это выразить:
//
//	string           — не отличить NULL от пустой строки "";
//	sql.NullString   — отличить можно, но получается {String:"", Valid:false},
//	                   и в JSON это выглядит уродливо: {"String":"","Valid":false};
//	*string          — nil = NULL, и в JSON автоматически превращается в null.
//
// Берём указатели: и с базой честно, и в JSON красиво.
// Цена — необходимость проверять на nil перед разыменованием.
type Client struct {
	ID       uuid.UUID    `db:"id"`
	FullName string       `db:"full_name"`
	Email    *string      `db:"email"`
	Phone    *string      `db:"phone"`
	Company  *string      `db:"company"`
	Notes    *string      `db:"notes"`
	Status   ClientStatus `db:"status"`

	CreatedAt time.Time `db:"created_at"`
	UpdatedAt time.Time `db:"updated_at"`

	// DeletedAt — метка мягкого удаления.
	// nil = запись живая. Наружу не отдаётся: клиенту API удалённые
	// записи вообще не видны, так что и поле ему ни к чему.
	DeletedAt *time.Time `db:"deleted_at"`
}

// IsDeleted сообщает, помечена ли карточка удалённой.
func (c *Client) IsDeleted() bool { return c.DeletedAt != nil }

// ---------------------------------------------------------------------------
// DTO: запросы
// ---------------------------------------------------------------------------

// CreateClientRequest — тело запроса на создание клиента (только админ).
//
// Разбор тегов binding на примере поля Email:
//
//	omitempty — если поле не передали (nil), остальные правила не применяются;
//	email     — если передали, значение обязано быть похоже на адрес почты;
//	max=255   — ограничение длины.
//
// Без omitempty правило email сработало бы и на отсутствующем поле,
// и создать клиента без почты стало бы невозможно.
type CreateClientRequest struct {
	FullName string  `json:"full_name" binding:"required,min=1,max=200"`
	Email    *string `json:"email"     binding:"omitempty,email,max=255"`
	Phone    *string `json:"phone"     binding:"omitempty,min=3,max=50"`
	Company  *string `json:"company"   binding:"omitempty,max=200"`
	Notes    *string `json:"notes"     binding:"omitempty,max=2000"`

	// Если статус не передали, применится ClientStatusActive.
	Status *ClientStatus `json:"status" binding:"omitempty,oneof=active archived"`
}

// UpdateClientRequest — тело запроса на прямое изменение клиента (только админ).
//
// Метод PUT по семантике HTTP означает ПОЛНУЮ ЗАМЕНУ ресурса,
// поэтому набор полей тот же, что при создании: не передал email —
// значит, email становится пустым. Это не баг, это смысл PUT.
//
// Частичное изменение — это PATCH, и в нашем сервисе его роль
// играет механизм предложений (см. SuggestionPayload).
type UpdateClientRequest struct {
	FullName string        `json:"full_name" binding:"required,min=1,max=200"`
	Email    *string       `json:"email"     binding:"omitempty,email,max=255"`
	Phone    *string       `json:"phone"     binding:"omitempty,min=3,max=50"`
	Company  *string       `json:"company"   binding:"omitempty,max=200"`
	Notes    *string       `json:"notes"     binding:"omitempty,max=2000"`
	Status   *ClientStatus `json:"status"    binding:"omitempty,oneof=active archived"`
}

// ListClientsQuery — параметры выборки списка клиентов.
//
// Тег `form:"..."` (а не json) — потому что значения приходят
// в query-строке: /clients?limit=20&offset=0&search=ivan
type ListClientsQuery struct {
	// Search — поиск по подстроке в имени и названии компании.
	Search string `form:"search" binding:"omitempty,max=200"`

	// Status — фильтр по статусу. Пусто = любой.
	Status string `form:"status" binding:"omitempty,oneof=active archived"`

	// Limit — сколько записей вернуть.
	//
	// ВЕРХНЯЯ ГРАНИЦА ОБЯЗАТЕЛЬНА. Без неё клиент пришлёт limit=1000000,
	// сервис попытается собрать миллион строк в память и ляжет.
	// Это не гипотетика, а типовой способ положить чужой API.
	Limit int `form:"limit" binding:"omitempty,min=1,max=100"`

	// Offset — сколько записей пропустить.
	//
	// Честное предупреждение: offset-пагинация деградирует на больших
	// смещениях — Postgres всё равно проходит и выбрасывает первые N строк.
	// На сотнях тысяч записей переходят на курсорную пагинацию
	// (WHERE created_at < :last_seen). Для нашего объёма offset достаточно.
	Offset int `form:"offset" binding:"omitempty,min=0"`

	// SortBy — поле сортировки.
	//
	// КРИТИЧНО: список допустимых значений жёстко ограничен через oneof.
	// Имя колонки нельзя передать в SQL параметром — только подставить
	// в текст запроса, а значит, без белого списка это прямая SQL-инъекция.
	SortBy string `form:"sort_by" binding:"omitempty,oneof=created_at updated_at full_name"`

	// SortOrder — направление сортировки.
	SortOrder string `form:"sort_order" binding:"omitempty,oneof=asc desc"`
}

// ApplyDefaults подставляет значения по умолчанию для незаполненных параметров.
//
// Почему отдельным методом, а не через envDefault-подобные теги:
// у gin такого механизма нет, а разбирать «0 — это не передали или
// передали ноль?» лучше в одном явном месте.
func (q *ListClientsQuery) ApplyDefaults() {
	const defaultLimit = 20

	if q.Limit == 0 {
		q.Limit = defaultLimit
	}
	if q.SortBy == "" {
		q.SortBy = "created_at"
	}
	if q.SortOrder == "" {
		q.SortOrder = "desc" // свежие сверху — то, что нужно в 99% случаев
	}
}

// ---------------------------------------------------------------------------
// DTO: ответы
// ---------------------------------------------------------------------------

// ClientResponse — карточка клиента в том виде, в каком её видит клиент API.
type ClientResponse struct {
	ID        uuid.UUID    `json:"id"`
	FullName  string       `json:"full_name"`
	Email     *string      `json:"email"`
	Phone     *string      `json:"phone"`
	Company   *string      `json:"company"`
	Notes     *string      `json:"notes"`
	Status    ClientStatus `json:"status"`
	CreatedAt time.Time    `json:"created_at"`
	UpdatedAt time.Time    `json:"updated_at"`
	// DeletedAt здесь нет: удалённые карточки клиенту не показываются вовсе.
}

// NewClientResponse превращает доменную модель в ответ API.
func NewClientResponse(c *Client) ClientResponse {
	return ClientResponse{
		ID:        c.ID,
		FullName:  c.FullName,
		Email:     c.Email,
		Phone:     c.Phone,
		Company:   c.Company,
		Notes:     c.Notes,
		Status:    c.Status,
		CreatedAt: c.CreatedAt,
		UpdatedAt: c.UpdatedAt,
	}
}

// NewClientResponses — то же самое для списка.
func NewClientResponses(clients []Client) []ClientResponse {
	// Именно make(..., 0, len), а не var out []ClientResponse:
	// пустой срез сериализуется в [], а nil-срез — в null.
	// Клиенту гораздо приятнее всегда получать массив,
	// чем городить проверку на null в каждом месте.
	out := make([]ClientResponse, 0, len(clients))
	for i := range clients {
		out = append(out, NewClientResponse(&clients[i]))
	}
	return out
}
