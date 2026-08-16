// Package db отвечает за подключение к PostgreSQL, пул соединений
// и управление транзакциями.
//
// Драйвер — pgx/v5 в НАТИВНОМ режиме (не через database/sql).
// Нативный режим быстрее и умеет специфичные типы Postgres:
// массивы, JSONB, диапазоны, композитные типы.
package db

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/vitikevich-landau/clients-api/internal/config"
)

// Pool — пул соединений с базой.
//
// # Зачем нужен пул
//
// Открывать новое TCP-соединение с Postgres на каждый HTTP-запрос — дорого:
// это рукопожатие TCP, TLS, аутентификация, инициализация сессии — десятки
// миллисекунд. Пул держит набор уже открытых соединений и выдаёт их
// во временное пользование, а потом забирает обратно.
//
// pgxpool потокобезопасен: один экземпляр создаётся при старте приложения
// и используется всеми горутинами одновременно. Создавать пул на запрос —
// грубая ошибка.
type Pool = pgxpool.Pool

// NewPool создаёт и проверяет пул соединений.
//
// Важный момент: pgxpool.New НЕ подключается к базе сразу — соединения
// создаются лениво, при первом запросе. Поэтому мы явно делаем Ping:
// хотим узнать о недоступной базе на старте, а не на первом запросе юзера.
func NewPool(ctx context.Context, cfg config.DBConfig, log *slog.Logger) (*Pool, error) {
	// ParseConfig разбирает строку подключения (DSN) в структуру настроек,
	// которую дальше можно донастроить программно.
	poolCfg, err := pgxpool.ParseConfig(cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("parse database dsn: %w", err)
	}

	// --- Настройки пула ---

	// Верхняя граница числа соединений.
	// НАПОМИНАНИЕ: реплики_сервиса * MaxConns должно быть меньше,
	// чем max_connections у самого Postgres (по умолчанию ~100).
	poolCfg.MaxConns = cfg.MaxConns

	// Нижняя граница: столько соединений держим открытыми всегда,
	// чтобы первые запросы после простоя не ждали подключения.
	poolCfg.MinConns = cfg.MinConns

	// Принудительно закрываем соединение по достижении возраста,
	// даже если оно рабочее. Зачем: чтобы балансировщик перед базой
	// (pgbouncer, HAProxy) мог перераспределить нагрузку после
	// добавления новой реплики, и чтобы не копились серверные утечки.
	poolCfg.MaxConnLifetime = cfg.MaxConnLifetime

	// Закрываем соединение, которое просто так лежит без дела.
	poolCfg.MaxConnIdleTime = cfg.MaxConnIdleTime

	// Таймаут на установление ОДНОГО соединения.
	poolCfg.ConnConfig.ConnectTimeout = cfg.ConnectTimeout

	// HealthCheckPeriod — как часто пул проверяет свои простаивающие
	// соединения и подчищает мёртвые.
	poolCfg.HealthCheckPeriod = time.Minute

	pool, err := pgxpool.NewWithConfig(ctx, poolCfg)
	if err != nil {
		return nil, fmt.Errorf("create connection pool: %w", err)
	}

	// Проверяем связь с базой с отдельным таймаутом.
	// Без этого приложение стартует "успешно" и начнёт отдавать 500
	// на каждый запрос — гораздо хуже, чем честно не подняться.
	pingCtx, cancel := context.WithTimeout(ctx, cfg.ConnectTimeout)
	defer cancel()

	if err := pool.Ping(pingCtx); err != nil {
		pool.Close() // не оставляем за собой висящий пул
		return nil, fmt.Errorf("ping database: %w", err)
	}

	log.Info("database connection pool established",
		slog.String("host", cfg.Host),
		slog.Int("port", cfg.Port),
		slog.String("database", cfg.Name),
		slog.Int("max_conns", int(cfg.MaxConns)),
		slog.Int("min_conns", int(cfg.MinConns)),
	)
	// Пароль в лог не пишем. Никогда. Даже в debug.

	return pool, nil
}

// PoolStats собирает метрики пула — их удобно отдавать в /readyz
// или скармливать Prometheus.
//
// На что смотреть в проде:
//   - AcquiredConns стабильно упирается в MaxConns → пул мал либо
//     запросы слишком долгие, скоро начнутся таймауты;
//   - EmptyAcquireCount растёт → запросы уже ЖДУТ свободное соединение.
func PoolStats(p *Pool) map[string]any {
	s := p.Stat()
	return map[string]any{
		"total_conns":         s.TotalConns(),        // всего соединений в пуле
		"acquired_conns":      s.AcquiredConns(),     // сейчас занято
		"idle_conns":          s.IdleConns(),         // сейчас свободно
		"max_conns":           s.MaxConns(),          // потолок
		"empty_acquire_count": s.EmptyAcquireCount(), // сколько раз пришлось ждать
	}
}
