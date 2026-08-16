package service

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"golang.org/x/crypto/bcrypt"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
	"github.com/vitikevich-landau/clients-api/internal/logger"
	"github.com/vitikevich-landau/clients-api/internal/model"
)

// bcryptCost — вычислительная сложность хеширования.
//
// Каждая единица УДВАИВАЕТ время работы. Смысл в том, чтобы проверка
// пароля была ощутимо дорогой: легитимный пользователь логинится раз в день
// и не заметит 100 мс, а перебор по украденной базе замедляется в тысячи раз.
//
// 12 — разумный современный минимум (примерно 200–300 мс на обычном железе).
// bcrypt.DefaultCost сейчас равен 10 — для нового кода этого уже маловато.
//
// Значение не берётся из конфига намеренно: оно записано ВНУТРЬ каждого хеша,
// поэтому старые пароли продолжают проверяться своим прежним cost даже после
// повышения этой константы.
const bcryptCost = 12

// dummyHashForTimingEqualization — заранее посчитанный bcrypt-хеш.
//
// Используется, когда пользователь с таким email не найден: мы всё равно
// прогоняем сравнение, чтобы ответ занял столько же времени, сколько занял бы
// при существующем пользователе. См. подробное объяснение в Login.
//
// Это хеш от строки, которую никто не подберёт, и он ни от чего не защищает
// сам по себе — важна только длительность вычисления.
var dummyHashForTimingEqualization = []byte(
	"$2a$12$N9qo8uLOickgx2ZMRZoMyeIjZAgcfl7p92ldGxad68LJZdL17lhWy")

// AuthService — регистрация и вход.
type AuthService struct {
	users  UserRepository
	tokens *TokenManager
	tx     TxRunner
}

// NewAuthService собирает сервис аутентификации.
func NewAuthService(users UserRepository, tokens *TokenManager, tx TxRunner) *AuthService {
	return &AuthService{users: users, tokens: tokens, tx: tx}
}

// Register создаёт нового пользователя и сразу выдаёт ему токен.
//
// # Ключевое правило безопасности
//
// Публичная регистрация ВСЕГДА выдаёт роль user. Роль не берётся из тела
// запроса ни при каких условиях — иначе любой желающий зарегистрировался бы
// администратором. Админов заводят отдельно: миграцией, служебной командой
// или другим админом.
func (s *AuthService) Register(ctx context.Context, req model.RegisterRequest) (*model.AuthResponse, error) {
	log := logger.FromContext(ctx)

	email := normalizeEmail(req.Email)

	// Предварительная проверка занятости адреса.
	//
	// Она НЕ защищает от дублей — это делает UNIQUE-индекс в базе,
	// а между проверкой и вставкой всегда есть окно для гонки.
	// Смысл проверки в другом: не тратить 200+ мс на bcrypt, если
	// адрес и так занят.
	exists, err := s.users.ExistsByEmail(ctx, s.tx.DB(), email)
	if err != nil {
		return nil, fmt.Errorf("check email availability: %w", err)
	}
	if exists {
		return nil, apperr.Conflict("user with this email already exists")
	}

	// Хешируем пароль. bcrypt сам генерирует случайную соль и вшивает её
	// в результат — отдельно хранить соль не нужно.
	hash, err := bcrypt.GenerateFromPassword([]byte(req.Password), bcryptCost)
	if err != nil {
		// Единственная реальная причина — пароль длиннее 72 байт.
		// Валидация max=72 в DTO это отсекает, но обработать надо.
		if errors.Is(err, bcrypt.ErrPasswordTooLong) {
			return nil, apperr.Validation("password is too long (max 72 bytes)").Wrap(err)
		}
		return nil, fmt.Errorf("hash password: %w", err)
	}

	user := &model.User{
		ID:           uuid.New(), // UUID генерируем сами, до похода в базу
		Email:        email,
		PasswordHash: string(hash),
		Role:         model.RoleUser, // жёстко, никаких вариантов
	}

	if err := s.users.Create(ctx, s.tx.DB(), user); err != nil {
		// Сюда попадаем, если между ExistsByEmail и Create кто-то успел
		// занять адрес. Репозиторий уже превратил нарушение UNIQUE
		// в apperr.Conflict — просто пробрасываем.
		return nil, err
	}

	// В лог — идентификатор, НЕ email и тем более не пароль.
	log.Info("user registered",
		slog.String("user_id", user.ID.String()),
		slog.String("role", user.Role.String()),
	)

	return s.buildAuthResponse(user)
}

// Login проверяет учётные данные и выдаёт токен.
//
// # Почему на все ошибки один и тот же ответ
//
// Если отвечать «пользователь не найден» и «неверный пароль» по-разному,
// форма входа превращается в инструмент разведки: атакующий перебирает
// адреса и узнаёт, кто зарегистрирован в системе. Дальше по этому списку
// идут фишинг и целевой подбор паролей.
//
// Поэтому оба случая дают одинаковый текст и одинаковый код 401.
func (s *AuthService) Login(ctx context.Context, req model.LoginRequest) (*model.AuthResponse, error) {
	log := logger.FromContext(ctx)

	email := normalizeEmail(req.Email)

	user, err := s.users.GetByEmail(ctx, s.tx.DB(), email)
	if err != nil {
		if apperr.IsNotFound(err) {
			// ================================================================
			//  ВЫРАВНИВАНИЕ ВРЕМЕНИ ОТВЕТА (защита от timing attack)
			// ================================================================
			//
			// Если при отсутствии пользователя просто вернуть ошибку,
			// ответ придёт за ~1 мс. А при существующем пользователе
			// с неверным паролем — за ~250 мс, потому что считался bcrypt.
			//
			// Разница в 250 раз прекрасно видна снаружи. Атакующему
			// достаточно замерить время ответа, чтобы понять, есть такой
			// пользователь или нет — и наша аккуратная одинаковая
			// формулировка ошибки не поможет.
			//
			// Лечится тем, что мы всё равно прогоняем сравнение —
			// с заранее заготовленным хешем. Время ответа выравнивается.
			_ = bcrypt.CompareHashAndPassword(
				dummyHashForTimingEqualization, []byte(req.Password))

			log.Warn("login failed: user not found")
			return nil, apperr.Unauthorized("invalid email or password")
		}
		return nil, fmt.Errorf("get user by email: %w", err)
	}

	// CompareHashAndPassword сам достаёт соль и cost из хеша
	// и сравнивает результат в постоянное время.
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(req.Password)); err != nil {
		log.Warn("login failed: wrong password",
			slog.String("user_id", user.ID.String()))
		// Текст ошибки ТОТ ЖЕ, что и при отсутствии пользователя.
		return nil, apperr.Unauthorized("invalid email or password")
	}

	log.Info("user logged in",
		slog.String("user_id", user.ID.String()),
		slog.String("role", user.Role.String()),
	)

	return s.buildAuthResponse(user)
}

// GetByID возвращает пользователя по идентификатору.
// Нужен для ручки «кто я» (/auth/me).
func (s *AuthService) GetByID(ctx context.Context, id uuid.UUID) (*model.User, error) {
	return s.users.GetByID(ctx, s.tx.DB(), id)
}

// buildAuthResponse выпускает токен и собирает ответ.
func (s *AuthService) buildAuthResponse(user *model.User) (*model.AuthResponse, error) {
	token, expiresAt, err := s.tokens.Issue(user)
	if err != nil {
		return nil, fmt.Errorf("issue token: %w", err)
	}

	return &model.AuthResponse{
		AccessToken: token,
		TokenType:   "Bearer",
		// Округляем вниз до секунд: доли секунды клиенту не нужны,
		// а time.Until даёт наносекунды.
		ExpiresIn: int(time.Until(expiresAt).Seconds()),
		User:      model.NewUserResponse(user),
	}, nil
}

// normalizeEmail приводит адрес к каноническому виду.
//
// Зачем: без нормализации " Ivan@Example.COM " и "ivan@example.com"
// окажутся разными пользователями, а человек будет уверен, что
// регистрировался один раз, и не сможет войти.
//
// Приведение к нижнему регистру формально не совсем корректно
// (по стандарту локальная часть адреса регистрозависима), но на практике
// все почтовые системы её регистр игнорируют, и все так делают.
func normalizeEmail(email string) string {
	return strings.ToLower(strings.TrimSpace(email))
}
