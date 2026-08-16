// Package response — единый формат HTTP-ответов.
//
// # Зачем отдельный пакет
//
// Формат ошибки должен быть ОДИНАКОВЫМ на всех роутах. Клиент пишет
// один кусок кода разбора ошибок, а не гадает, что придёт с каждой ручки.
//
// Пакет отдельный (а не внутри handler), потому что писать ответы нужно
// и хендлерам, и middleware — например, Recovery отвечает 500, а Auth
// отвечает 401, не доходя до хендлера. Общий пакет разрывает
// циклическую зависимость handler ⇄ middleware.
package response

import (
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
)

// headerRequestID — тот же заголовок, что ставит RequestID-middleware.
//
// Значение читается из уже записанных заголовков ОТВЕТА, а не из gin.Context.
// Так этот пакет не зависит от пакета middleware, и цикла импортов не возникает.
const headerRequestID = "X-Request-ID"

// ErrorBody — тело ошибки.
type ErrorBody struct {
	// Code — машиночитаемый код: not_found, validation_error, ...
	// Клиент принимает решения ПО КОДУ, а не по тексту:
	// текст можно переписать или перевести, код — контракт.
	Code apperr.Code `json:"code"`

	// Message — человекочитаемое описание. Безопасное: без имён таблиц,
	// SQL и стектрейсов.
	Message string `json:"message"`

	// Details — пояснения по конкретным полям при ошибке валидации.
	// omitempty: в остальных случаях ключа в JSON просто не будет.
	Details map[string]string `json:"details,omitempty"`

	// RequestID — чтобы пользователь мог назвать его в поддержке,
	// а мы по нему нашли весь запрос в логах.
	RequestID string `json:"request_id,omitempty"`
}

// ErrorResponse — обёртка вокруг тела ошибки.
//
// Вложенность {"error": {...}} — не бюрократия. Она позволяет однозначно
// отличить ошибку от успешного ответа: наличие ключа "error" и есть признак.
// Плоский формат такой возможности не даёт.
type ErrorResponse struct {
	Error ErrorBody `json:"error"`
}

// Error отправляет ошибку клиенту в едином формате.
//
// Принимает ЛЮБУЮ ошибку: apperr.From сам разберётся, наша это доменная
// ошибка или неожиданная. Во втором случае наружу уйдёт обезличенное
// "internal server error", а подробности останутся в логе.
func Error(c *gin.Context, err error) {
	appErr := apperr.From(err)

	body := ErrorBody{
		Code:      appErr.Code,
		Message:   appErr.Message,
		Details:   appErr.Details,
		RequestID: c.Writer.Header().Get(headerRequestID),
	}

	// AbortWithStatusJSON = записать ответ + оборвать цепочку middleware.
	// Обрыв важен: без него следующие в цепочке могут попытаться
	// записать ещё один ответ поверх нашего.
	c.AbortWithStatusJSON(appErr.HTTPStatus(), ErrorResponse{Error: body})
}

// ValidationError отправляет 400 с разбором по полям.
//
// Отдельная функция, потому что ошибки валидации — единственные,
// где клиенту полезны подробности: какое поле не понравилось и почему.
func ValidationError(c *gin.Context, message string, details map[string]string) {
	Error(c, apperr.Validation(message).WithDetails(details))
}

// OK отправляет 200 с телом.
func OK(c *gin.Context, payload any) {
	c.JSON(http.StatusOK, payload)
}

// Created отправляет 201 с телом.
//
// Именно 201, а не 200: код сообщает клиенту, что ресурс СОЗДАН.
// Это часть контракта HTTP, и клиентские библиотеки на него полагаются.
func Created(c *gin.Context, payload any) {
	c.JSON(http.StatusCreated, payload)
}

// NoContent отправляет 204 без тела.
//
// Стандартный ответ на успешное удаление: подтверждаем, что всё прошло,
// и честно говорим, что возвращать нечего.
func NoContent(c *gin.Context) {
	c.Status(http.StatusNoContent)
}
