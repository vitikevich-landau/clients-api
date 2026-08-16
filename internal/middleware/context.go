// Package middleware — обвязка вокруг обработчиков запросов.
//
// # Модель «луковицы»
//
// Middleware выполняются не «до» и не «после» хендлера, а ВОКРУГ него.
// Запрос проваливается внутрь до самого центра, а ответ выныривает
// обратно через те же слои в обратном порядке:
//
//	Запрос →  [Recovery]
//	            [RequestID]
//	              [Logger]
//	                [Timeout]
//	                  [CORS]
//	                    [Auth]
//	                      [RequireAdmin]
//	                        → ХЕНДЛЕР ←
//	                      [RequireAdmin]
//	                    [Auth]
//	                  [CORS]
//	                [Timeout]
//	              [Logger]
//	            [RequestID]
//	          [Recovery]   → Ответ
//
// Внутри middleware вызов c.Next() означает «провалиться глубже».
// Код ДО c.Next() выполняется на пути внутрь, код ПОСЛЕ — на пути наружу.
// Именно поэтому логгер умеет писать «запрос занял 45 мс»: время
// он засекает до c.Next(), а считает после.
//
// c.Abort() обрывает цепочку — следующие middleware и хендлер
// не выполнятся вовсе.
//
// ВНИМАНИЕ, КЛАССИЧЕСКАЯ ОШИБКА: c.Abort() НЕ делает return из функции.
// Он лишь помечает цепочку прерванной. Забыл написать return после Abort —
// и код ниже продолжит выполняться в том же middleware.
package middleware

import (
	"github.com/gin-gonic/gin"

	"github.com/vitikevich-landau/clients-api/internal/model"
)

// Ключи, под которыми данные лежат в gin.Context.
//
// gin.Context хранит значения в map[string]any, поэтому ключи здесь —
// строки, а не типы, как в стандартном context.Context. Константы нужны,
// чтобы опечатка в строковом литерале не превращалась в тихий баг:
// c.Get("auth_usr") вернул бы nil, и проверка прав молча развалилась бы.
const (
	ctxKeyRequestID = "request_id"
	ctxKeyAuthUser  = "auth_user"
)

// HeaderRequestID — имя заголовка с идентификатором запроса.
//
// X-Request-ID — де-факто стандарт: его проставляют nginx, Envoy,
// балансировщики облаков. Мы подхватываем чужой, если он пришёл,
// и генерируем свой, если нет.
const HeaderRequestID = "X-Request-ID"

// GetRequestID возвращает идентификатор текущего запроса.
// Пустая строка означает, что RequestID-middleware не подключён.
func GetRequestID(c *gin.Context) string {
	if v, ok := c.Get(ctxKeyRequestID); ok {
		if id, ok := v.(string); ok {
			return id
		}
	}
	return ""
}

// GetAuthUser возвращает текущего пользователя, если запрос аутентифицирован.
//
// Второе значение — признак наличия. Пригодится там, где авторизация
// необязательна, но при её наличии поведение меняется.
func GetAuthUser(c *gin.Context) (model.AuthUser, bool) {
	v, ok := c.Get(ctxKeyAuthUser)
	if !ok {
		return model.AuthUser{}, false
	}
	user, ok := v.(model.AuthUser)
	return user, ok
}

// MustGetAuthUser возвращает текущего пользователя или паникует.
//
// # Почему паника — это здесь правильно
//
// Дойти сюда без пользователя в контексте можно ровно одним способом:
// роут зарегистрировали в группе БЕЗ middleware.Auth. Это не ошибка
// пользователя API и не сбой инфраструктуры — это ошибка программиста,
// допущенная при сборке роутера.
//
// Тихо вернуть 401 в такой ситуации хуже: баг замаскируется под штатное
// поведение и доедет до прода. Паника же будет поймана Recovery,
// громко записана в лог со стектрейсом и сразу найдена.
//
// Приём допустим ИМЕННО потому, что условие проверяется при сборке роутов,
// то есть один раз при старте, а не зависит от входных данных.
func MustGetAuthUser(c *gin.Context) model.AuthUser {
	user, ok := GetAuthUser(c)
	if !ok {
		panic("middleware: auth user is missing in context — " +
			"the route is probably registered without middleware.Auth")
	}
	return user
}
