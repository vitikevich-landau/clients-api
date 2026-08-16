package middleware

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"os"
	"runtime/debug"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
	"github.com/vitikevich-landau/clients-api/internal/response"
)

// Recovery ловит панику в любом слое обработки и превращает её в ответ 500.
//
// # Почему это САМЫЙ ВНЕШНИЙ middleware
//
// Без него любая паника — разыменование nil, выход за границы среза,
// деление на ноль — кладёт ВЕСЬ процесс. Не один запрос, а весь сервис
// со всеми параллельными запросами.
//
// Стоять он должен снаружи всех остальных, чтобы поймать панику в них тоже.
//
// # Почему свой, а не gin.Recovery()
//
// Штатный пишет стектрейс обычным текстом в stderr — это ломает структурный
// формат логов, и такую запись не найти поиском по request_id.
// Наш пишет в slog со всеми полями запроса.
func Recovery(log *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		// defer с recover() обязан быть объявлен ДО c.Next():
		// он сработает при разворачивании стека, когда паника пойдёт наверх.
		defer func() {
			rec := recover()
			if rec == nil {
				return // паники не было — обычный выход
			}

			// --- Особый случай: клиент оборвал соединение ---
			//
			// «broken pipe» и «connection reset by peer» означают, что
			// пользователь закрыл вкладку или отвалилась сеть, пока мы
			// писали ответ. Это НЕ ошибка сервиса — писать сюда Error
			// и поднимать алерты бессмысленно.
			//
			// Отвечать тоже некому: соединения уже нет. Поэтому просто
			// помечаем запрос прерванным.
			if isBrokenPipe(rec) {
				log.Warn("client closed connection",
					slog.String("request_id", GetRequestID(c)),
					slog.String("path", c.Request.URL.Path),
				)
				c.Abort()
				return
			}

			// --- Настоящая паника: баг в коде ---
			log.Error("panic recovered",
				slog.String("request_id", GetRequestID(c)),
				slog.String("method", c.Request.Method),
				slog.String("path", c.Request.URL.Path),
				slog.Any("panic", rec),
				// Стектрейс — единственный способ понять, ГДЕ рвануло.
				// Строкой, а не через вложенную структуру: так его
				// нормально показывают все системы просмотра логов.
				slog.String("stack", string(debug.Stack())),
			)

			// Если ответ уже начали писать, заголовки ушли — второй раз
			// статус не поставить. Остаётся только оборвать соединение,
			// чтобы клиент увидел обрыв, а не «успешный» огрызок ответа.
			if c.Writer.Written() {
				c.Abort()
				return
			}

			// Клиенту — обезличенный 500. Ни текста паники, ни стектрейса:
			// это прямая подсказка атакующему о внутреннем устройстве.
			// Подробности остались в логе, найти их можно по request_id,
			// который клиент получил в заголовке ответа.
			response.Error(c, apperr.Internal(panicToError(rec)))
		}()

		c.Next()
	}
}

// isBrokenPipe распознаёт обрыв соединения клиентом.
//
// Проверка через errors.As по цепочке net.OpError → syscall.Errno была бы
// строже, но на разных платформах эти ошибки заворачиваются по-разному.
// Сравнение текста здесь надёжнее, и так же поступает сам gin.
func isBrokenPipe(rec any) bool {
	err, ok := rec.(error)
	if !ok {
		return false
	}

	var netErr *net.OpError
	if !errors.As(err, &netErr) {
		return false
	}

	var sysErr *os.SyscallError
	if !errors.As(netErr, &sysErr) {
		return false
	}

	msg := strings.ToLower(sysErr.Error())
	return strings.Contains(msg, "broken pipe") ||
		strings.Contains(msg, "connection reset by peer")
}

// panicToError приводит значение из recover() к error.
//
// panic() принимает any: паниковать можно строкой, числом, чем угодно.
// Приводим к единому виду, чтобы дальше работать с обычной ошибкой.
func panicToError(rec any) error {
	if err, ok := rec.(error); ok {
		return err
	}
	return &panicError{value: rec}
}

// panicError оборачивает не-error значение из recover().
type panicError struct {
	value any
}

func (e *panicError) Error() string {
	return fmt.Sprintf("panic: %v", e.value)
}

// Проверка на этапе компиляции, что тип реализует error.
var _ error = (*panicError)(nil)
