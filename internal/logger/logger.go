// Package logger настраивает структурное логирование на базе log/slog
// из стандартной библиотеки Go (доступен начиная с Go 1.21).
//
// # Зачем структурные логи
//
// Раньше писали текстом:
//
//	2026-08-16 12:00:01 User 42 created client 1337 in 45ms
//
// Человеку красиво, но найти "все действия юзера 42 за час" можно только
// регулярками, и они ломаются, как только кто-то поправит формулировку.
//
// Структурный лог — это не строка, а НАБОР ПОЛЕЙ:
//
//	{"time":"...","level":"INFO","msg":"client created",
//	 "user_id":"...","client_id":"...","duration_ms":45,"request_id":"a1b2c3"}
//
// Такой лог кладётся в Loki / ELK / Datadog, и по нему делаются запросы
// вида `user_id="..." AND level="ERROR"`. Ради этого всё и затевается.
//
// # Куда писать
//
// В stdout. Не в файл. Ротацией и доставкой логов занимается
// инфраструктура (Docker, k8s, systemd) — это не задача приложения.
package logger

import (
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/lmittmann/tint"

	"github.com/vitikevich-landau/clients-api/internal/config"
)

// New создаёт логгер по конфигурации.
//
// В проде — JSON-хендлер из стандартной библиотеки.
// Локально — tint: тот же slog, но с цветным человекочитаемым выводом.
func New(cfg config.LogConfig, out io.Writer) *slog.Logger {
	level := parseLevel(cfg.Level)

	var handler slog.Handler
	switch cfg.Format {
	case "text":
		handler = tint.NewHandler(out, &tint.Options{
			Level:      level,
			AddSource:  cfg.AddSource,
			TimeFormat: time.TimeOnly, // локально дата не нужна, только время
		})
	default: // "json"
		handler = slog.NewJSONHandler(out, &slog.HandlerOptions{
			Level:     level,
			AddSource: cfg.AddSource,
		})
	}

	return slog.New(handler)
}

// parseLevel переводит строку из конфига в уровень slog.
// Значение уже провалидировано в config.Validate(), поэтому
// default здесь — просто страховка.
func parseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// Правила уровней — коротко, чтобы не спорить в код-ревью:
//
//	Debug — детали для разработки. В проде выключены.
//	Info  — нормальное течение жизни: сервис стартовал, запрос обработан.
//	Warn  — странно, но живём: повторная попытка, деградация, близость к лимиту.
//	Error — операция провалилась, надо чинить.
//
// САМАЯ ЧАСТАЯ БОЛЕЗНЬ: писать Error там, где ошибки нет.
// Юзер запросил несуществующий id — это не ошибка сервиса, это штатный 404.
// Если Error-ов сотни в час, на них перестают смотреть,
// и настоящая авария тонет в шуме.
//
// ЧТО НЕЛЬЗЯ ПИСАТЬ В ЛОГИ НИКОГДА:
// пароли, токены, ключи API, номера карт, персональные данные.
// Логи оседают в системах хранения с широким доступом,
// и утечка оттуда — это полноценная утечка данных.
// Правило: логируем ИДЕНТИФИКАТОРЫ (user_id), а не СОДЕРЖИМОЕ (email, password).
