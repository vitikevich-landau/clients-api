package handler

import (
	"github.com/gin-gonic/gin"

	"github.com/vitikevich-landau/clients-api/internal/middleware"
	"github.com/vitikevich-landau/clients-api/internal/model"
	"github.com/vitikevich-landau/clients-api/internal/response"
)

// ClientHandler — ручки справочника клиентов.
//
// Read-методы (List, Get) доступны любому аутентифицированному пользователю.
// Write-методы (Create, Update, Delete) регистрируются в админской группе
// роутов и защищены middleware.RequireAdmin — в самих методах проверки
// прав нет и быть не должно.
type ClientHandler struct {
	clients ClientService
}

// NewClientHandler создаёт хендлер клиентов.
func NewClientHandler(clients ClientService) *ClientHandler {
	return &ClientHandler{clients: clients}
}

// List отдаёт страницу клиентов.
//
//	@Summary		Список клиентов
//	@Description	Постраничная выдача с поиском по имени и компании,
//	@Description	фильтром по статусу и сортировкой.
//	@Tags			clients
//	@Produce		json
//	@Param			search		query		string	false	"Поиск по имени или компании"
//	@Param			status		query		string	false	"Фильтр по статусу"	Enums(active, archived)
//	@Param			limit		query		int		false	"Записей на страницу (1..100)"	default(20)
//	@Param			offset		query		int		false	"Сколько пропустить"			default(0)
//	@Param			sort_by		query		string	false	"Поле сортировки"	Enums(created_at, updated_at, full_name)	default(created_at)
//	@Param			sort_order	query		string	false	"Направление"		Enums(asc, desc)						default(desc)
//	@Success		200			{object}	model.ListResponse[model.ClientResponse]
//	@Failure		400			{object}	response.ErrorResponse
//	@Failure		401			{object}	response.ErrorResponse
//	@Security		BearerAuth
//	@Router			/clients [get]
func (h *ClientHandler) List(c *gin.Context) {
	var query model.ListClientsQuery
	if !bindQuery(c, &query) {
		return
	}

	clients, total, err := h.clients.List(c.Request.Context(), query)
	if err != nil {
		response.Error(c, err)
		return
	}

	// Значения по умолчанию сервис уже применил к своей копии структуры,
	// но наша копия осталась исходной — применяем и здесь, чтобы отдать
	// клиенту те limit/offset, с которыми выборка реально сделана.
	query.ApplyDefaults()

	response.OK(c, model.NewListResponse(
		model.NewClientResponses(clients),
		query.Limit, query.Offset, total,
	))
}

// Get отдаёт карточку одного клиента.
//
//	@Summary		Карточка клиента
//	@Tags			clients
//	@Produce		json
//	@Param			id	path		string	true	"UUID клиента"
//	@Success		200	{object}	model.ClientResponse
//	@Failure		400	{object}	response.ErrorResponse	"Некорректный UUID"
//	@Failure		401	{object}	response.ErrorResponse
//	@Failure		404	{object}	response.ErrorResponse	"Клиент не найден или удалён"
//	@Security		BearerAuth
//	@Router			/clients/{id} [get]
func (h *ClientHandler) Get(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	client, err := h.clients.GetByID(c.Request.Context(), id)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, model.NewClientResponse(client))
}

// Create заводит нового клиента. Только для администратора.
//
//	@Summary		Создать клиента
//	@Description	Доступно только администратору.
//	@Tags			admin
//	@Accept			json
//	@Produce		json
//	@Param			request	body		model.CreateClientRequest	true	"Данные клиента"
//	@Success		201		{object}	model.ClientResponse
//	@Failure		400		{object}	response.ErrorResponse
//	@Failure		401		{object}	response.ErrorResponse
//	@Failure		403		{object}	response.ErrorResponse	"Недостаточно прав"
//	@Failure		409		{object}	response.ErrorResponse	"Email уже занят другим клиентом"
//	@Security		BearerAuth
//	@Router			/admin/clients [post]
func (h *ClientHandler) Create(c *gin.Context) {
	var req model.CreateClientRequest
	if !bindJSON(c, &req) {
		return
	}

	// actor нужен сервису для записи в аудит-лог: кто именно создал.
	actor := middleware.MustGetAuthUser(c)

	client, err := h.clients.Create(c.Request.Context(), req, actor)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.Created(c, model.NewClientResponse(client))
}

// Update полностью заменяет данные клиента. Только для администратора.
//
//	@Summary		Изменить клиента
//	@Description	Доступно только администратору. PUT означает ПОЛНУЮ ЗАМЕНУ:
//	@Description	непереданные необязательные поля будут очищены.
//	@Tags			admin
//	@Accept			json
//	@Produce		json
//	@Param			id		path		string						true	"UUID клиента"
//	@Param			request	body		model.UpdateClientRequest	true	"Новые данные клиента"
//	@Success		200		{object}	model.ClientResponse
//	@Failure		400		{object}	response.ErrorResponse
//	@Failure		401		{object}	response.ErrorResponse
//	@Failure		403		{object}	response.ErrorResponse
//	@Failure		404		{object}	response.ErrorResponse
//	@Failure		409		{object}	response.ErrorResponse
//	@Security		BearerAuth
//	@Router			/admin/clients/{id} [put]
func (h *ClientHandler) Update(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	var req model.UpdateClientRequest
	if !bindJSON(c, &req) {
		return
	}

	actor := middleware.MustGetAuthUser(c)

	client, err := h.clients.Update(c.Request.Context(), id, req, actor)
	if err != nil {
		response.Error(c, err)
		return
	}

	response.OK(c, model.NewClientResponse(client))
}

// Delete мягко удаляет клиента. Только для администратора.
//
//	@Summary		Удалить клиента
//	@Description	Мягкое удаление: строка остаётся в базе с меткой deleted_at,
//	@Description	но исчезает из всех выдач.
//	@Tags			admin
//	@Param			id	path	string	true	"UUID клиента"
//	@Success		204	"Удалено, тела ответа нет"
//	@Failure		400	{object}	response.ErrorResponse
//	@Failure		401	{object}	response.ErrorResponse
//	@Failure		403	{object}	response.ErrorResponse
//	@Failure		404	{object}	response.ErrorResponse	"Уже удалён или не существует"
//	@Security		BearerAuth
//	@Router			/admin/clients/{id} [delete]
func (h *ClientHandler) Delete(c *gin.Context) {
	id, ok := parseUUIDParam(c, "id")
	if !ok {
		return
	}

	actor := middleware.MustGetAuthUser(c)

	if err := h.clients.Delete(c.Request.Context(), id, actor); err != nil {
		response.Error(c, err)
		return
	}

	// 204 No Content — стандартный ответ на успешное удаление:
	// всё прошло, возвращать нечего.
	response.NoContent(c)
}
