package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// Querier — минимальный интерфейс "того, что умеет выполнять запросы".
//
// # Зачем он нужен
//
// Его реализуют И пул (*pgxpool.Pool), И транзакция (pgx.Tx).
// Благодаря этому методы репозитория принимают Querier и работают
// одинаково — что внутри транзакции, что вне её:
//
//	// обычный запрос, каждый сам по себе
//	repo.Update(ctx, pool, client)
//
//	// тот же метод, но внутри транзакции
//	txm.WithTx(ctx, func(ctx context.Context, tx db.Querier) error {
//	    return repo.Update(ctx, tx, client)
//	})
//
// Не нужно дублировать методы в двух вариантах и не нужно прятать
// транзакцию в контексте (популярный, но неявный и потому спорный приём).
type Querier interface {
	// Query — запрос, возвращающий много строк.
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)

	// QueryRow — запрос, возвращающий ровно одну строку.
	QueryRow(ctx context.Context, sql string, args ...any) pgx.Row

	// Exec — запрос без результата (INSERT/UPDATE/DELETE),
	// возвращает CommandTag с числом затронутых строк.
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Проверка на этапе компиляции, что пул действительно реализует Querier.
// Если pgx поменяет сигнатуры в новой мажорной версии — узнаем при сборке,
// а не в рантайме на проде.
var _ Querier = (*Pool)(nil)

// TxManager запускает функции внутри транзакции.
type TxManager struct {
	pool *Pool
}

// NewTxManager создаёт менеджер транзакций поверх пула.
func NewTxManager(pool *Pool) *TxManager {
	return &TxManager{pool: pool}
}

// DB возвращает пул для запросов ВНЕ транзакции.
//
// Каждый одиночный запрос к Postgres и так выполняется в неявной
// транзакции — оборачивать одиночный SELECT в BEGIN/COMMIT бессмысленно
// и только тратит два лишних round-trip до базы.
func (m *TxManager) DB() Querier { return m.pool }

// WithTx выполняет fn внутри транзакции.
//
// # Контракт
//
//	fn вернула nil    → COMMIT
//	fn вернула ошибку → ROLLBACK, ошибка пробрасывается наверх как есть
//	fn запаниковала   → ROLLBACK, паника пробрасывается дальше
//
// # Зачем это в нашем проекте
//
// Одобрение правки (approve) — это ДВА изменения, которые обязаны
// произойти вместе:
//
//  1. применить payload к строке в clients
//  2. перевести предложение в статус approved
//
// Без транзакции возможен разрыв: упали между шагами — и получили
// "предложение одобрено, но данные клиента не изменились".
// Такие рассинхроны потом ищут неделями.
//
// # Про вложенность
//
// Вызывать WithTx внутри WithTx нельзя — получите вторую независимую
// транзакцию на другом соединении и, скорее всего, дедлок на самих себе.
// Транзакцией управляет ТОЛЬКО service-слой, repository про неё не знает.
func (m *TxManager) WithTx(ctx context.Context, fn func(ctx context.Context, tx Querier) error) error {
	tx, err := m.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}

	// ВАЖНАЯ ДЕТАЛЬ ПРО КОНТЕКСТ ПРИ ОТКАТЕ.
	//
	// Если запрос отменили (юзер закрыл вкладку) или сработал таймаут,
	// ctx уже мёртв — и ROLLBACK по нему просто не отправится.
	// Транзакция повиснет на сервере и будет держать блокировки,
	// пока Postgres сам её не прибьёт.
	//
	// context.WithoutCancel (Go 1.21+) даёт контекст с теми же значениями,
	// но без наследования отмены. Ровно то, что нужно для завершающих
	// операций вроде отката.
	rollbackCtx := context.WithoutCancel(ctx)

	// Страховка от паники: без неё транзакция осталась бы незакрытой,
	// а соединение — навсегда занятым в пуле. Несколько таких паник
	// исчерпают пул и положат весь сервис.
	defer func() {
		if p := recover(); p != nil {
			_ = tx.Rollback(rollbackCtx)
			panic(p) // панику не проглатываем — её поймает Recovery-middleware
		}
	}()

	if err := fn(ctx, tx); err != nil {
		if rbErr := tx.Rollback(rollbackCtx); rbErr != nil && !errors.Is(rbErr, pgx.ErrTxClosed) {
			// Возвращаем обе ошибки: исходную (она важнее — из-за неё
			// всё и откатывается) и ошибку отката (она диагностическая).
			return errors.Join(err, fmt.Errorf("rollback transaction: %w", rbErr))
		}
		return err // ошибку бизнес-логики отдаём как есть, чтобы не потерять её тип
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}

	return nil
}

// ---------------------------------------------------------------------------
// Разбор ошибок PostgreSQL
// ---------------------------------------------------------------------------

// Коды ошибок PostgreSQL (SQLSTATE).
// Полный список: https://www.postgresql.org/docs/current/errcodes-appendix.html
const (
	pgCodeUniqueViolation     = "23505" // нарушено ограничение UNIQUE
	pgCodeForeignKeyViolation = "23503" // нарушен внешний ключ
)

// IsUniqueViolation сообщает, что запрос упал из-за нарушения уникальности.
//
// Зачем разбирать код ошибки, а не проверять существование заранее:
// проверка "SELECT ... затем INSERT" — это гонка. Между проверкой и
// вставкой другой запрос успеет вставить ту же запись.
// Единственный надёжный способ — попытаться вставить и разобрать отказ базы.
//
// Параметр constraint позволяет отличить, КАКОЕ именно ограничение нарушено,
// когда их на таблице несколько. Пустая строка — любое.
func IsUniqueViolation(err error, constraint string) bool {
	var pgErr *pgconn.PgError
	if !errors.As(err, &pgErr) {
		return false
	}
	if pgErr.Code != pgCodeUniqueViolation {
		return false
	}
	return constraint == "" || pgErr.ConstraintName == constraint
}

// IsForeignKeyViolation сообщает, что нарушен внешний ключ —
// например, пытаемся создать предложение для несуществующего клиента.
func IsForeignKeyViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgCodeForeignKeyViolation
}
