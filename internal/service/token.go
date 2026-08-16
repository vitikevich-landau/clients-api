package service

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
	"github.com/vitikevich-landau/clients-api/internal/config"
	"github.com/vitikevich-landau/clients-api/internal/model"
)

// Claims — полезная нагрузка нашего JWT.
//
// Токен состоит из трёх частей, разделённых точками:
//
//	header.payload.signature
//
// Первые две — это обычный base64 от JSON. ИХ МОЖЕТ ПРОЧИТАТЬ КТО УГОДНО,
// открыв jwt.io. JWT НЕ шифрует данные, он их только ПОДПИСЫВАЕТ.
//
// Отсюда правило: в токен кладём только то, что не жалко показать —
// идентификатор, роль, срок. Никаких паролей, телефонов, внутренних флагов.
//
// Подпись гарантирует, что содержимое не подменили: поменял роль
// на "admin" — подпись перестала сходиться, сервер отверг токен.
type Claims struct {
	// RegisteredClaims — стандартные поля из RFC 7519:
	// sub (кто), exp (до когда), iat (когда выдан), iss (кем выдан), jti (id токена).
	jwt.RegisteredClaims

	// Свои поля. Роль в токене избавляет от похода в базу
	// на каждый запрос — в этом и смысл JWT.
	Email string     `json:"email"`
	Role  model.Role `json:"role"`
}

// TokenManager выпускает и проверяет JWT.
type TokenManager struct {
	secret []byte
	ttl    time.Duration
	issuer string
}

// NewTokenManager создаёт менеджер токенов.
func NewTokenManager(cfg config.JWTConfig) *TokenManager {
	return &TokenManager{
		secret: []byte(cfg.Secret),
		ttl:    cfg.TTL,
		issuer: cfg.Issuer,
	}
}

// TTL возвращает срок жизни выдаваемых токенов.
func (m *TokenManager) TTL() time.Duration { return m.ttl }

// Issue выпускает токен для пользователя.
//
// Вторым значением возвращается момент истечения — он нужен,
// чтобы отдать клиенту expires_in.
func (m *TokenManager) Issue(u *model.User) (string, time.Time, error) {
	now := time.Now()
	expiresAt := now.Add(m.ttl)

	claims := Claims{
		RegisteredClaims: jwt.RegisteredClaims{
			// sub — идентификатор владельца токена.
			Subject: u.ID.String(),

			// exp — после этого момента токен недействителен.
			ExpiresAt: jwt.NewNumericDate(expiresAt),

			// iat — когда выдан. Пригодится, если понадобится
			// отозвать все токены, выданные до определённого времени.
			IssuedAt: jwt.NewNumericDate(now),

			// nbf — «не раньше чем». Защищает от игры с рассинхроном часов.
			NotBefore: jwt.NewNumericDate(now),

			// iss — кто выдал. Проверяется при разборе: токен от чужого
			// сервиса с тем же секретом не пройдёт.
			Issuer: m.issuer,

			// jti — уникальный идентификатор токена.
			// Нужен, если появится чёрный список отозванных токенов.
			ID: uuid.NewString(),
		},
		Email: u.Email,
		Role:  u.Role,
	}

	// HS256 — симметричная подпись: один секрет и подписывает, и проверяет.
	// Подходит, когда токены выпускает и проверяет один сервис.
	//
	// Если проверять токены должны ДРУГИЕ сервисы, берут RS256/ES256:
	// подписывает приватный ключ, проверяет публичный, и раздавать
	// публичный ключ безопасно.
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)

	signed, err := token.SignedString(m.secret)
	if err != nil {
		return "", time.Time{}, fmt.Errorf("sign jwt: %w", err)
	}

	return signed, expiresAt, nil
}

// Parse проверяет токен и достаёт из него сведения о пользователе.
//
// Возвращает готовые к использованию apperr-ошибки: этот метод
// вызывается из middleware, которому остаётся только отдать их клиенту.
func (m *TokenManager) Parse(tokenString string) (*model.AuthUser, error) {
	claims := &Claims{}

	token, err := jwt.ParseWithClaims(
		tokenString,
		claims,
		// keyFunc возвращает ключ для проверки подписи.
		func(t *jwt.Token) (any, error) { return m.secret, nil },

		// ============================================================
		//  WithValidMethods — САМАЯ ВАЖНАЯ СТРОКА ВО ВСЁМ ФАЙЛЕ.
		// ============================================================
		//
		// Без неё библиотека доверяет полю "alg" из ЗАГОЛОВКА ТОКЕНА,
		// то есть из данных, которые прислал сам атакующий.
		// Это открывает две классические атаки:
		//
		//  1. alg=none — «токен без подписи». Библиотека, которая верит
		//     заголовку, пропускает такой токен как валидный.
		//     Подделать роль admin становится тривиально.
		//
		//  2. Подмена RS256 на HS256. Если сервер использует пару ключей,
		//     атакующий берёт ПУБЛИЧНЫЙ ключ (он общедоступен), подписывает
		//     им токен как HMAC и меняет alg на HS256. Сервер честно
		//     проверяет HMAC тем же публичным ключом — сходится.
		//
		// Явный белый список алгоритмов закрывает оба варианта:
		// заголовок токена больше ни на что не влияет.
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),

		// Проверяем, что токен выпустили мы.
		jwt.WithIssuer(m.issuer),

		// Токен без срока годности недопустим: он был бы вечным пропуском.
		jwt.WithExpirationRequired(),
	)
	if err != nil {
		// Разбираем причину, чтобы отдать клиенту понятное сообщение.
		// Истёкший токен — штатная ситуация (надо обновить),
		// а испорченная подпись — повод насторожиться.
		switch {
		case errors.Is(err, jwt.ErrTokenExpired):
			return nil, apperr.Unauthorized("token has expired").Wrap(err)
		case errors.Is(err, jwt.ErrTokenNotValidYet):
			return nil, apperr.Unauthorized("token is not valid yet").Wrap(err)
		case errors.Is(err, jwt.ErrTokenSignatureInvalid):
			return nil, apperr.Unauthorized("token signature is invalid").Wrap(err)
		default:
			return nil, apperr.Unauthorized("invalid token").Wrap(err)
		}
	}

	// Страховка: ParseWithClaims при отсутствии ошибки уже гарантирует
	// валидность, но явная проверка стоит ноль и защищает от сюрпризов
	// при обновлении библиотеки.
	if !token.Valid {
		return nil, apperr.Unauthorized("invalid token")
	}

	userID, err := uuid.Parse(claims.Subject)
	if err != nil {
		return nil, apperr.Unauthorized("invalid token subject").Wrap(err)
	}

	// Роль пришла из токена — то есть, формально, снаружи.
	// Подпись гарантирует, что её не подменили ПОСЛЕ выдачи, но не
	// гарантирует, что мы не выдали когда-то токен с ролью, которой
	// больше нет в системе (например, после рефакторинга ролей).
	if !claims.Role.Valid() {
		return nil, apperr.Unauthorized("invalid role in token")
	}

	return &model.AuthUser{
		ID:    userID,
		Email: claims.Email,
		Role:  claims.Role,
	}, nil
}
