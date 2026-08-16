package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/vitikevich-landau/clients-api/internal/middleware"
	"github.com/vitikevich-landau/clients-api/internal/model"
	"github.com/vitikevich-landau/clients-api/internal/response"
)

// AuthHandler — ручки регистрации, входа и «кто я».
type AuthHandler struct {
	auth AuthService
}

// NewAuthHandler создаёт хендлер аутентификации.
func NewAuthHandler(auth AuthService) *AuthHandler {
	return &AuthHandler{auth: auth}
}

// Register регистрирует нового пользователя.
//
// Роль всегда user — см. пояснение в service.AuthService.Register.
//
//	@Summary		Регистрация нового пользователя
//	@Description	Создаёт пользователя с ролью user и сразу возвращает JWT.
//	@Description	Роль admin через этот эндпоинт получить нельзя.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			request	body		model.RegisterRequest	true	"Email и пароль"
//	@Success		201		{object}	model.AuthResponse
//	@Failure		400		{object}	response.ErrorResponse	"Не прошла валидация"
//	@Failure		409		{object}	response.ErrorResponse	"Email уже занят"
//	@Router			/auth/register [post]
func (h *AuthHandler) Register(c *gin.Context) {
	var req model.RegisterRequest
	if !bindJSON(c, &req) {
		return // ответ клиенту уже отправлен внутри bindJSON
	}

	// ВАЖНО: передаём c.Request.Context(), а НЕ c и не context.Background().
	//
	// Именно в этом контексте едут:
	//   - логгер с request_id (положил middleware.Logger);
	//   - дедлайн запроса (положил middleware.Timeout);
	//   - сигнал отмены, если клиент оборвал соединение.
	//
	// Передашь context.Background() — потеряешь всё сразу:
	// логи потеряют request_id, а таймаут перестанет работать.
	resp, err := h.auth.Register(c.Request.Context(), req)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.Created(c, resp)
}

// Login проверяет учётные данные и выдаёт токен.
//
//	@Summary		Вход в систему
//	@Description	Возвращает JWT для доступа к защищённым эндпоинтам.
//	@Tags			auth
//	@Accept			json
//	@Produce		json
//	@Param			request	body		model.LoginRequest	true	"Email и пароль"
//	@Success		200		{object}	model.AuthResponse
//	@Failure		400		{object}	response.ErrorResponse	"Не прошла валидация"
//	@Failure		401		{object}	response.ErrorResponse	"Неверный email или пароль"
//	@Router			/auth/login [post]
func (h *AuthHandler) Login(c *gin.Context) {
	var req model.LoginRequest
	if !bindJSON(c, &req) {
		return
	}

	resp, err := h.auth.Login(c.Request.Context(), req)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, resp)
}

// Me возвращает сведения о текущем пользователе.
//
// # Зачем ручка, если всё есть в токене
//
// Данные в токене — это снимок на момент выдачи. Роль могли поменять,
// пользователя могли удалить. Эта ручка идёт в БАЗУ и отдаёт актуальное
// состояние. Фронтенд обычно дёргает её при загрузке приложения, чтобы
// понять, действителен ли сохранённый токен вообще.
//
//	@Summary		Текущий пользователь
//	@Description	Возвращает актуальные данные владельца токена из базы.
//	@Tags			auth
//	@Produce		json
//	@Success		200	{object}	model.UserResponse
//	@Failure		401	{object}	response.ErrorResponse	"Нет или невалиден токен"
//	@Failure		404	{object}	response.ErrorResponse	"Пользователь удалён"
//	@Security		BearerAuth
//	@Router			/auth/me [get]
func (h *AuthHandler) Me(c *gin.Context) {
	// MustGetAuthUser безопасен: роут зарегистрирован в группе
	// с middleware.Auth, значит, пользователь в контексте гарантированно есть.
	actor := middleware.MustGetAuthUser(c)

	user, err := h.auth.GetByID(c.Request.Context(), actor.ID)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, model.NewUserResponse(user))
}
