package middleware

import (
	"log/slog"

	"github.com/gin-gonic/gin"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
	"github.com/vitikevich-landau/clients-api/internal/logger"
	"github.com/vitikevich-landau/clients-api/internal/model"
	"github.com/vitikevich-landau/clients-api/internal/response"
)

// RequireRole пропускает дальше только пользователей с одной из указанных ролей.
//
// Работает ТОЛЬКО после middleware.Auth: пользователь должен быть
// уже разобран из токена и лежать в контексте.
//
// # Почему проверка прав здесь, а не в каждом хендлере
//
// Middleware вешается на ГРУППУ роутов. Добавил новый админский роут
// в группу — он автоматически защищён. Забыть невозможно.
//
// Проверка внутри каждого хендлера, наоборот, забывается легко:
// скопировал соседний обработчик, удалил «лишнюю» строчку — и роут открыт.
// Именно так появляется большинство дыр в правах доступа.
func RequireRole(roles ...model.Role) gin.HandlerFunc {
	// Множество собираем ОДИН РАЗ при регистрации роута, а не на каждый
	// запрос. Разница на паре ролей ничтожна, но привычка правильная:
	// всё, что можно посчитать заранее, считается заранее.
	allowed := make(map[model.Role]struct{}, len(roles))
	for _, r := range roles {
		allowed[r] = struct{}{}
	}

	return func(c *gin.Context) {
		user := MustGetAuthUser(c)

		if _, ok := allowed[user.Role]; !ok {
			// Отказ в правах — это событие безопасности, его логируем.
			// Единичный случай — обычно просто ошибка клиента,
			// но всплеск таких записей означает, что систему щупают.
			logger.FromContext(c.Request.Context()).Warn("access denied: insufficient role",
				slog.String("user_id", user.ID.String()),
				slog.String("user_role", user.Role.String()),
				slog.String("path", c.Request.URL.Path),
			)

			// 403, а не 401: пользователь ИЗВЕСТЕН, просто ему нельзя.
			// Отвечать 401 здесь было бы неправильно — клиент решил бы,
			// что надо перелогиниться, и ушёл бы в бесконечный цикл входа.
			response.Error(c, apperr.Forbidden("insufficient permissions"))
			return
		}

		c.Next()
	}
}

// RequireAdmin — частый случай: пускаем только администраторов.
//
// Обёртка ради читаемости роутера: admin.Use(middleware.RequireAdmin())
// понятнее, чем RequireRole(model.RoleAdmin).
func RequireAdmin() gin.HandlerFunc {
	return RequireRole(model.RoleAdmin)
}
