package middleware

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
	"github.com/vitikevich-landau/clients-api/internal/logger"
	"github.com/vitikevich-landau/clients-api/internal/response"
)

// Timeout ограничивает время обработки одного запроса.
//
// # Зачем
//
// Один зависший запрос держит соединение из пула базы. Десять зависших
// при MaxConns=10 исчерпают пул целиком — и ляжет ВЕСЬ сервис, включая
// те ручки, которые работают за миллисекунды. Таймаут превращает
// «полный отказ» в «медленно, но живём».
//
// # Как это работает
//
// Подменяем context.Context запроса на контекст с дедлайном. Дальше он
// едет через все слои — service, repository — и доезжает до pgx.
// Когда дедлайн наступает, контекст отменяется, и pgx ОТМЕНЯЕТ ЗАПРОС
// в самом Postgres. База перестаёт молотить работу, результат которой
// уже никому не нужен.
//
// Ровно тот же механизм срабатывает, когда пользователь просто закрывает
// вкладку: net/http отменяет контекст запроса сам.
//
// # Честное ограничение этой реализации
//
// Мы НЕ прерываем выполнение хендлера силой — в Go нельзя убить
// горутину извне. Если хендлер игнорирует ctx (например, крутит долгий
// цикл без обращений к базе), он доработает до конца. Дедлайн действует
// на всё, что уважает контекст: обращения к БД, HTTP-запросы наружу,
// select с ctx.Done().
//
// Более агрессивные схемы (запуск хендлера в отдельной горутине)
// приносят проблемы похуже: gin.Context не потокобезопасен, а «убитый»
// хендлер продолжает выполняться и может попытаться писать в уже
// закрытый ответ. Поэтому здесь простой и предсказуемый вариант.
func Timeout(timeout time.Duration) gin.HandlerFunc {
	return func(c *gin.Context) {
		ctx, cancel := context.WithTimeout(c.Request.Context(), timeout)

		// cancel обязателен ВСЕГДА, даже если дедлайн не наступил:
		// иначе контекст и связанный с ним таймер останутся в памяти
		// до истечения срока. На тысячах запросов это заметная утечка.
		defer cancel()

		c.Request = c.Request.WithContext(ctx)

		c.Next()

		// Проверяем на пути наружу: успели или нет.
		if !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return
		}

		logger.FromContext(ctx).Warn("request timed out",
			slog.String("path", c.Request.URL.Path),
			slog.Duration("timeout", timeout),
		)

		// Если хендлер уже начал писать ответ, второй раз статус
		// не поставить — заголовки ушли. Просто фиксируем в логе.
		if c.Writer.Written() {
			return
		}

		response.Error(c, apperr.Timeout("request processing timed out"))
	}
}
