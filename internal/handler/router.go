package handler

import (
	"log/slog"

	"github.com/gin-gonic/gin"
	swaggerFiles "github.com/swaggo/files"
	ginSwagger "github.com/swaggo/gin-swagger"

	// Пустой импорт сгенерированной документации.
	// Пакет docs в своём init() регистрирует спецификацию OpenAPI,
	// которую потом отдаёт ginSwagger. Напрямую мы к нему не обращаемся —
	// отсюда подчёркивание.
	_ "github.com/vitikevich-landau/clients-api/docs"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
	"github.com/vitikevich-landau/clients-api/internal/config"
	"github.com/vitikevich-landau/clients-api/internal/middleware"
	"github.com/vitikevich-landau/clients-api/internal/response"
)

// RouterDeps — всё, что нужно роутеру для сборки.
//
// # Почему структура, а не десять аргументов функции
//
// Функция NewRouter(cfg, log, tokens, auth, clients, suggestions, health)
// читается плохо, и при вызове легко перепутать местами два аргумента
// одного типа — компилятор этого не заметит. У структуры каждое поле
// названо, порядок не важен, а добавление новой зависимости не ломает
// все существующие вызовы.
type RouterDeps struct {
	Config *config.Config
	Logger *slog.Logger

	// Tokens нужен middleware.Auth для разбора JWT.
	Tokens middleware.TokenParser

	Auth        *AuthHandler
	Clients     *ClientHandler
	Suggestions *SuggestionHandler
	Health      *HealthHandler
}

// NewRouter собирает движок gin: middleware, группы, роуты.
//
// # Как читать эту функцию
//
// Сверху вниз она повторяет путь запроса: сначала глобальные middleware
// (внешние слои луковицы), потом открытые роуты, потом защищённые,
// потом админские. Чем ниже по файлу — тем строже требования к вызывающему.
func NewRouter(deps RouterDeps) *gin.Engine {
	cfg := deps.Config

	// --- Режим работы gin ---
	//
	// В release-режиме gin перестаёт печатать при старте список роутов
	// и предупреждения. Не косметика: отладочный вывод идёт мимо нашего
	// логгера и ломает построчный разбор JSON-логов.
	if cfg.IsLocal() {
		gin.SetMode(gin.DebugMode)
	} else {
		gin.SetMode(gin.ReleaseMode)
	}

	// Имена полей в ошибках валидации должны совпадать с именами
	// в JSON, а не с именами полей Go. Настраивается один раз.
	SetupValidator()

	// gin.New(), а НЕ gin.Default().
	//
	// gin.Default() навешивает свои Logger и Recovery, которые пишут
	// человекочитаемый текст в stderr. В проде нужен JSON и наш формат,
	// поэтому берём пустой движок и ставим своё.
	router := gin.New()

	// --- Настройки движка ---

	// Доверяем заголовкам X-Forwarded-For только от перечисленных прокси.
	//
	// nil означает «никому не доверять»: c.ClientIP() будет возвращать
	// адрес непосредственного соединения. Это правильное значение
	// по умолчанию — иначе любой клиент подделает свой IP в логах
	// и обойдёт ограничения по адресу, просто прислав заголовок.
	//
	// Если сервис стоит за nginx или балансировщиком, сюда надо вписать
	// его подсеть — тогда в логах будет настоящий адрес пользователя.
	_ = router.SetTrustedProxies(nil)

	// Не перенаправлять /clients/ на /clients автоматически.
	// Неявные редиректы путают клиентов API и ломают POST-запросы
	// (при 301 браузер меняет метод на GET).
	router.RedirectTrailingSlash = false

	// ========================================================================
	//  ГЛОБАЛЬНЫЕ MIDDLEWARE. ПОРЯДОК ИМЕЕТ ЗНАЧЕНИЕ.
	//
	//  Каждый следующий Use() — слой ВНУТРИ предыдущего:
	//  первый зарегистрированный оборачивает всех остальных.
	// ========================================================================

	// 1. Recovery — самый внешний. Ловит панику откуда угодно, включая
	//    остальные middleware. Если поставить его вторым, паника в первом
	//    положит процесс.
	router.Use(middleware.Recovery(deps.Logger))

	// 2. RequestID — до логгера, потому что логгер вшивает
	//    идентификатор в каждую свою запись.
	router.Use(middleware.RequestID())

	// 3. Logger — снаружи Timeout, чтобы записать в лог и сам факт
	//    таймаута тоже.
	router.Use(middleware.Logger(deps.Logger))

	// 4. Timeout — ограничивает время работы всего, что внутри.
	router.Use(middleware.Timeout(cfg.HTTP.RequestTimeout))

	// 5. CORS — только если настроен список доменов.
	if len(cfg.HTTP.CORSAllowedOrigins) > 0 {
		router.Use(middleware.CORS(cfg.HTTP.CORSAllowedOrigins))
	}

	// --- Ответы на неизвестные маршруты ---
	//
	// По умолчанию gin отдаёт "404 page not found" простым текстом.
	// Клиент API ждёт JSON и наш формат ошибки — иначе его разбор
	// ошибок споткнётся именно там, где он и так уже растерян.
	router.NoRoute(func(c *gin.Context) {
		response.Error(c, apperr.NotFound("endpoint"))
	})

	// 405 Method Not Allowed: путь есть, но метод не тот.
	// Требует явного включения — по умолчанию gin отдаёт на это 404.
	// Разница полезна: 405 сразу подсказывает, что путь угадан верно.
	router.HandleMethodNotAllowed = true
	router.NoMethod(func(c *gin.Context) {
		response.Error(c, apperr.MethodNotAllowed("method not allowed for this endpoint"))
	})

	// ========================================================================
	//  СЛУЖЕБНЫЕ РОУТЫ — без авторизации и вне версионирования
	// ========================================================================
	//
	// Оркестратор (k8s, docker-compose) обязан достучаться до них
	// без всяких токенов. Секретов они не раскрывают.
	router.GET("/healthz", deps.Health.Healthz)
	router.GET("/readyz", deps.Health.Readyz)

	// Swagger UI — только вне продакшена.
	//
	// В проде интерактивная документация со списком всех ручек и схем —
	// готовая карта для атакующего. Если она нужна и там, её закрывают
	// авторизацией или выносят во внутреннюю сеть.
	if !cfg.IsProd() {
		router.GET("/swagger/*any", ginSwagger.WrapHandler(swaggerFiles.Handler))
	}

	// ========================================================================
	//  API v1
	// ========================================================================
	//
	// Версия в пути — не бюрократия. Когда через год понадобится
	// изменить формат ответа, старые клиенты (мобильное приложение,
	// которое пользователи не обновили) продолжат ходить в /v1,
	// а новые — в /v2. Без этого поменять контракт нельзя вообще.
	v1 := router.Group("/api/v1")

	// --- Открытые роуты: вход и регистрация ---
	//
	// Единственное место без авторизации: попасть внутрь как-то надо.
	auth := v1.Group("/auth")
	{
		auth.POST("/register", deps.Auth.Register)
		auth.POST("/login", deps.Auth.Login)
	}

	// --- Защищённые роуты: нужен любой валидный токен ---
	//
	// Ключевой момент: middleware.Auth вешается на ГРУППУ.
	// Любой роут, добавленный в неё дальше, автоматически защищён.
	// Забыть проверку при добавлении новой ручки невозможно.
	authorized := v1.Group("")
	authorized.Use(middleware.Auth(deps.Tokens))
	{
		authorized.GET("/auth/me", deps.Auth.Me)

		// Справочник клиентов: смотреть может любой пользователь.
		authorized.GET("/clients", deps.Clients.List)
		authorized.GET("/clients/:id", deps.Clients.Get)

		// Предложить правку — то, ради чего всё затевалось.
		// Обычный пользователь не меняет карточку, он предлагает изменение.
		authorized.POST("/clients/:id/suggestions", deps.Suggestions.Create)

		// Свои предложения и их статусы.
		//
		// ВНИМАНИЕ на порядок: /suggestions/my объявлен ДО /suggestions/:id.
		// Роутер gin построен на префиксном дереве, и статический сегмент
		// "my" всегда выигрывает у параметра ":id" независимо от порядка
		// объявления — но читателю кода порядок подсказывает, что здесь
		// есть о чём подумать. В роутерах попроще (gorilla/mux) порядок
		// решал бы всё, и обратная последовательность ловила бы "my"
		// как идентификатор.
		authorized.GET("/suggestions/my", deps.Suggestions.ListMy)
		authorized.GET("/suggestions/:id", deps.Suggestions.Get)
	}

	// --- Админские роуты: нужен токен с ролью admin ---
	//
	// Две проверки подряд: сначала «кто ты» (Auth), затем «можно ли тебе»
	// (RequireAdmin). Это разные вопросы и разные middleware.
	admin := v1.Group("/admin")
	admin.Use(middleware.Auth(deps.Tokens), middleware.RequireAdmin())
	{
		// Полный доступ к справочнику.
		admin.POST("/clients", deps.Clients.Create)
		admin.PUT("/clients/:id", deps.Clients.Update)
		admin.DELETE("/clients/:id", deps.Clients.Delete)

		// Модерация предложений.
		admin.GET("/suggestions", deps.Suggestions.ListAll)
		admin.POST("/suggestions/:id/approve", deps.Suggestions.Approve)
		admin.POST("/suggestions/:id/reject", deps.Suggestions.Reject)
	}

	return router
}
