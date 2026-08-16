package middleware

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/vitikevich-landau/clients-api/internal/logger"
)

// Logger пишет по одной структурной записи на каждый запрос
// и прокидывает подготовленный логгер вниз по слоям.
//
// # Здесь лучше всего видно устройство «луковицы»
//
// Код до c.Next() — путь ВНУТРЬ: засекаем время, готовим логгер.
// c.Next() — передача управления следующему слою и дальше хендлеру.
// Код после c.Next() — путь НАРУЖУ: статус ответа и длительность
// уже известны, можно писать итоговую запись.
func Logger(baseLogger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		// ---------- путь внутрь ----------

		start := time.Now()
		path := c.Request.URL.Path
		rawQuery := c.Request.URL.RawQuery

		// Собираем логгер, в который УЖЕ вшиты поля запроса.
		// Дальше любой слой просто пишет log.Info("..."), а request_id,
		// метод и путь добавляются к записи сами.
		reqLogger := baseLogger.With(
			slog.String("request_id", GetRequestID(c)),
			slog.String("method", c.Request.Method),
			slog.String("path", path),
		)

		// Кладём логгер в context.Context самого запроса — именно оттуда
		// его достанут service и repository через logger.FromContext.
		//
		// ВАЖНО: gin.Context и context.Context — РАЗНЫЕ вещи.
		// Чтобы значение доехало до слоёв, ничего не знающих про gin,
		// надо подменить контекст у *http.Request. Отсюда эта строка.
		ctx := logger.ToContext(c.Request.Context(), reqLogger)
		c.Request = c.Request.WithContext(ctx)

		// ---------- проваливаемся глубже ----------

		c.Next()

		// ---------- путь наружу ----------

		duration := time.Since(start)
		status := c.Writer.Status()

		attrs := []slog.Attr{
			slog.Int("status", status),
			// Миллисекунды числом, а не строкой "45ms":
			// по числу можно строить графики и искать «дольше 500 мс»,
			// по строке — нет.
			slog.Int64("duration_ms", duration.Milliseconds()),
			slog.Int("response_size", c.Writer.Size()),
			slog.String("client_ip", c.ClientIP()),
		}

		if rawQuery != "" {
			// Строка запроса может содержать поисковые слова —
			// это метаданные, а не персональные данные, писать можно.
			// А вот тело запроса не логируем НИКОГДА: там пароли.
			attrs = append(attrs, slog.String("query", rawQuery))
		}

		// Если по пути кто-то положил ошибки в c.Errors — добавим их.
		if len(c.Errors) > 0 {
			attrs = append(attrs, slog.String("errors", c.Errors.String()))
		}

		// Уровень выбираем по коду ответа — это принципиально.
		//
		// 5xx — ЭТО НАША ВИНА: сервис сломался, надо чинить → Error.
		// 4xx — вина клиента: не тот токен, кривой JSON, нет объекта → Warn.
		//        Писать сюда Error нельзя: сотня 404 в час не авария,
		//        но она утопит в шуме единственный настоящий сбой.
		// 2xx/3xx — норма → Info.
		msg := "request handled"
		switch {
		case status >= 500:
			logAttrs(c, reqLogger, slog.LevelError, msg, attrs)
		case status >= 400:
			logAttrs(c, reqLogger, slog.LevelWarn, msg, attrs)
		default:
			logAttrs(c, reqLogger, slog.LevelInfo, msg, attrs)
		}
	}
}

// logAttrs — обёртка над LogAttrs, чтобы не повторять её вызов трижды.
//
// LogAttrs (а не Info/Warn/Error) выбран намеренно: он принимает уже
// готовый []slog.Attr и не тратит время на приведение типов через any.
// На горячем пути, где запись делается на каждый запрос, это заметно.
func logAttrs(c *gin.Context, l *slog.Logger, level slog.Level, msg string, attrs []slog.Attr) {
	l.LogAttrs(c.Request.Context(), level, msg, attrs...)
}
