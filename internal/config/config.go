// Package config отвечает за чтение и валидацию настроек приложения.
//
// Главный принцип: конфигурация читается ОДИН РАЗ при старте, полностью
// валидируется, и если чего-то не хватает — приложение падает сразу
// с внятным сообщением. Лучше не запуститься вообще, чем запуститься
// наполовину и упасть через час под нагрузкой.
//
// Источник настроек — переменные окружения (12-factor app).
// Почему не файл в репозитории: пароль от продовой базы не должен
// лежать в git. Никогда.
package config

import (
	"fmt"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

// Окружения, в которых может работать приложение.
// От этого зависит формат логов и поведение gin.
const (
	EnvLocal = "local" // локальная разработка: цветные логи, отладочный режим gin
	EnvDev   = "dev"   // dev-стенд
	EnvProd  = "prod"  // продакшен: JSON-логи, gin в release-режиме
)

// Config — корневая структура конфигурации.
// Вложенные структуры с envPrefix позволяют группировать переменные
// по смыслу: HTTP_PORT, DB_HOST, JWT_SECRET и т.д.
type Config struct {
	// Env — окружение: local | dev | prod.
	Env string `env:"APP_ENV" envDefault:"local"`

	HTTP HTTPConfig `envPrefix:"HTTP_"`
	DB   DBConfig   `envPrefix:"DB_"`
	JWT  JWTConfig  `envPrefix:"JWT_"`
	Log  LogConfig  `envPrefix:"LOG_"`
}

// HTTPConfig — настройки HTTP-сервера.
type HTTPConfig struct {
	// Port — порт, который слушает сервис.
	Port int `env:"PORT" envDefault:"8080"`

	// ReadTimeout — сколько ждём получения запроса целиком (включая тело).
	// Защита от медленных клиентов (Slowloris-атака).
	ReadTimeout time.Duration `env:"READ_TIMEOUT" envDefault:"10s"`

	// WriteTimeout — сколько отводим на запись ответа.
	WriteTimeout time.Duration `env:"WRITE_TIMEOUT" envDefault:"15s"`

	// IdleTimeout — сколько держим keep-alive соединение без запросов.
	IdleTimeout time.Duration `env:"IDLE_TIMEOUT" envDefault:"60s"`

	// RequestTimeout — потолок на обработку одного запроса нашей логикой.
	// Реализуется middleware через context.WithTimeout: по истечении
	// контекст отменяется, и запрос в Postgres тоже прерывается.
	//
	// ВАЖНО: должен быть МЕНЬШЕ WriteTimeout, иначе сервер оборвёт
	// соединение раньше, чем мы успеем отдать красивый ответ 504.
	RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" envDefault:"10s"`

	// ShutdownTimeout — сколько даём текущим запросам доработать
	// при остановке сервиса (graceful shutdown).
	ShutdownTimeout time.Duration `env:"SHUTDOWN_TIMEOUT" envDefault:"15s"`

	// CORSAllowedOrigins — список доменов фронтенда, которым разрешены
	// кросс-доменные запросы. Пусто = CORS выключен.
	// Значение "*" разрешает всем (только для разработки!).
	CORSAllowedOrigins []string `env:"CORS_ALLOWED_ORIGINS" envSeparator:","`
}

// DBConfig — настройки подключения к PostgreSQL.
type DBConfig struct {
	Host     string `env:"HOST" envDefault:"localhost"`
	Port     int    `env:"PORT" envDefault:"5432"`
	User     string `env:"USER,required"`
	Password string `env:"PASSWORD,required"`
	Name     string `env:"NAME,required"`

	// SSLMode — режим шифрования соединения с базой.
	// disable — только для локальной разработки.
	// В проде обязательно require или verify-full.
	SSLMode string `env:"SSLMODE" envDefault:"disable"`

	// MaxConns — максимальный размер пула соединений.
	//
	// ЛОВУШКА, на которой все обжигаются: у самого Postgres есть лимит
	// max_connections (по умолчанию ~100). Считать надо так:
	//
	//     количество_реплик_сервиса * MaxConns < max_connections - запас
	//
	// Три реплики по 50 соединений = 150 > 100, и база начнёт отказывать.
	MaxConns int32 `env:"MAX_CONNS" envDefault:"10"`

	// MinConns — сколько соединений держать открытыми всегда.
	// Убирает задержку "холодного старта" на первых запросах.
	MinConns int32 `env:"MIN_CONNS" envDefault:"2"`

	// MaxConnLifetime — максимальное время жизни соединения.
	// Зачем закрывать рабочие соединения: чтобы балансировщик перед базой
	// мог перераспределить нагрузку, и чтобы не накапливались утечки
	// на стороне сервера (временные таблицы, prepared statements).
	MaxConnLifetime time.Duration `env:"MAX_CONN_LIFETIME" envDefault:"1h"`

	// MaxConnIdleTime — через сколько закрывать простаивающее соединение.
	MaxConnIdleTime time.Duration `env:"MAX_CONN_IDLE_TIME" envDefault:"30m"`

	// ConnectTimeout — сколько ждём установления соединения при старте.
	ConnectTimeout time.Duration `env:"CONNECT_TIMEOUT" envDefault:"5s"`
}

// DSN собирает строку подключения к Postgres в формате URL.
//
// Пример: postgres://user:pass@localhost:5432/dbname?sslmode=disable
func (c DBConfig) DSN() string {
	return fmt.Sprintf(
		"postgres://%s:%s@%s:%d/%s?sslmode=%s",
		c.User, c.Password, c.Host, c.Port, c.Name, c.SSLMode,
	)
}

// JWTConfig — настройки выдачи и проверки токенов.
type JWTConfig struct {
	// Secret — ключ подписи HMAC-SHA256.
	// Утечка секрета = злоумышленник может выписать себе админский токен.
	// Минимальная длина проверяется в Validate().
	Secret string `env:"SECRET,required"`

	// TTL — срок жизни токена.
	// Короткий срок = меньше окно для злоупотребления украденным токеном,
	// но чаще приходится логиниться заново.
	TTL time.Duration `env:"TTL" envDefault:"24h"`

	// Issuer — кто выдал токен, попадает в claim "iss".
	Issuer string `env:"ISSUER" envDefault:"clients-api"`
}

// LogConfig — настройки логирования.
type LogConfig struct {
	// Level — минимальный уровень: debug | info | warn | error.
	Level string `env:"LEVEL" envDefault:"info"`

	// Format — формат вывода: json | text.
	// В проде всегда json — его умеют разбирать системы сбора логов.
	// Локально text — его удобнее читать глазами.
	Format string `env:"FORMAT" envDefault:"json"`

	// AddSource — добавлять ли в лог файл и строку, откуда он вызван.
	// Полезно при отладке, немного замедляет логирование.
	AddSource bool `env:"ADD_SOURCE" envDefault:"false"`
}

// Load читает конфигурацию из переменных окружения.
//
// Порядок работы:
//  1. Пытаемся подгрузить файл .env (для локальной разработки).
//     Если файла нет — это НЕ ошибка: в проде переменные приходят
//     из окружения контейнера, никакого .env там нет.
//  2. Разбираем переменные окружения в структуру.
//  3. Валидируем результат.
func Load() (*Config, error) {
	// godotenv не перезаписывает уже существующие переменные окружения —
	// то есть настоящее окружение всегда важнее файла .env.
	_ = godotenv.Load()

	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return nil, fmt.Errorf("parse environment variables: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("validate config: %w", err)
	}

	return &cfg, nil
}

// Validate проверяет осмысленность значений.
//
// Тег `env:"...,required"` ловит только отсутствие переменной.
// Здесь мы ловим то, что тегами не выразить: допустимые значения
// из списка, минимальную длину секрета, взаимосвязи между полями.
func (c *Config) Validate() error {
	switch c.Env {
	case EnvLocal, EnvDev, EnvProd:
		// ок
	default:
		return fmt.Errorf("APP_ENV must be one of: %s, %s, %s (got %q)",
			EnvLocal, EnvDev, EnvProd, c.Env)
	}

	if c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		return fmt.Errorf("HTTP_PORT must be in range 1..65535 (got %d)", c.HTTP.Port)
	}

	// Если потолок на обработку больше, чем таймаут записи ответа,
	// клиент получит оборванное соединение вместо аккуратного 504.
	if c.HTTP.RequestTimeout >= c.HTTP.WriteTimeout {
		return fmt.Errorf(
			"HTTP_REQUEST_TIMEOUT (%s) must be less than HTTP_WRITE_TIMEOUT (%s)",
			c.HTTP.RequestTimeout, c.HTTP.WriteTimeout,
		)
	}

	if c.DB.MaxConns < 1 {
		return fmt.Errorf("DB_MAX_CONNS must be at least 1 (got %d)", c.DB.MaxConns)
	}

	if c.DB.MinConns < 0 || c.DB.MinConns > c.DB.MaxConns {
		return fmt.Errorf("DB_MIN_CONNS must be in range 0..DB_MAX_CONNS (got %d, max %d)",
			c.DB.MinConns, c.DB.MaxConns)
	}

	// 32 байта — разумный минимум для HMAC-SHA256.
	// Короткий секрет реально подбирается перебором.
	const minSecretLen = 32
	if len(c.JWT.Secret) < minSecretLen {
		return fmt.Errorf("JWT_SECRET must be at least %d characters long (got %d)",
			minSecretLen, len(c.JWT.Secret))
	}

	if c.JWT.TTL <= 0 {
		return fmt.Errorf("JWT_TTL must be positive (got %s)", c.JWT.TTL)
	}

	switch c.Log.Level {
	case "debug", "info", "warn", "error":
		// ок
	default:
		return fmt.Errorf("LOG_LEVEL must be one of: debug, info, warn, error (got %q)", c.Log.Level)
	}

	switch c.Log.Format {
	case "json", "text":
		// ок
	default:
		return fmt.Errorf("LOG_FORMAT must be one of: json, text (got %q)", c.Log.Format)
	}

	// Защита от дурака: в проде нельзя разрешать CORS всем подряд.
	if c.Env == EnvProd {
		for _, origin := range c.HTTP.CORSAllowedOrigins {
			if origin == "*" {
				return fmt.Errorf(`HTTP_CORS_ALLOWED_ORIGINS must not contain "*" in prod environment`)
			}
		}
		if c.DB.SSLMode == "disable" {
			return fmt.Errorf(`DB_SSLMODE must not be "disable" in prod environment`)
		}
	}

	return nil
}

// IsLocal сообщает, работаем ли мы в режиме локальной разработки.
// Используется, чтобы включить отладочный режим gin и человекочитаемые логи.
func (c *Config) IsLocal() bool { return c.Env == EnvLocal }

// IsProd сообщает, работаем ли мы в продакшене.
func (c *Config) IsProd() bool { return c.Env == EnvProd }
