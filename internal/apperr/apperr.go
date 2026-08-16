// Package apperr — единый тип ошибки приложения и его перевод в HTTP.
//
// # Зачем это нужно
//
// Слои не должны знать друг о друге лишнего:
//
//	repository  — говорит "в базе не нашлось" (техническим языком)
//	service     — превращает это в доменную ошибку "клиент не существует"
//	handler     — превращает доменную ошибку в HTTP: 404 + JSON
//
// Если бы repository возвращал http.StatusNotFound, слои слиплись бы,
// и переиспользовать логику вне HTTP (в CLI, в gRPC, в фоновой задаче)
// стало бы невозможно.
//
// # Второе важное правило
//
// Клиенту — БЕЗОПАСНОЕ сообщение, в лог — ПОДРОБНОСТИ.
// Отдавать наружу текст ошибки PostgreSQL нельзя: это прямая подсказка
// атакующему про структуру базы. Поэтому в Error есть два поля:
// Message (уходит клиенту) и внутреннее err (уходит только в лог).
package apperr

import (
	"errors"
	"fmt"
	"net/http"
)

// Code — машиночитаемый код ошибки.
//
// Клиент должен принимать решения по КОДУ, а не по тексту сообщения:
// текст мы можем переписать или перевести в любой момент, код — контракт.
type Code string

const (
	CodeValidation       Code = "validation_error"   // невалидные входные данные
	CodeUnauthorized     Code = "unauthorized"       // нет токена или он невалиден
	CodeForbidden        Code = "forbidden"          // токен есть, но прав не хватает
	CodeNotFound         Code = "not_found"          // объект не существует
	CodeMethodNotAllowed Code = "method_not_allowed" // путь есть, метод не тот
	CodeConflict         Code = "conflict"           // нарушение уникальности / бизнес-правила
	CodeTimeout          Code = "timeout"            // не уложились в отведённое время
	CodeInternal         Code = "internal_error"     // всё остальное, наша вина
)

// Error — ошибка приложения.
//
// Реализует стандартный интерфейс error, поддерживает errors.Is/errors.As
// через метод Unwrap.
type Error struct {
	// Code — машиночитаемый код, определяет и HTTP-статус.
	Code Code

	// Message — безопасный текст для клиента.
	// Не должен содержать деталей реализации: имён таблиц, SQL, стектрейсов.
	Message string

	// Details — пояснения по полям, заполняются при ошибках валидации.
	// Например: {"email": "must be a valid email address"}.
	Details map[string]string

	// err — исходная причина. НЕ отдаётся клиенту, попадает только в лог.
	err error
}

// Error реализует интерфейс error.
func (e *Error) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Code, e.Message, e.err)
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Unwrap возвращает исходную ошибку, чтобы работали errors.Is и errors.As.
// Это позволяет писать, например, errors.Is(err, pgx.ErrNoRows)
// даже когда ошибка уже завёрнута в apperr.Error.
func (e *Error) Unwrap() error { return e.err }

// HTTPStatus переводит код ошибки в HTTP-статус.
//
// Это ЕДИНСТВЕННОЕ место в проекте, где доменные ошибки встречаются
// с протоколом HTTP. Всё остальное приложение о HTTP не знает.
func (e *Error) HTTPStatus() int {
	switch e.Code {
	case CodeValidation:
		return http.StatusBadRequest // 400
	case CodeUnauthorized:
		return http.StatusUnauthorized // 401
	case CodeForbidden:
		return http.StatusForbidden // 403
	case CodeNotFound:
		return http.StatusNotFound // 404
	case CodeMethodNotAllowed:
		return http.StatusMethodNotAllowed // 405
	case CodeConflict:
		return http.StatusConflict // 409
	case CodeTimeout:
		return http.StatusGatewayTimeout // 504
	default:
		return http.StatusInternalServerError // 500
	}
}

// WithDetails добавляет пояснения по полям и возвращает ту же ошибку.
// Удобно для цепочки вызовов: apperr.Validation("...").WithDetails(m).
func (e *Error) WithDetails(details map[string]string) *Error {
	e.Details = details
	return e
}

// Wrap прикрепляет к ошибке исходную причину (для лога) и возвращает её же.
func (e *Error) Wrap(err error) *Error {
	e.err = err
	return e
}

// ---------------------------------------------------------------------------
// Конструкторы. Пользоваться надо ими, а не собирать структуру руками —
// так гарантированно не забудешь код и не отдашь клиенту лишнего.
// ---------------------------------------------------------------------------

// Validation — входные данные не прошли проверку. 400.
func Validation(message string) *Error {
	return &Error{Code: CodeValidation, Message: message}
}

// Unauthorized — не аутентифицирован (нет токена / токен невалиден). 401.
func Unauthorized(message string) *Error {
	return &Error{Code: CodeUnauthorized, Message: message}
}

// Forbidden — аутентифицирован, но не авторизован (прав не хватает). 403.
//
// Разница между 401 и 403, которую вечно путают:
//
//	401 — "я не знаю, кто ты"      → иди залогинься
//	403 — "я знаю, кто ты, и тебе нельзя" → логин не поможет
func Forbidden(message string) *Error {
	return &Error{Code: CodeForbidden, Message: message}
}

// NotFound — объект не найден. 404.
// Параметр resource — что именно искали: "client", "suggestion", "user".
func NotFound(resource string) *Error {
	return &Error{
		Code:    CodeNotFound,
		Message: fmt.Sprintf("%s not found", resource),
	}
}

// MethodNotAllowed — путь существует, но не поддерживает этот метод. 405.
//
// Отличать от 404 полезно: 405 сразу подсказывает клиенту, что путь
// он угадал верно и ошибся только методом.
func MethodNotAllowed(message string) *Error {
	return &Error{Code: CodeMethodNotAllowed, Message: message}
}

// Conflict — конфликт состояния: нарушена уникальность
// или бизнес-правило запрещает операцию в текущем состоянии. 409.
func Conflict(message string) *Error {
	return &Error{Code: CodeConflict, Message: message}
}

// Timeout — не уложились в отведённое время. 504.
func Timeout(message string) *Error {
	return &Error{Code: CodeTimeout, Message: message}
}

// Internal — наша внутренняя ошибка. 500.
//
// Клиенту уходит обезличенное "internal server error",
// а настоящая причина сохраняется внутри и пишется в лог.
func Internal(err error) *Error {
	return &Error{
		Code:    CodeInternal,
		Message: "internal server error",
		err:     err,
	}
}

// ---------------------------------------------------------------------------
// Разбор произвольной ошибки
// ---------------------------------------------------------------------------

// From приводит любую ошибку к *Error.
//
// Если где-то в цепочке обёрток лежит наш *Error — достаём его.
// Если нет — значит, это неожиданная ошибка, заворачиваем в Internal,
// чтобы клиент случайно не увидел внутренности системы.
//
// Именно эта функция вызывается в HTTP-хендлерах.
func From(err error) *Error {
	if err == nil {
		return nil
	}

	var appErr *Error
	if errors.As(err, &appErr) {
		return appErr
	}

	return Internal(err)
}

// IsNotFound — удобная проверка для service-слоя,
// когда надо отличить "не нашлось" от настоящей поломки.
func IsNotFound(err error) bool {
	var appErr *Error
	return errors.As(err, &appErr) && appErr.Code == CodeNotFound
}
