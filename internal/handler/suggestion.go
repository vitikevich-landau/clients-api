package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/vitikevich-landau/clients-api/internal/middleware"
	"github.com/vitikevich-landau/clients-api/internal/model"
	"github.com/vitikevich-landau/clients-api/internal/response"
)

// SuggestionHandler — ручки механики модерации правок.
type SuggestionHandler struct {
	suggestions SuggestionService
}

// NewSuggestionHandler создаёт хендлер предложений.
func NewSuggestionHandler(suggestions SuggestionService) *SuggestionHandler {
	return &SuggestionHandler{suggestions: suggestions}
}

// Create регистрирует предложение правки карточки клиента.
//
// # Формат тела запроса
//
// Тело — это НАПРЯМУЮ предлагаемые изменения, без обёртки:
//
//	{"phone": "+7 900 111-22-33", "notes": null}
//
// Такая семантика описана стандартом JSON Merge Patch (RFC 7396):
//
//	поле есть со значением → поменять на это значение
//	поле есть со значением null → очистить
//	поля нет вовсе → не трогать
//
// Различать последние два случая позволяет тип model.Optional —
// обычный указатель на это не способен.
//
//	@Summary		Предложить правку клиента
//	@Description	Карточка клиента НЕ меняется. Создаётся предложение
//	@Description	со статусом pending, которое рассмотрит администратор.
//	@Description	Тело — частичное изменение по правилам JSON Merge Patch:
//	@Description	значение = заменить, null = очистить, отсутствие ключа = не трогать.
//	@Tags			suggestions
//	@Accept			json
//	@Produce		json
//	@Param			id		path		string					true	"UUID клиента"
//	@Param			request	body		model.SuggestionPayload	true	"Предлагаемые изменения"
//	@Success		201		{object}	model.SuggestionResponse
//	@Failure		400		{object}	response.ErrorResponse	"Пустой или невалидный payload"
//	@Failure		401		{object}	response.ErrorResponse
//	@Failure		404		{object}	response.ErrorResponse	"Клиент не найден"
//	@Failure		409		{object}	response.ErrorResponse	"У вас уже есть нерассмотренное предложение по этому клиенту"
//	@Security		BearerAuth
//	@Router			/clients/{id}/suggestions [post]
func (h *SuggestionHandler) Create(c *gin.Context) {
	clientID, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	var payload model.SuggestionPayload
	if !bindJSON(c, &payload) {
		return
	}

	author := middleware.MustGetAuthUser(c)

	suggestion, err := h.suggestions.Create(c.Request.Context(), clientID, author, payload)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.Created(c, model.NewSuggestionDetailedResponse(suggestion))
}

// ListMy отдаёт предложения текущего пользователя.
//
//	@Summary		Мои предложения
//	@Description	Показывает только предложения, созданные текущим пользователем,
//	@Description	вместе с их статусами и комментариями модератора.
//	@Tags			suggestions
//	@Produce		json
//	@Param			status		query		string	false	"Фильтр по статусу"	Enums(pending, approved, rejected)
//	@Param			client_id	query		string	false	"Фильтр по клиенту (UUID)"
//	@Param			limit		query		int		false	"Записей на страницу (1..100)"	default(20)
//	@Param			offset		query		int		false	"Сколько пропустить"			default(0)
//	@Success		200			{object}	model.ListResponse[model.SuggestionResponse]
//	@Failure		400			{object}	response.ErrorResponse
//	@Failure		401			{object}	response.ErrorResponse
//	@Security		BearerAuth
//	@Router			/suggestions/my [get]
func (h *SuggestionHandler) ListMy(c *gin.Context) {
	var query model.ListSuggestionsQuery
	if !bindQuery(c, &query) {
		return
	}

	actor := middleware.MustGetAuthUser(c)

	// Передаём идентификатор автора — сервис добавит его в WHERE,
	// и чужие строки просто не покинут базу.
	//
	// Обрати внимание: идентификатор берётся ИЗ ТОКЕНА, а не из параметра
	// запроса. Если бы автор приходил параметром, любой пользователь
	// подставил бы чужой id и прочитал чужие предложения.
	// Всё, что определяет права, берётся из токена и только из токена.
	authorID := actor.ID

	items, total, err := h.suggestions.List(c.Request.Context(), query, &authorID)
	if err != nil {
		response.Error(c, err)
		return
	}

	query.ApplyDefaults()

	response.OK(c, model.NewListResponse(
		model.NewSuggestionDetailedResponses(items),
		query.Limit, query.Offset, total,
	))
}

// ListAll отдаёт предложения всех пользователей. Только для администратора.
//
//	@Summary		Очередь модерации
//	@Description	Все предложения всех авторов. Доступно только администратору.
//	@Description	Обычный сценарий: ?status=pending — что ждёт рассмотрения.
//	@Tags			admin
//	@Produce		json
//	@Param			status		query		string	false	"Фильтр по статусу"	Enums(pending, approved, rejected)
//	@Param			client_id	query		string	false	"Фильтр по клиенту (UUID)"
//	@Param			limit		query		int		false	"Записей на страницу (1..100)"	default(20)
//	@Param			offset		query		int		false	"Сколько пропустить"			default(0)
//	@Success		200			{object}	model.ListResponse[model.SuggestionResponse]
//	@Failure		400			{object}	response.ErrorResponse
//	@Failure		401			{object}	response.ErrorResponse
//	@Failure		403			{object}	response.ErrorResponse
//	@Security		BearerAuth
//	@Router			/admin/suggestions [get]
func (h *SuggestionHandler) ListAll(c *gin.Context) {
	var query model.ListSuggestionsQuery
	if !bindQuery(c, &query) {
		return
	}

	// nil вместо идентификатора автора = админский режим, видно всё.
	items, total, err := h.suggestions.List(c.Request.Context(), query, nil)
	if err != nil {
		response.Error(c, err)
		return
	}

	query.ApplyDefaults()

	response.OK(c, model.NewListResponse(
		model.NewSuggestionDetailedResponses(items),
		query.Limit, query.Offset, total,
	))
}

// Get отдаёт одно предложение.
//
// Обычный пользователь видит только свои, админ — любые.
// Проверку делает сервис (и отвечает 404, а не 403 — см. пояснение там).
//
//	@Summary		Одно предложение
//	@Description	Обычный пользователь видит только свои предложения.
//	@Tags			suggestions
//	@Produce		json
//	@Param			id	path		string	true	"UUID предложения"
//	@Success		200	{object}	model.SuggestionResponse
//	@Failure		400	{object}	response.ErrorResponse
//	@Failure		401	{object}	response.ErrorResponse
//	@Failure		404	{object}	response.ErrorResponse	"Не найдено либо принадлежит другому пользователю"
//	@Security		BearerAuth
//	@Router			/suggestions/{id} [get]
func (h *SuggestionHandler) Get(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	actor := middleware.MustGetAuthUser(c)

	suggestion, err := h.suggestions.GetByID(c.Request.Context(), id, actor)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, model.NewSuggestionDetailedResponse(suggestion))
}

// Approve одобряет предложение и применяет его к карточке клиента.
//
//	@Summary		Одобрить предложение
//	@Description	Атомарно применяет payload к карточке клиента и переводит
//	@Description	предложение в статус approved. Только для администратора.
//	@Tags			admin
//	@Accept			json
//	@Produce		json
//	@Param			id		path		string							true	"UUID предложения"
//	@Param			request	body		model.ApproveSuggestionRequest	false	"Необязательный комментарий"
//	@Success		200		{object}	model.SuggestionResponse
//	@Failure		400		{object}	response.ErrorResponse
//	@Failure		401		{object}	response.ErrorResponse
//	@Failure		403		{object}	response.ErrorResponse
//	@Failure		404		{object}	response.ErrorResponse	"Предложение не найдено"
//	@Failure		409		{object}	response.ErrorResponse	"Уже рассмотрено, клиент удалён или email занят"
//	@Security		BearerAuth
//	@Router			/admin/suggestions/{id}/approve [post]
func (h *SuggestionHandler) Approve(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	// Тело здесь НЕОБЯЗАТЕЛЬНО: одобрить можно молча.
	// ShouldBindJSON на пустом теле вернёт io.EOF, поэтому используем
	// не bindJSON (он бы ответил 400), а ручной разбор с игнорированием
	// ошибки. Валидацию длины комментария при этом теряем — поэтому
	// проверяем её ниже вручную.
	var req model.ApproveSuggestionRequest
	_ = c.ShouldBindJSON(&req)

	const maxCommentLen = 1000
	if req.Comment != nil && len(*req.Comment) > maxCommentLen {
		response.ValidationError(c, "request validation failed", map[string]string{
			"comment": "must be at most 1000 characters long",
		})
		return
	}

	reviewer := middleware.MustGetAuthUser(c)

	suggestion, err := h.suggestions.Approve(c.Request.Context(), id, reviewer, req.Comment)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, model.NewSuggestionDetailedResponse(suggestion))
}

// Reject отклоняет предложение. Карточка клиента не меняется.
//
//	@Summary		Отклонить предложение
//	@Description	Переводит предложение в статус rejected. Комментарий
//	@Description	обязателен — автор должен понимать причину отказа.
//	@Description	Только для администратора.
//	@Tags			admin
//	@Accept			json
//	@Produce		json
//	@Param			id		path		string							true	"UUID предложения"
//	@Param			request	body		model.RejectSuggestionRequest	true	"Причина отказа"
//	@Success		200		{object}	model.SuggestionResponse
//	@Failure		400		{object}	response.ErrorResponse	"Не указан комментарий"
//	@Failure		401		{object}	response.ErrorResponse
//	@Failure		403		{object}	response.ErrorResponse
//	@Failure		404		{object}	response.ErrorResponse
//	@Failure		409		{object}	response.ErrorResponse	"Уже рассмотрено"
//	@Security		BearerAuth
//	@Router			/admin/suggestions/{id}/reject [post]
func (h *SuggestionHandler) Reject(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	// Здесь тело ОБЯЗАТЕЛЬНО: комментарий помечен как required.
	var req model.RejectSuggestionRequest
	if !bindJSON(c, &req) {
		return
	}

	reviewer := middleware.MustGetAuthUser(c)

	suggestion, err := h.suggestions.Reject(c.Request.Context(), id, reviewer, req.Comment)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, model.NewSuggestionDetailedResponse(suggestion))
}
