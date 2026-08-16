package model

// Pagination — сведения о постраничной выдаче, которые уезжают клиенту
// вместе со списком.
//
// Зачем отдавать Total: без него фронтенд не может нарисовать
// «страница 3 из 17» и не знает, когда прекращать подгрузку.
//
// Цена: COUNT(*) — это отдельный запрос, и на больших таблицах он не
// бесплатный. На миллионах строк переходят либо на приблизительный
// подсчёт (reltuples из pg_class), либо на курсорную пагинацию,
// где Total не нужен вовсе.
type Pagination struct {
	Limit  int `json:"limit"`
	Offset int `json:"offset"`
	Total  int `json:"total"`
}

// ListResponse[T] — универсальная обёртка для списочных ответов.
//
// # Почему не отдавать просто массив
//
// Ответ вида `[{...}, {...}]` — тупик: добавить к нему общее количество
// или курсор следующей страницы можно только сломав контракт.
// Объект-обёртка расширяется без ломающих изменений.
//
// Дженерик (Go 1.18+) избавляет от копипасты: одна структура
// обслуживает и клиентов, и предложения, и что угодно ещё.
type ListResponse[T any] struct {
	Items      []T        `json:"items"`
	Pagination Pagination `json:"pagination"`
}

// NewListResponse собирает списочный ответ.
func NewListResponse[T any](items []T, limit, offset, total int) ListResponse[T] {
	// Страховка от null в JSON: nil-срез сериализуется в null,
	// а пустой — в []. Клиенту всегда отдаём массив.
	if items == nil {
		items = make([]T, 0)
	}

	return ListResponse[T]{
		Items: items,
		Pagination: Pagination{
			Limit:  limit,
			Offset: offset,
			Total:  total,
		},
	}
}
