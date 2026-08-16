package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
	"github.com/vitikevich-landau/clients-api/internal/model"
	"github.com/vitikevich-landau/clients-api/internal/response"
)

// TokenParser — то, что нужно этому middleware от менеджера токенов.
//
// Интерфейс объявлен здесь, у потребителя (см. пояснение в service/interfaces.go).
// Реализуется структурой service.TokenManager, но пакет middleware
// про неё ничего не знает — и в тестах легко подменяется заглушкой.
type TokenParser interface {
	Parse(token string) (*model.AuthUser, error)
}

// Схема авторизации и её длина с пробелом — для разбора заголовка.
const (
	bearerScheme = "Bearer"
	bearerPrefix = bearerScheme + " "
)

// Auth проверяет JWT и кладёт пользователя в контекст запроса.
//
// Ожидаемый формат заголовка:
//
//	Authorization: Bearer eyJhbGciOiJIUzI1NiIs...
//
// При любой проблеме — нет заголовка, кривая схема, битый или истёкший
// токен — отвечаем 401 и ОБРЫВАЕМ цепочку: хендлер не выполнится.
//
// # Аутентификация против авторизации
//
// Этот middleware отвечает на вопрос «КТО ты» (аутентификация).
// На вопрос «МОЖНО ли тебе сюда» (авторизация) отвечает RequireRole.
// Два разных вопроса и два разных middleware — их часто путают.
func Auth(tokens TokenParser) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		if header == "" {
			response.Error(c, apperr.Unauthorized("authorization header is required"))
			// return после ответа ОБЯЗАТЕЛЕН: response.Error вызывает Abort,
			// но Abort не прерывает выполнение ЭТОЙ функции — только
			// цепочку следующих обработчиков.
			return
		}

		// Регистр схемы по RFC 7235 не важен, поэтому EqualFold,
		// а не обычное сравнение: некоторые клиенты шлют "bearer".
		if len(header) <= len(bearerPrefix) || !strings.EqualFold(header[:len(bearerPrefix)], bearerPrefix) {
			response.Error(c, apperr.Unauthorized(
				"authorization header must be in format: Bearer <token>"))
			return
		}

		rawToken := strings.TrimSpace(header[len(bearerPrefix):])
		if rawToken == "" {
			response.Error(c, apperr.Unauthorized("token is empty"))
			return
		}

		// Parse уже возвращает готовые apperr-ошибки с внятными
		// формулировками («token has expired», «invalid signature»).
		user, err := tokens.Parse(rawToken)
		if err != nil {
			response.Error(c, err)
			return
		}

		// Кладём пользователя в контекст. Дальше по цепочке его достанут
		// RequireRole и хендлеры через MustGetAuthUser.
		//
		// САМ ТОКЕН В КОНТЕКСТ НЕ КЛАДЁМ. Иначе он рано или поздно
		// попадёт в лог при отладке — а токен это фактически пароль,
		// действующий до конца срока.
		c.Set(ctxKeyAuthUser, *user)

		c.Next()
	}
}
