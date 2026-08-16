package repository

import (
	"context"
	"fmt"
	"strings"

	"github.com/georgysavva/scany/v2/pgxscan"
	"github.com/google/uuid"

	"github.com/vitikevich-landau/clients-api/internal/apperr"
	"github.com/vitikevich-landau/clients-api/internal/db"
	"github.com/vitikevich-landau/clients-api/internal/model"
)

// ClientRepository — доступ к таблице clients.
type ClientRepository struct{}

// NewClientRepository создаёт репозиторий клиентов.
func NewClientRepository() *ClientRepository {
	return &ClientRepository{}
}

// Имя частичного уникального индекса из миграции 00002.
const constraintClientsEmailAlive = "ux_clients_email_alive"

// clientColumns — список колонок для SELECT.
//
// Вынесен в константу по двум причинам:
//   - не расползается копипастой по пяти запросам;
//   - явное перечисление вместо SELECT * — обязательное правило.
//
// Почему SELECT * — зло: добавили колонку миграцией, и запрос молча начал
// возвращать другое количество полей. В лучшем случае сломается скан,
// в худшем — утечёт что-нибудь лишнее в ответ API.
const clientColumns = `id, full_name, email, phone, company, notes,
	status, created_at, updated_at, deleted_at`

// clientSortColumns — БЕЛЫЙ СПИСОК колонок, по которым разрешена сортировка.
//
// ЭТО ЗАЩИТА ОТ SQL-ИНЪЕКЦИИ, И ОНА ОБЯЗАТЕЛЬНА.
//
// Имя колонки нельзя передать в запрос параметром ($1) — плейсхолдеры
// работают только для ЗНАЧЕНИЙ. Имя колонки приходится вклеивать в текст
// запроса. Значит, оно обязано браться из этой карты, а не из строки,
// пришедшей от клиента.
//
// Валидация `oneof=...` в DTO уже отсекает мусор, но полагаться на один
// рубеж защиты нельзя: кто-то поправит тег, и дыра откроется.
var clientSortColumns = map[string]string{
	"created_at": "created_at",
	"updated_at": "updated_at",
	"full_name":  "full_name",
}

// Create вставляет новую карточку клиента.
func (r *ClientRepository) Create(ctx context.Context, q db.Querier, c *model.Client) error {
	const query = `
		INSERT INTO clients (id, full_name, email, phone, company, notes, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		RETURNING created_at, updated_at`

	err := q.QueryRow(ctx, query,
		c.ID, c.FullName, c.Email, c.Phone, c.Company, c.Notes, c.Status,
	).Scan(&c.CreatedAt, &c.UpdatedAt)
	if err != nil {
		if db.IsUniqueViolation(err, constraintClientsEmailAlive) {
			return apperr.Conflict("client with this email already exists").Wrap(err)
		}
		return fmt.Errorf("insert client: %w", err)
	}

	return nil
}

// GetByID возвращает живую карточку клиента.
//
// Условие deleted_at IS NULL здесь ОБЯЗАТЕЛЬНО. Забыть его при мягком
// удалении — самая частая ошибка: удалённые записи начинают всплывать
// в выдаче, и никто долго не понимает, почему.
func (r *ClientRepository) GetByID(ctx context.Context, q db.Querier, id uuid.UUID) (*model.Client, error) {
	query := `SELECT ` + clientColumns + `
		FROM clients
		WHERE id = $1 AND deleted_at IS NULL`

	var c model.Client
	if err := pgxscan.Get(ctx, q, &c, query, id); err != nil {
		if pgxscan.NotFound(err) {
			return nil, apperr.NotFound("client")
		}
		return nil, fmt.Errorf("select client by id: %w", err)
	}

	return &c, nil
}

// GetByIDForUpdate — то же самое, но с блокировкой строки до конца транзакции.
//
// # Зачем FOR UPDATE
//
// Два администратора одновременно одобряют две разные правки одного клиента.
// Без блокировки оба прочитают ОДНО И ТО ЖЕ исходное состояние карточки,
// каждый наложит свои изменения на него и запишет результат.
// Тот, кто записал вторым, затрёт правку первого — классическая
// «потерянная запись» (lost update).
//
// FOR UPDATE заставляет второго дождаться, пока первый закоммитит,
// и работать уже с обновлёнными данными.
//
// Вызывать имеет смысл ТОЛЬКО внутри транзакции: вне её блокировка
// снимется сразу после запроса и никакой пользы не принесёт.
func (r *ClientRepository) GetByIDForUpdate(ctx context.Context, q db.Querier, id uuid.UUID) (*model.Client, error) {
	query := `SELECT ` + clientColumns + `
		FROM clients
		WHERE id = $1 AND deleted_at IS NULL
		FOR UPDATE`

	var c model.Client
	if err := pgxscan.Get(ctx, q, &c, query, id); err != nil {
		if pgxscan.NotFound(err) {
			return nil, apperr.NotFound("client")
		}
		return nil, fmt.Errorf("select client by id for update: %w", err)
	}

	return &c, nil
}

// Update перезаписывает изменяемые поля карточки.
//
// updated_at выставляется базой (now()), а не приходит из приложения —
// снова затем, чтобы источник времени был один.
func (r *ClientRepository) Update(ctx context.Context, q db.Querier, c *model.Client) error {
	const query = `
		UPDATE clients
		SET full_name = $2,
		    email     = $3,
		    phone     = $4,
		    company   = $5,
		    notes     = $6,
		    status    = $7,
		    updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL
		RETURNING updated_at`

	err := q.QueryRow(ctx, query,
		c.ID, c.FullName, c.Email, c.Phone, c.Company, c.Notes, c.Status,
	).Scan(&c.UpdatedAt)
	if err != nil {
		// RETURNING не вернул строку — значит, WHERE ничего не нашёл:
		// клиента либо не существует, либо он уже удалён.
		if pgxscan.NotFound(err) {
			return apperr.NotFound("client")
		}
		if db.IsUniqueViolation(err, constraintClientsEmailAlive) {
			return apperr.Conflict("client with this email already exists").Wrap(err)
		}
		return fmt.Errorf("update client: %w", err)
	}

	return nil
}

// SoftDelete помечает карточку удалённой.
//
// Физически строка остаётся: не рвутся ссылки из истории предложений,
// и удаление можно откатить одним UPDATE.
func (r *ClientRepository) SoftDelete(ctx context.Context, q db.Querier, id uuid.UUID) error {
	const query = `
		UPDATE clients
		SET deleted_at = now(),
		    updated_at = now()
		WHERE id = $1 AND deleted_at IS NULL`

	tag, err := q.Exec(ctx, query, id)
	if err != nil {
		return fmt.Errorf("soft delete client: %w", err)
	}

	// CommandTag сообщает, сколько строк затронуто.
	// Ноль означает, что клиента нет или он уже был удалён.
	// Без этой проверки повторное удаление молча возвращало бы 204,
	// и клиент API не отличил бы «удалил» от «нечего удалять».
	if tag.RowsAffected() == 0 {
		return apperr.NotFound("client")
	}

	return nil
}

// List возвращает страницу клиентов и общее количество подходящих записей.
//
// Возвращаются ДВА значения (срез и total), потому что клиенту нужно
// и содержимое страницы, и понимание, сколько всего страниц.
func (r *ClientRepository) List(
	ctx context.Context,
	q db.Querier,
	params model.ListClientsQuery,
) ([]model.Client, int, error) {
	// --- Сборка условий WHERE ---
	//
	// Условия и аргументы копятся параллельно. Номер плейсхолдера
	// ($1, $2, ...) всегда равен текущей длине среза аргументов —
	// поэтому добавлять их надо строго вместе.
	conditions := []string{"deleted_at IS NULL"}
	args := make([]any, 0, 4)

	if params.Search != "" {
		// ILIKE — регистронезависимое сравнение (специфика Postgres).
		// Обёртка %...% ищет подстроку в любом месте — под это в миграции
		// заведены GIN + pg_trgm индексы, иначе был бы полный перебор.
		args = append(args, "%"+params.Search+"%")
		conditions = append(conditions, fmt.Sprintf(
			"(full_name ILIKE $%d OR company ILIKE $%d)", len(args), len(args)))
	}

	if params.Status != "" {
		args = append(args, params.Status)
		conditions = append(conditions, fmt.Sprintf("status = $%d", len(args)))
	}

	where := strings.Join(conditions, " AND ")

	// --- Подсчёт общего количества ---
	//
	// Отдельный запрос. Считаем ДО применения LIMIT/OFFSET:
	// нам нужно, сколько записей подходит под фильтр вообще,
	// а не сколько влезло на страницу.
	var total int
	countQuery := `SELECT count(*) FROM clients WHERE ` + where
	if err := q.QueryRow(ctx, countQuery, args...).Scan(&total); err != nil {
		return nil, 0, fmt.Errorf("count clients: %w", err)
	}

	// Ранний выход: если под фильтр ничего не подходит, второй запрос
	// заведомо вернёт пустоту — не гоняем базу впустую.
	if total == 0 {
		return []model.Client{}, 0, nil
	}

	// --- Сортировка ---
	//
	// Колонку берём ИЗ БЕЛОГО СПИСКА, а не из пользовательской строки.
	sortColumn, ok := clientSortColumns[params.SortBy]
	if !ok {
		sortColumn = "created_at" // страховка, если DTO пропустил мусор
	}

	sortOrder := "DESC"
	if strings.EqualFold(params.SortOrder, "asc") {
		sortOrder = "ASC"
	}

	// --- Пагинация ---
	args = append(args, params.Limit)
	limitPlaceholder := len(args)
	args = append(args, params.Offset)
	offsetPlaceholder := len(args)

	// ВТОРИЧНАЯ СОРТИРОВКА ПО id — не украшательство, а необходимость.
	//
	// Если у двух записей одинаковый created_at, их взаимный порядок
	// в Postgres не определён и может отличаться от запроса к запросу.
	// При постраничном обходе это приводит к тому, что одна запись
	// показывается на двух страницах, а другая не показывается вообще.
	// Уникальная колонка вторым ключом делает порядок детерминированным.
	listQuery := fmt.Sprintf(`
		SELECT %s
		FROM clients
		WHERE %s
		ORDER BY %s %s, id ASC
		LIMIT $%d OFFSET $%d`,
		clientColumns, where, sortColumn, sortOrder,
		limitPlaceholder, offsetPlaceholder,
	)

	var clients []model.Client
	// pgxscan.Select читает МНОГО строк в срез структур.
	if err := pgxscan.Select(ctx, q, &clients, listQuery, args...); err != nil {
		return nil, 0, fmt.Errorf("select clients: %w", err)
	}

	return clients, total, nil
}

// Exists проверяет, что живой клиент с таким id существует.
// Дешевле, чем тянуть всю карточку, когда нужен только факт наличия.
func (r *ClientRepository) Exists(ctx context.Context, q db.Querier, id uuid.UUID) (bool, error) {
	const query = `SELECT EXISTS(
		SELECT 1 FROM clients WHERE id = $1 AND deleted_at IS NULL)`

	var exists bool
	if err := q.QueryRow(ctx, query, id).Scan(&exists); err != nil {
		return false, fmt.Errorf("check client existence: %w", err)
	}

	return exists, nil
}
