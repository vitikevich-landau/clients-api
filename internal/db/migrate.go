package db

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"

	// Регистрирует драйвер "pgx" в database/sql.
	// Импорт с пустым идентификатором (_) означает: пакет нужен только
	// ради его init(), напрямую мы к нему не обращаемся.
	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/pressly/goose/v3"

	"github.com/vitikevich-landau/clients-api/internal/config"
	"github.com/vitikevich-landau/clients-api/migrations"
)

// advisoryLockID — произвольное, но ПОСТОЯННОЕ число, под которым
// берётся консультативная блокировка Postgres на время миграций.
//
// Зачем: при выкатке новой версии сервиса реплики стартуют одновременно,
// и каждая пытается накатить миграции. Без блокировки они полезут менять
// схему параллельно и подерутся — в лучшем случае одна упадёт,
// в худшем схема останется в промежуточном состоянии.
//
// pg_advisory_lock заставляет реплики выстроиться в очередь: первая
// накатывает, остальные ждут, потом видят, что всё уже применено,
// и просто идут дальше.
const advisoryLockID int64 = 4815162342

// Migrate накатывает все непримененные миграции.
//
// # Про запуск миграций из приложения
//
// Здесь это сделано для простоты запуска: `make up` — и всё работает.
// В больших системах миграции обычно выносят в ОТДЕЛЬНЫЙ шаг деплоя
// (init-контейнер в k8s, отдельная job в CI), потому что:
//   - миграция может идти дольше, чем health-check ждёт старта пода;
//   - откатить деплой проще, когда схема и код разъезжаются осознанно;
//   - приложению в проде обычно не дают прав на ALTER TABLE.
//
// Компромисс осознанный, и он описан в README.
func Migrate(ctx context.Context, cfg config.DBConfig, log *slog.Logger) error {
	// goose работает через database/sql, а не через нативный pgx.
	// Это отдельное короткоживущее подключение: открыли, накатили, закрыли.
	// Основной пул приложения тут ни при чём.
	sqlDB, err := sql.Open("pgx", cfg.DSN())
	if err != nil {
		return fmt.Errorf("open database for migrations: %w", err)
	}
	defer func() {
		if err := sqlDB.Close(); err != nil {
			log.Warn("failed to close migration database connection",
				slog.String("error", err.Error()))
		}
	}()

	// Блокировку обязательно берём на ОДНОМ соединении: pg_advisory_lock
	// живёт в рамках сессии, а database/sql по умолчанию раздаёт запросы
	// по разным соединениям пула. Возьмём блокировку на одном, а отпустим
	// с другого — и она зависнет до конца жизни процесса.
	conn, err := sqlDB.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire connection for advisory lock: %w", err)
	}
	defer func() { _ = conn.Close() }()

	log.Debug("acquiring migration advisory lock")
	if _, err := conn.ExecContext(ctx, "SELECT pg_advisory_lock($1)", advisoryLockID); err != nil {
		return fmt.Errorf("acquire advisory lock: %w", err)
	}
	defer func() {
		// Отпускаем блокировку в любом случае — даже если миграция упала.
		// context.WithoutCancel: если ctx уже отменён, обычный запрос
		// не отправится и блокировка провисит до закрытия соединения.
		if _, err := conn.ExecContext(
			context.WithoutCancel(ctx),
			"SELECT pg_advisory_unlock($1)", advisoryLockID,
		); err != nil {
			log.Warn("failed to release migration advisory lock",
				slog.String("error", err.Error()))
		}
	}()

	// Источник миграций — вшитая в бинарник файловая система.
	goose.SetBaseFS(migrations.FS)

	// Перенаправляем вывод goose в наш slog, чтобы миграции не ломали
	// формат логов (иначе goose пишет обычным log.Printf в stderr).
	goose.SetLogger(&gooseLogger{log: log})

	if err := goose.SetDialect("postgres"); err != nil {
		return fmt.Errorf("set goose dialect: %w", err)
	}

	// "." — корень встроенной FS, там и лежат .sql-файлы.
	if err := goose.UpContext(ctx, sqlDB, "."); err != nil {
		return fmt.Errorf("apply migrations: %w", err)
	}

	version, err := goose.GetDBVersionContext(ctx, sqlDB)
	if err != nil {
		return fmt.Errorf("read schema version: %w", err)
	}

	log.Info("database migrations applied", slog.Int64("schema_version", version))
	return nil
}

// gooseLogger — адаптер между интерфейсом логгера goose и slog.
type gooseLogger struct {
	log *slog.Logger
}

// Printf — обычные сообщения goose ("OK 00001_users.sql").
// Уровень Debug, чтобы не засорять прод-логи при каждом старте.
func (l *gooseLogger) Printf(format string, v ...any) {
	l.log.Debug("goose: " + trimNewline(fmt.Sprintf(format, v...)))
}

// Fatalf вызывается goose при фатальной ошибке.
//
// ВАЖНО: НЕ вызываем os.Exit, хотя оригинальный логгер goose именно это
// и делает. Выход из процесса посреди библиотечного вызова пропускает
// все defer — блокировка не отпустится, соединения не закроются.
// Пишем Error и возвращаем управление: ошибку goose всё равно вернёт
// из UpContext, и мы обработаем её нормальным способом.
func (l *gooseLogger) Fatalf(format string, v ...any) {
	l.log.Error("goose: " + trimNewline(fmt.Sprintf(format, v...)))
}

// trimNewline убирает завершающий перевод строки: goose добавляет его сам,
// а в структурном логе он превращается в мусор внутри JSON-поля.
func trimNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
