package service

import (
	"context"
	"log/slog"
	"strings"

	"github.com/google/uuid"

	"github.com/vitikevich-landau/clients-api/internal/logger"
	"github.com/vitikevich-landau/clients-api/internal/model"
)

// ClientService — работа со справочником клиентов.
//
// Все методы этого сервиса, кроме List и GetByID, вызываются только
// из админских хендлеров. Саму ПРОВЕРКУ ПРАВ выполняет middleware
// на уровне группы роутов, а не каждый метод по отдельности.
//
// Почему так: проверка в middleware не может быть случайно забыта
// при добавлении нового роута в защищённую группу. Проверка внутри
// каждого метода — может, и именно так появляются дыры.
type ClientService struct {
	clients ClientRepository
	tx      TxRunner
}

// NewClientService собирает сервис клиентов.
func NewClientService(clients ClientRepository, tx TxRunner) *ClientService {
	return &ClientService{clients: clients, tx: tx}
}

// List возвращает страницу клиентов и общее количество подходящих записей.
//
// Доступно всем аутентифицированным пользователям — и обычным, и админам.
func (s *ClientService) List(
	ctx context.Context,
	params model.ListClientsQuery,
) ([]model.Client, int, error) {
	// Значения по умолчанию проставляем здесь, а не в хендлере:
	// так они применятся при любом способе вызова сервиса,
	// включая вызов из теста или из другой точки входа.
	params.ApplyDefaults()
	params.Search = strings.TrimSpace(params.Search)

	// Обычный SELECT — транзакция не нужна, работаем через пул напрямую.
	return s.clients.List(ctx, s.tx.DB(), params)
}

// GetByID возвращает карточку клиента.
func (s *ClientService) GetByID(ctx context.Context, id uuid.UUID) (*model.Client, error) {
	return s.clients.GetByID(ctx, s.tx.DB(), id)
}

// Create заводит нового клиента. Только для администратора.
func (s *ClientService) Create(
	ctx context.Context,
	req model.CreateClientRequest,
	actor model.AuthUser,
) (*model.Client, error) {
	log := logger.FromContext(ctx)

	// Статус по умолчанию, если его не передали.
	status := model.ClientStatusActive
	if req.Status != nil {
		status = *req.Status
	}

	client := &model.Client{
		ID:       uuid.New(),
		FullName: strings.TrimSpace(req.FullName),
		Email:    normalizeOptionalEmail(req.Email),
		Phone:    trimOptional(req.Phone),
		Company:  trimOptional(req.Company),
		Notes:    req.Notes, // заметки не тримим: переносы строк осмысленны
		Status:   status,
	}

	if err := s.clients.Create(ctx, s.tx.DB(), client); err != nil {
		return nil, err
	}

	// Логируем ФАКТ действия и кто его совершил — это аудит.
	// Содержимое карточки (имя, телефон клиента) в лог не пишем:
	// это персональные данные.
	log.Info("client created",
		slog.String("client_id", client.ID.String()),
		slog.String("actor_id", actor.ID.String()),
	)

	return client, nil
}

// Update полностью заменяет изменяемые поля карточки. Только для администратора.
//
// Семантика PUT — полная замена: не передал email, значит, email очищается.
// Частичное изменение в этом сервисе делается через механизм предложений.
func (s *ClientService) Update(
	ctx context.Context,
	id uuid.UUID,
	req model.UpdateClientRequest,
	actor model.AuthUser,
) (*model.Client, error) {
	log := logger.FromContext(ctx)

	// Читаем текущее состояние, чтобы:
	//   1) отличить «нет такого клиента» (404) от «нечего менять»;
	//   2) вернуть клиенту API полную карточку с актуальными created_at.
	client, err := s.clients.GetByID(ctx, s.tx.DB(), id)
	if err != nil {
		return nil, err
	}

	status := model.ClientStatusActive
	if req.Status != nil {
		status = *req.Status
	}

	client.FullName = strings.TrimSpace(req.FullName)
	client.Email = normalizeOptionalEmail(req.Email)
	client.Phone = trimOptional(req.Phone)
	client.Company = trimOptional(req.Company)
	client.Notes = req.Notes
	client.Status = status

	if err := s.clients.Update(ctx, s.tx.DB(), client); err != nil {
		return nil, err
	}

	log.Info("client updated",
		slog.String("client_id", client.ID.String()),
		slog.String("actor_id", actor.ID.String()),
	)

	return client, nil
}

// Delete мягко удаляет карточку. Только для администратора.
//
// Предложения по этому клиенту НЕ трогаем: они остаются в истории
// со своими статусами. Создать новое предложение уже не выйдет —
// репозиторий не найдёт живого клиента.
func (s *ClientService) Delete(ctx context.Context, id uuid.UUID, actor model.AuthUser) error {
	log := logger.FromContext(ctx)

	if err := s.clients.SoftDelete(ctx, s.tx.DB(), id); err != nil {
		return err
	}

	log.Info("client soft-deleted",
		slog.String("client_id", id.String()),
		slog.String("actor_id", actor.ID.String()),
	)

	return nil
}

// ---------------------------------------------------------------------------
// Вспомогательные функции
// ---------------------------------------------------------------------------

// trimOptional убирает пробелы по краям и превращает ставшую пустой
// строку в nil.
//
// Зачем последнее: пустая строка и NULL в базе — разные значения,
// и частичный уникальный индекс по email их различает. Пусть в базе
// будет честный NULL, а не строка нулевой длины.
func trimOptional(s *string) *string {
	if s == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*s)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

// normalizeOptionalEmail приводит необязательный email к нижнему регистру.
//
// Уникальный индекс в базе построен по lower(email). Если складывать
// туда адреса в разном регистре, индекс отработает правильно, но в выдаче
// один и тот же адрес будет выглядеть по-разному. Приводим сразу.
func normalizeOptionalEmail(s *string) *string {
	trimmed := trimOptional(s)
	if trimmed == nil {
		return nil
	}
	lowered := strings.ToLower(*trimmed)
	return &lowered
}
