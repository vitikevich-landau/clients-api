// Команда api — HTTP-сервис учёта клиентов с модерацией правок.
//
// # Что делает main
//
// Ровно одно: СОБИРАЕТ приложение из готовых частей и запускает.
// Ни бизнес-логики, ни SQL, ни разбора JSON здесь нет и быть не должно.
//
// Такой стиль называют «композиционным корнем» (composition root):
// единственное место, где конкретные реализации встречаются друг с другом.
// Всё остальное приложение работает с интерфейсами и не знает,
// что там на самом деле — Postgres или заглушка из теста.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/vitikevich-landau/clients-api/internal/config"
	"github.com/vitikevich-landau/clients-api/internal/db"
	"github.com/vitikevich-landau/clients-api/internal/handler"
	"github.com/vitikevich-landau/clients-api/internal/logger"
	"github.com/vitikevich-landau/clients-api/internal/repository"
	"github.com/vitikevich-landau/clients-api/internal/service"
)

// version подставляется при сборке через флаг компоновщика:
//
//	go build -ldflags "-X main.version=$(git rev-parse --short HEAD)"
//
// Зачем: получив жалобу, надо точно знать, КАКАЯ версия кода её выдала.
// Версия отдаётся в /healthz и пишется в лог при старте.
var version = "dev"

//	@title			Clients API
//	@version		1.0
//	@description	Учебный продакшн-подобный сервис учёта клиентов.
//	@description
//	@description	Ключевая механика: обычный пользователь не редактирует карточку
//	@description	клиента напрямую — он ПРЕДЛАГАЕТ правку, которая применяется
//	@description	только после одобрения администратором.
//	@description
//	@description	Демо-учётки (заводятся миграцией 00004):
//	@description	admin@example.com / admin12345 — роль admin
//	@description	user@example.com / user12345 — роль user

//	@host		localhost:8080
//	@BasePath	/api/v1

//	@securityDefinitions.apikey	BearerAuth
//	@in							header
//	@name						Authorization
//	@description				Введите: Bearer <ваш JWT>

func main() {
	// Вся настоящая работа — в run(), которая возвращает ошибку.
	//
	// Зачем так: os.Exit НЕ выполняет отложенные функции (defer).
	// Если бы мы вызывали его прямо из run, за собой не закрылись бы
	// ни пул соединений, ни файлы. Здесь os.Exit вызывается ровно один раз,
	// когда все defer уже отработали.
	if err := run(); err != nil {
		// Логгер на этот момент может быть ещё не настроен
		// (например, упали на чтении конфига) — пишем в stderr напрямую.
		fmt.Fprintf(os.Stderr, "fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// ========================================================================
	//  1. Конфигурация
	// ========================================================================
	//
	// Читается первой: без неё непонятно даже, как настраивать логгер.
	// Любая проблема здесь — немедленная остановка. Лучше не запуститься,
	// чем работать наполовину.
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// ========================================================================
	//  2. Логгер
	// ========================================================================
	log := logger.New(cfg.Log, os.Stdout)

	// slog.Default() используется как запасной вариант в logger.FromContext,
	// когда логгера нет в контексте (фоновые задачи, тесты).
	// Подменяем его на наш, чтобы формат был единым везде.
	slog.SetDefault(log)

	log.Info("starting service",
		slog.String("version", version),
		slog.String("env", cfg.Env),
		slog.Int("port", cfg.HTTP.Port),
	)

	// Контекст, живущий столько же, сколько приложение.
	// Отменяется при получении сигнала остановки — см. шаг 7.
	ctx, stop := signal.NotifyContext(context.Background(),
		syscall.SIGINT,  // Ctrl+C в терминале
		syscall.SIGTERM, // docker stop, kubectl delete pod, systemctl stop
	)
	defer stop()

	// ========================================================================
	//  3. Миграции
	// ========================================================================
	//
	// ДО создания основного пула: смысла подключаться к базе со старой
	// схемой нет, приложение всё равно не сможет работать.
	if err := db.Migrate(ctx, cfg.DB, log); err != nil {
		return fmt.Errorf("run migrations: %w", err)
	}

	// ========================================================================
	//  4. Пул соединений с базой
	// ========================================================================
	pool, err := db.NewPool(ctx, cfg.DB, log)
	if err != nil {
		return fmt.Errorf("connect to database: %w", err)
	}
	// Закрываем пул при выходе. Close ждёт возврата всех выданных
	// соединений — то есть завершения текущих запросов.
	defer func() {
		log.Info("closing database connection pool")
		pool.Close()
	}()

	// ========================================================================
	//  5. Сборка слоёв: repository → service → handler
	// ========================================================================
	//
	// Зависимости идут строго снизу вверх. Каждый слой получает то,
	// что ему нужно, через конструктор — это и есть внедрение
	// зависимостей (dependency injection). Никаких глобальных переменных
	// и никаких библиотек DI: на таком размере они только мешают.

	txManager := db.NewTxManager(pool)

	// Репозитории — знают SQL и больше ничего.
	userRepo := repository.NewUserRepository()
	clientRepo := repository.NewClientRepository()
	suggestionRepo := repository.NewSuggestionRepository()

	// Менеджер токенов — выпускает и проверяет JWT.
	tokenManager := service.NewTokenManager(cfg.JWT)

	// Сервисы — знают бизнес-правила и ничего не знают про HTTP.
	authService := service.NewAuthService(userRepo, tokenManager, txManager)
	clientService := service.NewClientService(clientRepo, txManager)
	suggestionService := service.NewSuggestionService(suggestionRepo, clientRepo, txManager)

	// Хендлеры — знают HTTP и ничего не знают про SQL.
	authHandler := handler.NewAuthHandler(authService)
	clientHandler := handler.NewClientHandler(clientService)
	suggestionHandler := handler.NewSuggestionHandler(suggestionService)
	healthHandler := handler.NewHealthHandler(pool, version)

	router := handler.NewRouter(handler.RouterDeps{
		Config:      cfg,
		Logger:      log,
		Tokens:      tokenManager,
		Auth:        authHandler,
		Clients:     clientHandler,
		Suggestions: suggestionHandler,
		Health:      healthHandler,
	})

	// ========================================================================
	//  6. HTTP-сервер
	// ========================================================================
	//
	// Собираем http.Server вручную, а не через router.Run().
	//
	// Причины две:
	//   - router.Run() не даёт выставить таймауты, а сервер без них
	//     кладётся медленными клиентами (Slowloris);
	//   - router.Run() не даёт возможности корректной остановки.
	server := &http.Server{
		Addr:    fmt.Sprintf(":%d", cfg.HTTP.Port),
		Handler: router,

		// Сколько ждём получения запроса целиком, включая тело.
		ReadTimeout: cfg.HTTP.ReadTimeout,

		// Сколько отводим на запись ответа.
		WriteTimeout: cfg.HTTP.WriteTimeout,

		// Сколько держим keep-alive соединение без новых запросов.
		IdleTimeout: cfg.HTTP.IdleTimeout,

		// Отдельный потолок на чтение ЗАГОЛОВКОВ. Именно этот таймаут
		// защищает от Slowloris: атакующий открывает тысячи соединений
		// и шлёт заголовки по байту в минуту, занимая все воркеры.
		ReadHeaderTimeout: cfg.HTTP.ReadTimeout,
	}

	// Запускаем сервер в отдельной горутине, чтобы главная могла
	// ждать сигнала остановки.
	//
	// Канал буферизованный (ёмкость 1) НАМЕРЕННО: если никто не станет
	// читать из него (потому что мы уже уходим по сигналу), горутина
	// всё равно завершится и не утечёт.
	serverErrors := make(chan error, 1)

	go func() {
		log.Info("http server started", slog.String("addr", server.Addr))

		// ListenAndServe блокируется до остановки сервера.
		// При корректной остановке возвращает http.ErrServerClosed —
		// это НЕ ошибка, а штатное завершение.
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			serverErrors <- fmt.Errorf("http server failed: %w", err)
			return
		}
		serverErrors <- nil
	}()

	// ========================================================================
	//  7. Ожидание остановки и GRACEFUL SHUTDOWN
	// ========================================================================
	//
	// Ждём, что случится раньше: сервер упадёт сам или придёт сигнал.
	select {
	case err := <-serverErrors:
		// Сервер не смог подняться (обычно порт занят) или упал.
		if err != nil {
			return err
		}

	case <-ctx.Done():
		// Пришёл SIGINT или SIGTERM.
		//
		// # Почему нельзя просто умереть
		//
		// В этот момент могут обрабатываться запросы: кто-то ждёт ответа,
		// идёт незакоммиченная транзакция, пишется файл. Мгновенная смерть
		// процесса означает оборванные соединения и, возможно, потерянные
		// данные. При каждом деплое.
		//
		// Правильная последовательность:
		//   1) перестать принимать НОВЫЕ соединения;
		//   2) дать текущим запросам доработать;
		//   3) закрыть пул соединений с базой (через defer выше);
		//   4) выйти.
		log.Info("shutdown signal received, stopping gracefully",
			slog.Duration("timeout", cfg.HTTP.ShutdownTimeout))

		// stop() возвращает обработку сигналов системе по умолчанию.
		// Практический смысл: второе нажатие Ctrl+C убьёт процесс
		// немедленно, не дожидаясь окончания graceful shutdown.
		// Иначе нетерпеливый разработчик не смог бы прервать зависшую остановку.
		stop()

		// Отдельный контекст с таймаутом на саму остановку.
		//
		// Он НЕ наследуется от ctx — тот уже отменён сигналом,
		// и Shutdown с ним завершился бы мгновенно, оборвав всё,
		// что мы как раз пытаемся аккуратно доработать.
		shutdownCtx, cancel := context.WithTimeout(
			context.Background(), cfg.HTTP.ShutdownTimeout)
		defer cancel()

		// Shutdown закрывает слушающие сокеты и ждёт завершения
		// активных запросов. Новые соединения не принимаются.
		if err := server.Shutdown(shutdownCtx); err != nil {
			// Не уложились в отведённое время: какие-то запросы
			// всё ещё выполняются. Закрываем принудительно —
			// оставшиеся соединения будут оборваны.
			log.Error("graceful shutdown timed out, forcing close",
				slog.String("error", err.Error()))

			if closeErr := server.Close(); closeErr != nil {
				return fmt.Errorf("force close server: %w", closeErr)
			}
		}
	}

	log.Info("service stopped")
	return nil
}
