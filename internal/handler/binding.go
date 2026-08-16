// Package handler — транспортный слой: HTTP-обработчики на gin.
//
// # Обязанности хендлера, и только они
//
//  1. разобрать вход (путь, query, тело) и проверить его формат;
//  2. позвать нужный метод сервиса;
//  3. превратить результат или ошибку в HTTP-ответ.
//
// Бизнес-логики здесь нет. Если в хендлере появляется «если статус
// такой, то...» — это признак, что логика утекла не в тот слой.
package handler

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/gin-gonic/gin/binding"
	"github.com/go-playground/validator/v10"
	"github.com/google/uuid"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
	"github.com/vitikevich-landau/clients-api/internal/response"
)

// SetupValidator донастраивает валидатор, встроенный в gin.
//
// # Какую проблему решаем
//
// По умолчанию validator сообщает об ошибке, используя имя поля
// СТРУКТУРЫ GO:
//
//	{"FullName": "required"}
//
// Клиент же присылал JSON и знать не знает про поле FullName —
// у него оно называется full_name. Ответ, в котором фигурируют
// внутренние имена, бесполезен для клиента и заодно раскрывает
// устройство наших структур.
//
// RegisterTagNameFunc заставляет валидатор брать имя из тега json
// (а если его нет — из form). Получаем:
//
//	{"full_name": "is required"}
//
// Вызывается ОДИН РАЗ при старте, из сборки роутера.
func SetupValidator() {
	v, ok := binding.Validator.Engine().(*validator.Validate)
	if !ok {
		// Ситуация «не может случиться»: gin всегда использует
		// go-playground/validator. Но молча игнорировать нельзя —
		// сообщения об ошибках тихо испортятся.
		panic("handler: gin validator engine has an unexpected type")
	}

	v.RegisterTagNameFunc(func(field reflect.StructField) string {
		// Тег вида `json:"full_name,omitempty"` — берём часть до запятой.
		name := strings.SplitN(field.Tag.Get("json"), ",", 2)[0]
		if name == "" {
			name = strings.SplitN(field.Tag.Get("form"), ",", 2)[0]
		}
		if name == "-" {
			return "" // поле исключено из сериализации
		}
		return name
	})
}

// bindJSON разбирает тело запроса и валидирует его.
//
// Возвращает false, если что-то не так — ответ клиенту при этом
// УЖЕ ОТПРАВЛЕН. Поэтому вызов выглядит так:
//
//	var req model.CreateClientRequest
//	if !bindJSON(c, &req) {
//	    return
//	}
func bindJSON(c *gin.Context, dst any) bool {
	err := c.ShouldBindJSON(dst)
	if err == nil {
		return true
	}

	// --- Случай 1: не прошла валидация по тегам binding ---
	var validationErrs validator.ValidationErrors
	if errors.As(err, &validationErrs) {
		response.ValidationError(c, "request validation failed",
			validationDetails(validationErrs))
		return false
	}

	// --- Случай 2: тело пустое ---
	// Отдельное сообщение, потому что «unexpected end of JSON input»
	// для человека звучит куда менее понятно, чем «пришлите тело».
	if errors.Is(err, io.EOF) {
		response.Error(c, apperr.Validation("request body is required").Wrap(err))
		return false
	}

	// --- Случай 3: тип не совпал ---
	// Например, прислали {"limit": "двадцать"} вместо числа.
	// Говорим клиенту, ГДЕ именно ошибка.
	var typeErr *json.UnmarshalTypeError
	if errors.As(err, &typeErr) {
		field := typeErr.Field
		if field == "" {
			field = "body"
		}
		response.ValidationError(c, "invalid value type", map[string]string{
			field: fmt.Sprintf("expected %s", typeErr.Type.String()),
		})
		return false
	}

	// --- Случай 4: битый JSON и всё прочее ---
	//
	// Текст ошибки парсера наружу НЕ отдаём: он может содержать
	// куски присланного тела, а там бывают пароли. Подробности —
	// в лог через Wrap, клиенту — общая формулировка.
	response.Error(c, apperr.Validation("invalid JSON body").Wrap(err))
	return false
}

// bindQuery разбирает и валидирует параметры строки запроса.
func bindQuery(c *gin.Context, dst any) bool {
	err := c.ShouldBindQuery(dst)
	if err == nil {
		return true
	}

	var validationErrs validator.ValidationErrors
	if errors.As(err, &validationErrs) {
		response.ValidationError(c, "invalid query parameters",
			validationDetails(validationErrs))
		return false
	}

	response.Error(c, apperr.Validation("invalid query parameters").Wrap(err))
	return false
}

// parseUUIDParam достаёт UUID из пути (/clients/:id).
//
// Возвращает uuid.Nil и false при неудаче — ответ клиенту уже отправлен.
//
// Отдельная функция нужна, чтобы на кривой идентификатор отвечать 400,
// а не 500. Без неё uuid.Parse вернул бы ошибку, которая по умолчанию
// превратилась бы во внутреннюю ошибку сервера — а виноват-то клиент.
func parseUUIDParam(c *gin.Context, name string) (uuid.UUID, bool) {
	raw := c.Param(name)

	id, err := uuid.Parse(raw)
	if err != nil {
		response.ValidationError(c, "invalid identifier", map[string]string{
			name: "must be a valid UUID",
		})
		return uuid.Nil, false
	}

	return id, true
}

// validationDetails превращает ошибки валидатора в карту поле → сообщение.
//
// Формулировки пишем сами, потому что стандартные от validator выглядят так:
//
//	Key: 'CreateClientRequest.FullName' Error:Field validation for
//	'FullName' failed on the 'required' tag
//
// Показывать такое клиенту нельзя: это отладочный вывод, а не сообщение
// об ошибке. Плюс он раскрывает имена наших внутренних структур.
func validationDetails(errs validator.ValidationErrors) map[string]string {
	details := make(map[string]string, len(errs))

	for _, e := range errs {
		// e.Field() — имя поля (уже в json-виде благодаря SetupValidator).
		// e.Tag()   — какое правило нарушено: required, email, min...
		// e.Param() — параметр правила: для min=8 это "8".
		details[e.Field()] = describeValidationError(e.Tag(), e.Param())
	}

	return details
}

// describeValidationError переводит правило валидатора в понятный текст.
//
// Сообщения на английском — это интерфейс API, а не интерфейс пользователя.
// Перевод на язык человека делает фронтенд, у которого есть контекст:
// он знает и локаль пользователя, и как называется поле в его форме.
func describeValidationError(tag, param string) string {
	switch tag {
	case "required":
		return "is required"
	case "email":
		return "must be a valid email address"
	case "uuid", "uuid4":
		return "must be a valid UUID"
	case "min":
		return fmt.Sprintf("must be at least %s characters long", param)
	case "max":
		return fmt.Sprintf("must be at most %s characters long", param)
	case "oneof":
		return fmt.Sprintf("must be one of: %s", strings.ReplaceAll(param, " ", ", "))
	case "gte":
		return fmt.Sprintf("must be greater than or equal to %s", param)
	case "lte":
		return fmt.Sprintf("must be less than or equal to %s", param)
	case "url":
		return "must be a valid URL"
	default:
		// Страховка на случай, если кто-то добавит правило,
		// а сюда его описание внести забудет.
		return fmt.Sprintf("failed validation rule: %s", tag)
	}
}
