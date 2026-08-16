package model_test

// Пакет с суффиксом _test (model_test, а не model).
//
// Это заставляет тест обращаться к коду ТОЛЬКО через экспортированные
// имена — ровно так, как это делает настоящий вызывающий код.
// Если тест лезет во внутренности пакета, он начинает падать при любом
// рефакторинге, ничего при этом не проверяя по существу.

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitikevich-landau/clients-api/internal/model"
)

// ptr — короткий помощник: указатель на значение любого типа.
// В тестах часто нужен *string, а взять адрес литерала в Go нельзя.
func ptr[T any](v T) *T { return &v }

// TestSuggestionPayload_UnmarshalJSON_ThreeStates — главный тест проекта.
//
// Проверяет то, ради чего вообще существует тип Optional: различение
// ТРЁХ состояний поля вместо двух.
func TestSuggestionPayload_UnmarshalJSON_ThreeStates(t *testing.T) {
	// Табличные тесты — идиома Go. Один прогон логики на многих входах,
	// каждый случай именован и виден в выводе по отдельности.
	tests := []struct {
		name        string
		input       string
		wantPresent bool
		wantNull    bool
		wantValue   string
	}{
		{
			name:        "поля нет в JSON — не трогаем",
			input:       `{}`,
			wantPresent: false,
		},
		{
			name:        "поле есть со значением — меняем",
			input:       `{"phone": "+7 900 111-22-33"}`,
			wantPresent: true,
			wantNull:    false,
			wantValue:   "+7 900 111-22-33",
		},
		{
			name:        "поле есть с null — очищаем",
			input:       `{"phone": null}`,
			wantPresent: true,
			wantNull:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var payload model.SuggestionPayload
			require.NoError(t, json.Unmarshal([]byte(tt.input), &payload))

			assert.Equal(t, tt.wantPresent, payload.Phone.Present,
				"признак присутствия поля определён неверно")
			assert.Equal(t, tt.wantNull, payload.Phone.IsNull(),
				"признак явного null определён неверно")

			if tt.wantValue != "" {
				value, ok := payload.Phone.Get()
				require.True(t, ok, "значение должно быть доступно")
				assert.Equal(t, tt.wantValue, value)
			}
		})
	}
}

// TestSuggestionPayload_MarshalJSON_OmitsAbsentFields проверяет,
// что при сохранении в JSONB отсутствующие поля не превращаются в null.
//
// Если бы это сломалось, предложение «поменять только телефон»
// при одобрении стёрло бы клиенту email, компанию и заметки.
func TestSuggestionPayload_MarshalJSON_OmitsAbsentFields(t *testing.T) {
	var payload model.SuggestionPayload
	require.NoError(t, json.Unmarshal([]byte(`{"phone":"+7 900 000-00-00"}`), &payload))

	encoded, err := json.Marshal(payload)
	require.NoError(t, err)

	// Разбираем обратно в карту, чтобы проверить именно НАБОР КЛЮЧЕЙ,
	// а не порядок полей в строке (он в JSON не гарантирован).
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(encoded, &decoded))

	assert.Len(t, decoded, 1, "в JSON должно быть ровно одно поле")
	assert.Contains(t, decoded, "phone")
	assert.NotContains(t, decoded, "email", "отсутствующее поле не должно появляться в JSON")
}

// TestSuggestionPayload_Apply проверяет наложение изменений на карточку.
func TestSuggestionPayload_Apply(t *testing.T) {
	// newClient создаёт свежую карточку для каждого случая:
	// таблица тестов не должна мутировать общее состояние.
	newClient := func() *model.Client {
		return &model.Client{
			FullName: "Ivan Petrov",
			Email:    ptr("ivan@example.com"),
			Phone:    ptr("+7 900 000-00-01"),
			Company:  ptr("Acme LLC"),
			Notes:    ptr("Key account"),
			Status:   model.ClientStatusActive,
		}
	}

	tests := []struct {
		name  string
		input string
		check func(t *testing.T, c *model.Client)
	}{
		{
			name:  "пустой payload ничего не меняет",
			input: `{}`,
			check: func(t *testing.T, c *model.Client) {
				assert.Equal(t, "Ivan Petrov", c.FullName)
				assert.Equal(t, "ivan@example.com", *c.Email)
				assert.Equal(t, "Acme LLC", *c.Company)
			},
		},
		{
			name:  "значение заменяет поле",
			input: `{"phone":"+7 999 999-99-99"}`,
			check: func(t *testing.T, c *model.Client) {
				assert.Equal(t, "+7 999 999-99-99", *c.Phone)
				// Остальные поля не пострадали.
				assert.Equal(t, "ivan@example.com", *c.Email)
			},
		},
		{
			name:  "явный null очищает поле",
			input: `{"email":null}`,
			check: func(t *testing.T, c *model.Client) {
				assert.Nil(t, c.Email, "email должен стать NULL")
				// Соседнее поле не тронуто.
				assert.Equal(t, "+7 900 000-00-01", *c.Phone)
			},
		},
		{
			name:  "пробелы по краям обрезаются",
			input: `{"full_name":"  Ivan Sidorov  "}`,
			check: func(t *testing.T, c *model.Client) {
				assert.Equal(t, "Ivan Sidorov", c.FullName)
			},
		},
		{
			name:  "меняется несколько полей сразу",
			input: `{"company":"Globex","status":"archived","notes":null}`,
			check: func(t *testing.T, c *model.Client) {
				assert.Equal(t, "Globex", *c.Company)
				assert.Equal(t, model.ClientStatusArchived, c.Status)
				assert.Nil(t, c.Notes)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var payload model.SuggestionPayload
			require.NoError(t, json.Unmarshal([]byte(tt.input), &payload))

			client := newClient()
			payload.Apply(client)

			tt.check(t, client)
		})
	}
}

// TestSuggestionPayload_Validate проверяет бизнес-правила payload.
func TestSuggestionPayload_Validate(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		wantField string // какое поле должно попасть в ошибки; "" = ошибок нет
	}{
		{
			name:  "корректный payload",
			input: `{"phone":"+7 900 000-00-00","email":"new@example.com"}`,
		},
		{
			name:  "очистка необязательного поля разрешена",
			input: `{"email":null}`,
		},
		{
			name:      "full_name нельзя обнулить — колонка NOT NULL",
			input:     `{"full_name":null}`,
			wantField: "full_name",
		},
		{
			name:      "full_name не может быть пустым",
			input:     `{"full_name":"   "}`,
			wantField: "full_name",
		},
		{
			name:      "email должен быть похож на адрес",
			input:     `{"email":"не-адрес"}`,
			wantField: "email",
		},
		{
			name:      "status нельзя обнулить",
			input:     `{"status":null}`,
			wantField: "status",
		},
		{
			name:      "status только из известного списка",
			input:     `{"status":"deleted"}`,
			wantField: "status",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var payload model.SuggestionPayload
			require.NoError(t, json.Unmarshal([]byte(tt.input), &payload))

			errs := payload.Validate()

			if tt.wantField == "" {
				assert.Empty(t, errs, "ожидался валидный payload, получены ошибки: %v", errs)
				return
			}

			assert.Contains(t, errs, tt.wantField,
				"ожидалась ошибка по полю %q, получено: %v", tt.wantField, errs)
		})
	}
}

// TestSuggestionPayload_IsEmptyAndChangedFields проверяет вспомогательные методы,
// на которых держатся проверка «предложение не пустое» и вывод в ответе API.
func TestSuggestionPayload_IsEmptyAndChangedFields(t *testing.T) {
	t.Run("пустой payload", func(t *testing.T) {
		var payload model.SuggestionPayload
		require.NoError(t, json.Unmarshal([]byte(`{}`), &payload))

		assert.True(t, payload.IsEmpty())
		assert.Empty(t, payload.ChangedFields())
	})

	t.Run("неизвестные ключи игнорируются и payload остаётся пустым", func(t *testing.T) {
		// Клиент прислал мусор. encoding/json незнакомые ключи молча
		// пропускает, поэтому payload должен остаться пустым —
		// и сервис отвергнет такое предложение.
		var payload model.SuggestionPayload
		require.NoError(t, json.Unmarshal([]byte(`{"unknown_field":"x"}`), &payload))

		assert.True(t, payload.IsEmpty())
	})

	t.Run("список изменённых полей отсортирован", func(t *testing.T) {
		var payload model.SuggestionPayload
		require.NoError(t, json.Unmarshal(
			[]byte(`{"phone":"+7","email":null,"company":"X"}`), &payload))

		assert.False(t, payload.IsEmpty())
		// Сортировка делает результат стабильным: одинаковый вход
		// всегда даёт одинаковый выход.
		assert.Equal(t, []string{"company", "email", "phone"}, payload.ChangedFields())
	})
}
