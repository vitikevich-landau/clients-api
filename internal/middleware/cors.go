package middleware

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// CORS разрешает браузерным клиентам с других доменов обращаться к нашему API.
//
// # Что вообще происходит
//
// Браузер по умолчанию ЗАПРЕЩАЕТ странице с домена A читать ответ от домена B.
// Это Same-Origin Policy — базовая защита: без неё любой сайт мог бы
// читать твою почту, пока ты залогинен, просто сделав запрос к её API.
//
// CORS — способ для сервера сказать «этому домену можно». Разрешение даёт
// СЕРВЕР заголовками ответа, а исполняет БРАУЗЕР.
//
// Важное следствие: CORS — не защита сервера. Curl, Postman и любой скрипт
// на бэкенде игнорируют его полностью. Защищает он ПОЛЬЗОВАТЕЛЯ БРАУЗЕРА
// от чужих сайтов, а не наш API от запросов.
//
// # Preflight
//
// Перед «непростым» запросом (PUT, DELETE, свои заголовки вроде
// Authorization) браузер сам шлёт предварительный OPTIONS-запрос:
// «а можно вообще?». Ответить на него надо ДО того, как запрос дойдёт
// до хендлера — иначе браузер откажется отправлять основной запрос.
func CORS(allowedOrigins []string) gin.HandlerFunc {
	// Разбираем настройки один раз при регистрации.
	allowAll := false
	origins := make(map[string]struct{}, len(allowedOrigins))

	for _, o := range allowedOrigins {
		o = strings.TrimSpace(o)
		if o == "*" {
			allowAll = true
			continue
		}
		if o != "" {
			origins[o] = struct{}{}
		}
	}

	// Заранее собранные строки заголовков — чтобы не клеить их на каждый запрос.
	allowMethods := strings.Join([]string{
		http.MethodGet, http.MethodPost, http.MethodPut,
		http.MethodPatch, http.MethodDelete, http.MethodOptions,
	}, ", ")

	allowHeaders := strings.Join([]string{
		"Authorization", "Content-Type", HeaderRequestID,
	}, ", ")

	// Сколько браузеру разрешено кэшировать результат preflight.
	// Без этого OPTIONS полетит перед КАЖДЫМ запросом — вдвое больше
	// обращений к серверу на ровном месте.
	maxAge := strconv.Itoa(int((12 * time.Hour).Seconds()))

	return func(c *gin.Context) {
		origin := c.GetHeader("Origin")

		// Заголовка Origin нет — запрос не из браузера (curl, другой сервис).
		// CORS к нему отношения не имеет, просто пропускаем.
		if origin == "" {
			c.Next()
			return
		}

		_, ok := origins[origin]
		if !ok && !allowAll {
			// Домен не в списке. Заголовки разрешения НЕ ставим —
			// браузер сам не отдаст ответ своей странице.
			//
			// Обрывать запрос здесь не надо: для не-браузерных клиентов
			// он абсолютно легитимен.
			c.Next()
			return
		}

		if allowAll {
			c.Header("Access-Control-Allow-Origin", "*")
		} else {
			// Эхо конкретного домена, а не "*".
			//
			// Это обязательно, если используются куки или credentials:
			// браузер отвергает сочетание "*" с Allow-Credentials.
			c.Header("Access-Control-Allow-Origin", origin)

			// Vary говорит кэшам (CDN, прокси), что ответ зависит
			// от заголовка Origin. Без него кэш отдаст ответ,
			// сформированный для ДРУГОГО домена — и всё сломается
			// самым загадочным образом.
			c.Header("Vary", "Origin")
		}

		c.Header("Access-Control-Allow-Methods", allowMethods)
		c.Header("Access-Control-Allow-Headers", allowHeaders)

		// Разрешаем JavaScript читать наш X-Request-ID из ответа.
		// По умолчанию браузер прячет от скрипта все нестандартные
		// заголовки — их надо перечислять явно.
		c.Header("Access-Control-Expose-Headers", HeaderRequestID)

		c.Header("Access-Control-Max-Age", maxAge)

		// Preflight: отвечаем сразу и дальше не пускаем.
		// 204 — «всё в порядке, тела нет».
		if c.Request.Method == http.MethodOptions {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}
