// Package model содержит структуры домена и DTO (объекты передачи данных).
//
// # Почему домен и DTO — разные структуры
//
// Соблазн велик: навесить на одну структуру и теги `db:"..."`, и `json:"..."`
// и использовать её везде. Так делать нельзя, и вот почему:
//
//	type User struct {
//	    PasswordHash string `db:"password_hash" json:"password_hash"` // ← УТЕЧКА
//	}
//
// Одна забытая строчка — и хеш пароля уезжает клиенту в JSON.
// Разделение доменной модели (что лежит в базе) и ответа (что видит клиент)
// делает такую утечку невозможной: в UserResponse просто нет этого поля.
package model

import (
	"time"

	"github.com/google/uuid"
)

// Role — роль пользователя в системе.
//
// Отдельный тип, а не просто string: компилятор не даст случайно
// передать в функцию, ждущую Role, произвольную строку из запроса.
type Role string

const (
	// RoleUser — обычный пользователь: смотрит клиентов
	// и ПРЕДЛАГАЕТ правки, которые уходят на модерацию.
	RoleUser Role = "user"

	// RoleAdmin — администратор: правит клиентов напрямую
	// и рассматривает предложения.
	RoleAdmin Role = "admin"
)

// Valid проверяет, что роль — одна из известных.
// Нужна при разборе JWT: в токене может прийти что угодно,
// включая роль, которой у нас нет.
func (r Role) Valid() bool {
	return r == RoleUser || r == RoleAdmin
}

// IsAdmin — короткая проверка прав.
func (r Role) IsAdmin() bool { return r == RoleAdmin }

// String реализует fmt.Stringer — удобно при логировании.
func (r Role) String() string { return string(r) }

// ---------------------------------------------------------------------------
// Доменная модель
// ---------------------------------------------------------------------------

// User — пользователь системы, как он лежит в таблице users.
//
// Теги `db:"..."` читает scany при раскладывании строки результата
// по полям структуры. Строго говоря, scany и сам умеет переводить
// FullName → full_name, но явные теги надёжнее: переименование поля
// в Go тогда не ломает молча маппинг.
//
// Тегов json здесь НЕТ намеренно — эта структура наружу не отдаётся.
type User struct {
	ID           uuid.UUID `db:"id"`
	Email        string    `db:"email"`
	PasswordHash string    `db:"password_hash"` // никогда не покидает сервер
	Role         Role      `db:"role"`
	CreatedAt    time.Time `db:"created_at"`
	UpdatedAt    time.Time `db:"updated_at"`
}

// AuthUser — сведения о текущем пользователе, извлечённые из JWT.
//
// Это НЕ то же самое, что User: здесь только то, что лежит в токене.
// Похода в базу на каждый запрос не происходит — в этом и смысл JWT.
//
// Обратная сторона: данные в токене могут устареть. Если админа разжаловали
// в юзеры, его старый токен продолжит давать админские права до истечения
// срока. Лечится либо коротким TTL, либо списком отозванных токенов,
// либо проверкой роли в базе на критичных операциях.
type AuthUser struct {
	ID    uuid.UUID
	Email string
	Role  Role
}

// IsAdmin — проверка прав текущего пользователя.
func (u AuthUser) IsAdmin() bool { return u.Role.IsAdmin() }

// ---------------------------------------------------------------------------
// DTO: запросы
// ---------------------------------------------------------------------------

// RegisterRequest — тело запроса на регистрацию.
//
// Теги `binding:"..."` — это правила go-playground/validator,
// который встроен в gin. При вызове c.ShouldBindJSON gin сначала
// разберёт JSON, потом прогонит структуру через эти правила.
type RegisterRequest struct {
	Email string `json:"email" binding:"required,email,max=255"`

	// max=72 — это не каприз, а ограничение bcrypt: алгоритм работает
	// максимум с 72 байтами. Более длинные пароли современные версии
	// x/crypto отвергают ошибкой (старые молча обрезали, что было хуже:
	// пароли "…длинный…AAA" и "…длинный…BBB" оказывались одинаковыми).
	Password string `json:"password" binding:"required,min=8,max=72"`
}

// LoginRequest — тело запроса на вход.
//
// Обрати внимание: здесь НЕТ min=8 на пароль. Требования к длине
// проверяются только при регистрации. Если валидировать их и при входе,
// мы подскажем атакующему формат пароля ещё до проверки учётки.
type LoginRequest struct {
	Email    string `json:"email" binding:"required,email"`
	Password string `json:"password" binding:"required"`
}

// ---------------------------------------------------------------------------
// DTO: ответы
// ---------------------------------------------------------------------------

// UserResponse — то, что видит клиент. Хеша пароля здесь нет и быть не может.
type UserResponse struct {
	ID        uuid.UUID `json:"id"`
	Email     string    `json:"email"`
	Role      Role      `json:"role"`
	CreatedAt time.Time `json:"created_at"`
}

// NewUserResponse превращает доменную модель в безопасный ответ.
func NewUserResponse(u *User) UserResponse {
	return UserResponse{
		ID:        u.ID,
		Email:     u.Email,
		Role:      u.Role,
		CreatedAt: u.CreatedAt,
	}
}

// AuthResponse — ответ на регистрацию и вход.
//
// Формат полей повторяет OAuth 2.0 (RFC 6749) — не потому, что мы делаем
// OAuth, а потому, что клиентские библиотеки такой ответ уже понимают.
type AuthResponse struct {
	AccessToken string `json:"access_token"`

	// TokenType всегда "Bearer" — подсказка клиенту, что токен
	// надо слать в заголовке Authorization: Bearer <token>.
	TokenType string `json:"token_type"`

	// ExpiresIn — сколько СЕКУНД токен ещё живёт.
	// Относительное время, а не абсолютное: клиенту не нужно
	// иметь правильно выставленные часы, чтобы понять срок.
	ExpiresIn int `json:"expires_in"`

	User UserResponse `json:"user"`
}
