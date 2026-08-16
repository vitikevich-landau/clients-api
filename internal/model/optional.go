package model

import (
	"bytes"
	"encoding/json"
)

// Optional[T] — значение, которое умеет различать ТРИ состояния,
// а не два, как обычный указатель.
//
// # Зачем это нужно
//
// Пользователь предлагает правку карточки клиента. Он присылает JSON:
//
//	{"phone": "+7 900 111-22-33"}   → поменять телефон
//	{"phone": null}                 → ОЧИСТИТЬ телефон
//	{}                              → телефон вообще не трогать
//
// Второй и третий случаи — РАЗНЫЕ операции, и их надо различать.
// Обычный *string этого не умеет: и «не передали», и «передали null»
// дают одинаковый nil.
//
// Optional различает:
//
//	Present=false, Value=nil  → ключа в JSON не было      (не трогаем)
//	Present=true,  Value=nil  → был явный null            (очищаем)
//	Present=true,  Value=&v   → было значение             (ставим v)
//
// Ровно такая семантика описана в стандарте JSON Merge Patch (RFC 7396).
//
// # Как это работает технически
//
// encoding/json вызывает UnmarshalJSON ТОЛЬКО для ключей, которые реально
// присутствуют в документе. Значит, сам факт вызова = «ключ был»,
// и мы просто выставляем Present=true внутри метода.
type Optional[T any] struct {
	// Present — ключ присутствовал во входном JSON.
	Present bool

	// Value — само значение. nil при Present=true означает явный null.
	Value *T
}

// Set создаёт Optional со значением. Пригодится в тестах и в коде,
// который собирает payload программно.
func Set[T any](v T) Optional[T] {
	return Optional[T]{Present: true, Value: &v}
}

// Null создаёт Optional, означающий «очистить поле».
func Null[T any]() Optional[T] {
	return Optional[T]{Present: true, Value: nil}
}

// UnmarshalJSON разбирает значение и помечает поле как присутствующее.
//
// Указатель в получателе (*Optional[T]) обязателен: метод меняет структуру.
// С получателем-значением изменения ушли бы в копию, и Present всегда
// оставался бы false — классическая ошибка.
func (o *Optional[T]) UnmarshalJSON(data []byte) error {
	// Сам факт вызова означает, что ключ был в JSON.
	o.Present = true

	if bytes.Equal(bytes.TrimSpace(data), []byte("null")) {
		o.Value = nil
		return nil
	}

	var v T
	if err := json.Unmarshal(data, &v); err != nil {
		return err
	}
	o.Value = &v
	return nil
}

// MarshalJSON сериализует значение (или null).
//
// Обрати внимание: отличить «не присутствует» на этом уровне нельзя —
// encoding/json не умеет по решению метода ВЫБРОСИТЬ ключ из объекта.
// Пропуск отсутствующих полей реализован выше, в SuggestionPayload.MarshalJSON,
// который собирает map только из присутствующих ключей.
func (o Optional[T]) MarshalJSON() ([]byte, error) {
	if o.Value == nil {
		return []byte("null"), nil
	}
	return json.Marshal(*o.Value)
}

// IsNull — было передано явное null (то есть «очистить поле»).
func (o Optional[T]) IsNull() bool {
	return o.Present && o.Value == nil
}

// HasValue — было передано конкретное непустое значение.
func (o Optional[T]) HasValue() bool {
	return o.Present && o.Value != nil
}

// Get возвращает значение и признак того, что оно есть.
// Идиоматичная для Go форма «значение, ok».
func (o Optional[T]) Get() (T, bool) {
	if o.Value == nil {
		var zero T
		return zero, false
	}
	return *o.Value, true
}
