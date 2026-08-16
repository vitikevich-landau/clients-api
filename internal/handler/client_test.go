package handler_test

// Тесты транспортного слоя.
//
// # Что здесь проверяется
//
// Коды ответов, разбор параметров, формат JSON, работа middleware —
// то есть ровно HTTP-часть. Бизнес-логика подменена заглушкой:
// её проверяют тесты пакета service.
//
// Такое разделение означает, что каждый тест падает по своей причине.
// Сломался SQL — упали тесты репозитория. Сломался формат ошибки —
// упали эти. Не приходится гадать, где именно проблема.
//
// # httptest вместо настоящего сервера
//
// httptest.NewRecorder() изображает http.ResponseWriter и запоминает
// всё, что в него записали. Порт не занимается, сеть не используется,
// тест выполняется за микросекунды.

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
	"github.com/vitikevich-landau/clients-api/internal/handler"
	"github.com/vitikevich-landau/clients-api/internal/middleware"
	"github.com/vitikevich-landau/clients-api/internal/model"
	"github.com/vitikevich-landau/clients-api/internal/response"
)

// TestMain выполняется один раз до всех тестов пакета.
func TestMain(m *testing.M) {
	// TestMode отключает отладочный вывод gin — иначе он забивает
	// вывод тестов списком роутов и предупреждениями.
	gin.SetMode(gin.TestMode)

	// Без этого вызова имена полей в ошибках валидации будут
	// в стиле Go (FullName), а не в стиле JSON (full_name).
	handler.SetupValidator()

	os.Exit(m.Run())
}

// ---------------------------------------------------------------------------
// Заглушки
// ---------------------------------------------------------------------------

// validToken — «правильный» токен для тестов.
const validToken = "valid-test-token"

var testUser = model.AuthUser{
	ID:    uuid.MustParse("bbbbbbbb-0000-4000-8000-000000000001"),
	Email: "user@example.com",
	Role:  model.RoleUser,
}

// fakeTokenParser изображает разбор JWT.
//
// Настоящий middleware.Auth при этом используется НАСТОЯЩИЙ — тест
// заодно проверяет и разбор заголовка Authorization, и ответ 401.
type fakeTokenParser struct{}

func (f *fakeTokenParser) Parse(token string) (*model.AuthUser, error) {
	if token != validToken {
		return nil, apperr.Unauthorized("invalid token")
	}
	user := testUser
	return &user, nil
}

// fakeClientService — заглушка бизнес-логики.
//
// Поля Err и Client позволяют тесту задать нужный исход,
// не поднимая ни сервис, ни базу.
type fakeClientService struct {
	Client  *model.Client
	Clients []model.Client
	Total   int
	Err     error

	// DeletedID запоминает, что именно просили удалить —
	// так тест проверяет, что идентификатор из пути дошёл до сервиса.
	DeletedID uuid.UUID
}

func (f *fakeClientService) List(context.Context, model.ListClientsQuery) ([]model.Client, int, error) {
	if f.Err != nil {
		return nil, 0, f.Err
	}
	return f.Clients, f.Total, nil
}

func (f *fakeClientService) GetByID(context.Context, uuid.UUID) (*model.Client, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Client, nil
}

func (f *fakeClientService) Create(context.Context, model.CreateClientRequest, model.AuthUser) (*model.Client, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Client, nil
}

func (f *fakeClientService) Update(context.Context, uuid.UUID, model.UpdateClientRequest, model.AuthUser) (*model.Client, error) {
	if f.Err != nil {
		return nil, f.Err
	}
	return f.Client, nil
}

func (f *fakeClientService) Delete(_ context.Context, id uuid.UUID, _ model.AuthUser) error {
	f.DeletedID = id
	return f.Err
}

// ---------------------------------------------------------------------------
// Обвязка
// ---------------------------------------------------------------------------

// newTestRouter собирает минимальный роутер: только нужные роуты
// и настоящий middleware.Auth поверх заглушки разбора токена.
//
// Полный handler.NewRouter здесь не нужен — он потребовал бы конфиг,
// логгер и все остальные хендлеры. Тест должен подниматься одной строкой.
func newTestRouter(svc handler.ClientService) *gin.Engine {
	h := handler.NewClientHandler(svc)

	router := gin.New()

	// RequestID нужен, чтобы в теле ошибки заполнялось поле request_id.
	router.Use(middleware.RequestID())

	authorized := router.Group("")
	authorized.Use(middleware.Auth(&fakeTokenParser{}))
	{
		authorized.GET("/clients", h.List)
		authorized.GET("/clients/:id", h.Get)
		authorized.POST("/clients", h.Create)
		authorized.DELETE("/clients/:id", h.Delete)
	}

	return router
}

// doRequest выполняет запрос к роутеру и возвращает записанный ответ.
//
// Параметр token пустой = заголовок Authorization не отправляется.
func doRequest(router *gin.Engine, method, path, token string, body any) *httptest.ResponseRecorder {
	var reader *bytes.Reader
	if body != nil {
		encoded, _ := json.Marshal(body)
		reader = bytes.NewReader(encoded)
	} else {
		reader = bytes.NewReader(nil)
	}

	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, req)
	return recorder
}

// decodeError разбирает тело ответа как нашу стандартную ошибку.
func decodeError(t *testing.T, recorder *httptest.ResponseRecorder) response.ErrorBody {
	t.Helper()

	var resp response.ErrorResponse
	require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp),
		"тело ответа должно быть в формате ErrorResponse, получено: %s", recorder.Body.String())
	return resp.Error
}

// ---------------------------------------------------------------------------
// Авторизация
// ---------------------------------------------------------------------------

func TestAuthMiddleware(t *testing.T) {
	router := newTestRouter(&fakeClientService{})

	tests := []struct {
		name       string
		authHeader string
		wantStatus int
	}{
		{name: "без заголовка", authHeader: "", wantStatus: http.StatusUnauthorized},
		{name: "неверный токен", authHeader: "Bearer wrong-token", wantStatus: http.StatusUnauthorized},
		{name: "без схемы Bearer", authHeader: validToken, wantStatus: http.StatusUnauthorized},
		{name: "пустой токен", authHeader: "Bearer ", wantStatus: http.StatusUnauthorized},
		// Регистр схемы по RFC 7235 не важен — должно работать.
		{name: "схема в нижнем регистре", authHeader: "bearer " + validToken, wantStatus: http.StatusOK},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/clients", nil)
			if tt.authHeader != "" {
				req.Header.Set("Authorization", tt.authHeader)
			}

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, req)

			assert.Equal(t, tt.wantStatus, recorder.Code)
		})
	}
}

// ---------------------------------------------------------------------------
// GET /clients/:id
// ---------------------------------------------------------------------------

func TestClientHandler_Get(t *testing.T) {
	clientID := uuid.New()

	t.Run("отдаёт карточку клиента", func(t *testing.T) {
		router := newTestRouter(&fakeClientService{
			Client: &model.Client{
				ID:       clientID,
				FullName: "Ivan Petrov",
				Email:    ptr("ivan@example.com"),
				Status:   model.ClientStatusActive,
			},
		})

		recorder := doRequest(router, http.MethodGet, "/clients/"+clientID.String(), validToken, nil)

		require.Equal(t, http.StatusOK, recorder.Code)

		var resp model.ClientResponse
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))
		assert.Equal(t, clientID, resp.ID)
		assert.Equal(t, "Ivan Petrov", resp.FullName)
	})

	t.Run("кривой UUID даёт 400, а не 500", func(t *testing.T) {
		router := newTestRouter(&fakeClientService{})

		recorder := doRequest(router, http.MethodGet, "/clients/not-a-uuid", validToken, nil)

		require.Equal(t, http.StatusBadRequest, recorder.Code)

		body := decodeError(t, recorder)
		assert.Equal(t, apperr.CodeValidation, body.Code)
		assert.Contains(t, body.Details, "id")
	})

	t.Run("отсутствующий клиент даёт 404 в нашем формате", func(t *testing.T) {
		router := newTestRouter(&fakeClientService{Err: apperr.NotFound("client")})

		recorder := doRequest(router, http.MethodGet, "/clients/"+clientID.String(), validToken, nil)

		require.Equal(t, http.StatusNotFound, recorder.Code)

		body := decodeError(t, recorder)
		assert.Equal(t, apperr.CodeNotFound, body.Code)
		// request_id обязан быть — по нему ищут запрос в логах.
		assert.NotEmpty(t, body.RequestID)
	})

	t.Run("неожиданная ошибка не раскрывает внутренности", func(t *testing.T) {
		// Сервис вернул «сырую» ошибку, не нашу доменную.
		router := newTestRouter(&fakeClientService{
			Err: errSecretInternal,
		})

		recorder := doRequest(router, http.MethodGet, "/clients/"+clientID.String(), validToken, nil)

		require.Equal(t, http.StatusInternalServerError, recorder.Code)

		body := decodeError(t, recorder)
		assert.Equal(t, apperr.CodeInternal, body.Code)
		// КЛЮЧЕВАЯ ПРОВЕРКА БЕЗОПАСНОСТИ: подробности не уехали клиенту.
		assert.NotContains(t, recorder.Body.String(), "pg_hba",
			"внутренняя ошибка не должна попадать в ответ клиенту")
		assert.Equal(t, "internal server error", body.Message)
	})
}

// errSecretInternal изображает «сырую» ошибку с чувствительными
// подробностями — такую, какую вернул бы драйвер базы.
var errSecretInternal = &testError{
	msg: `FATAL: no pg_hba.conf entry for host "10.0.3.17"`,
}

type testError struct{ msg string }

func (e *testError) Error() string { return e.msg }

// ---------------------------------------------------------------------------
// POST /clients — валидация
// ---------------------------------------------------------------------------

func TestClientHandler_Create_Validation(t *testing.T) {
	router := newTestRouter(&fakeClientService{Client: &model.Client{ID: uuid.New()}})

	tests := []struct {
		name       string
		body       map[string]any
		wantStatus int
		wantField  string
	}{
		{
			name:       "нет обязательного поля",
			body:       map[string]any{"email": "test@example.com"},
			wantStatus: http.StatusBadRequest,
			wantField:  "full_name", // имя из json-тега, а не FullName
		},
		{
			name:       "невалидный email",
			body:       map[string]any{"full_name": "Ivan", "email": "not-an-email"},
			wantStatus: http.StatusBadRequest,
			wantField:  "email",
		},
		{
			name:       "статус вне списка допустимых",
			body:       map[string]any{"full_name": "Ivan", "status": "deleted"},
			wantStatus: http.StatusBadRequest,
			wantField:  "status",
		},
		{
			name:       "корректные данные",
			body:       map[string]any{"full_name": "Ivan Petrov"},
			wantStatus: http.StatusCreated,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := doRequest(router, http.MethodPost, "/clients", validToken, tt.body)

			require.Equal(t, tt.wantStatus, recorder.Code, "тело: %s", recorder.Body.String())

			if tt.wantField == "" {
				return
			}

			body := decodeError(t, recorder)
			assert.Contains(t, body.Details, tt.wantField,
				"в ответе должно быть указано проблемное поле, получено: %v", body.Details)
		})
	}
}

// ---------------------------------------------------------------------------
// DELETE /clients/:id
// ---------------------------------------------------------------------------

func TestClientHandler_Delete(t *testing.T) {
	clientID := uuid.New()
	svc := &fakeClientService{}
	router := newTestRouter(svc)

	recorder := doRequest(router, http.MethodDelete, "/clients/"+clientID.String(), validToken, nil)

	// 204 No Content: удалили, возвращать нечего.
	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Empty(t, recorder.Body.String(), "при 204 тела быть не должно")

	// Идентификатор из пути дошёл до сервиса без искажений.
	assert.Equal(t, clientID, svc.DeletedID)
}

// ---------------------------------------------------------------------------
// GET /clients — список
// ---------------------------------------------------------------------------

func TestClientHandler_List(t *testing.T) {
	t.Run("пустой список отдаётся как [], а не null", func(t *testing.T) {
		router := newTestRouter(&fakeClientService{Clients: nil, Total: 0})

		recorder := doRequest(router, http.MethodGet, "/clients", validToken, nil)

		require.Equal(t, http.StatusOK, recorder.Code)
		// Клиенту всегда приходит массив — иначе фронтенду пришлось бы
		// проверять на null в каждом месте использования.
		assert.Contains(t, recorder.Body.String(), `"items":[]`)
	})

	t.Run("значения пагинации по умолчанию попадают в ответ", func(t *testing.T) {
		router := newTestRouter(&fakeClientService{
			Clients: []model.Client{{ID: uuid.New(), FullName: "Ivan"}},
			Total:   1,
		})

		recorder := doRequest(router, http.MethodGet, "/clients", validToken, nil)

		require.Equal(t, http.StatusOK, recorder.Code)

		var resp model.ListResponse[model.ClientResponse]
		require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &resp))

		assert.Equal(t, 20, resp.Pagination.Limit, "limit по умолчанию")
		assert.Equal(t, 0, resp.Pagination.Offset)
		assert.Equal(t, 1, resp.Pagination.Total)
		assert.Len(t, resp.Items, 1)
	})

	t.Run("limit больше максимума отвергается", func(t *testing.T) {
		router := newTestRouter(&fakeClientService{})

		// Без верхней границы клиент попросил бы миллион записей
		// и положил бы сервис. Проверка обязана быть.
		recorder := doRequest(router, http.MethodGet, "/clients?limit=1000000", validToken, nil)

		require.Equal(t, http.StatusBadRequest, recorder.Code)

		body := decodeError(t, recorder)
		assert.Contains(t, body.Details, "limit")
	})

	t.Run("сортировка по произвольной колонке отвергается", func(t *testing.T) {
		router := newTestRouter(&fakeClientService{})

		// Защита от SQL-инъекции: имя колонки нельзя передать
		// параметром запроса, поэтому список допустимых значений
		// жёстко ограничен.
		recorder := doRequest(router,
			http.MethodGet, "/clients?sort_by=password_hash", validToken, nil)

		require.Equal(t, http.StatusBadRequest, recorder.Code)

		body := decodeError(t, recorder)
		assert.Contains(t, body.Details, "sort_by")
	})
}

// ptr — указатель на значение.
func ptr[T any](v T) *T { return &v }
