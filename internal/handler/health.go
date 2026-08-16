package handler

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
)

// Pinger — то, что нужно этому хендлеру от базы: уметь отвечать на пинг.
// Реализуется пулом pgxpool.
type Pinger interface {
	Ping(ctx context.Context) error
}

// HealthHandler — служебные ручки проверки состояния сервиса.
type HealthHandler struct {
	db      Pinger
	version string
}

// NewHealthHandler создаёт хендлер проверок.
func NewHealthHandler(db Pinger, version string) *HealthHandler {
	return &HealthHandler{db: db, version: version}
}

// healthTimeout — потолок на проверку базы.
//
// Держим коротким намеренно: проверка готовности должна отвечать быстро.
// Если база тормозит настолько, что не успевает за секунду ответить
// на пустяковый запрос, сервис и правда не готов принимать нагрузку.
const healthTimeout = 1 * time.Second

// Healthz — проба ЖИВУЧЕСТИ (liveness probe).
//
// # Отвечает на вопрос: «процесс жив или его надо перезапустить?»
//
// Внешние зависимости здесь НЕ проверяются, и это принципиально.
//
// Представь обратное: база на минуту недоступна, liveness-проба падает,
// оркестратор решает, что приложение сломалось, и перезапускает ВСЕ поды.
// База от этого не починится, а сервис теперь ещё и не может подняться —
// потому что при старте ждёт базу. Получился каскадный отказ, который
// мы устроили себе сами.
//
// Правило: liveness падает только тогда, когда помочь может ИМЕННО
// перезапуск процесса — дедлок, исчерпание горутин, разрушенное состояние.
//
//	@Summary		Проба живучести
//	@Description	Всегда 200, если процесс отвечает. Внешние зависимости не проверяет.
//	@Tags			health
//	@Produce		json
//	@Success		200	{object}	map[string]interface{}
//	@Router			/healthz [get]
func (h *HealthHandler) Healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"version": h.version,
	})
}

// Readyz — проба ГОТОВНОСТИ (readiness probe).
//
// # Отвечает на вопрос: «можно ли слать сюда трафик прямо сейчас?»
//
// Вот здесь зависимости проверять НУЖНО. Если база недоступна, сервис
// не сможет обработать ни одного осмысленного запроса — и балансировщик
// должен временно увести трафик на другие реплики.
//
// Разница с liveness: readiness=false → «подожди, не шли запросы»,
// liveness=false → «убей и подними заново». Путать их — классическая
// ошибка, приводящая к тем самым каскадным перезапускам.
//
//	@Summary		Проба готовности
//	@Description	Проверяет доступность базы. 503, если база не отвечает.
//	@Tags			health
//	@Produce		json
//	@Success		200	{object}	map[string]interface{}
//	@Failure		503	{object}	map[string]interface{}	"База недоступна"
//	@Router			/readyz [get]
func (h *HealthHandler) Readyz(c *gin.Context) {
	ctx, cancel := context.WithTimeout(c.Request.Context(), healthTimeout)
	defer cancel()

	if err := h.db.Ping(ctx); err != nil {
		// 503 Service Unavailable — «сейчас не могу, попробуй позже».
		//
		// Текст ошибки от базы наружу НЕ отдаём: эти ручки часто открыты
		// без авторизации, и сообщение вида «no route to host 10.0.3.17:5432»
		// подарит атакующему кусок карты внутренней сети.
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "unavailable",
			"reason": "database is not reachable",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"status":  "ok",
		"version": h.version,
	})
}
