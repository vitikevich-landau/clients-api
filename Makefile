# Makefile — точка входа для всех команд проекта.
#
# Зачем он нужен: команды вроде
#   go build -trimpath -ldflags="-s -w -X main.version=$(git rev-parse --short HEAD)" ...
# невозможно помнить наизусть. Make превращает их в `make build`,
# документирует и делает одинаковыми для всех участников проекта и для CI.
#
# Список доступных команд: make help (или просто make)

# .PHONY объявляет цели, которые не создают файл с таким именем.
# Без этого `make test` не сработает, если в каталоге появится файл "test":
# make решит, что цель уже собрана.
.PHONY: help up down restart logs ps build run test test-cover lint fmt vet \
        tidy swag migrate-up migrate-down migrate-status migrate-create psql clean

# Цель по умолчанию — та, что стоит первой.
.DEFAULT_GOAL := help

# ---------------------------------------------------------------------------
# Переменные
# ---------------------------------------------------------------------------

# Версия для сборки: короткий хеш коммита, либо "dev" вне git-репозитория.
VERSION ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "dev")

# Строка подключения для goose при запуске миграций С ХОСТА
# (не изнутри контейнера — поэтому localhost).
DB_DSN ?= postgres://clients:clients_dev_password@localhost:5432/clients?sslmode=disable

MIGRATIONS_DIR := migrations
BINARY := bin/api

# ---------------------------------------------------------------------------
# Справка
# ---------------------------------------------------------------------------

help: ## Показать список команд
	@echo "Clients API — доступные команды:"
	@echo ""
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-18s\033[0m %s\n", $$1, $$2}'
	@echo ""

# ---------------------------------------------------------------------------
# Docker
# ---------------------------------------------------------------------------

up: ## Поднять Postgres и сервис (миграции применятся сами)
	docker compose up -d --build
	@echo ""
	@echo "  API:     http://localhost:8080"
	@echo "  Swagger: http://localhost:8080/swagger/index.html"
	@echo "  Health:  http://localhost:8080/healthz"
	@echo ""
	@echo "  Логи:    make logs"

down: ## Остановить всё (данные в томе сохраняются)
	docker compose down

clean: ## Остановить всё и УДАЛИТЬ данные базы
	docker compose down -v
	rm -rf $(BINARY) coverage.out coverage.html

restart: ## Перезапустить только сервис (база не трогается)
	docker compose restart api

logs: ## Смотреть логи сервиса в реальном времени
	docker compose logs -f api

ps: ## Статус контейнеров
	docker compose ps

psql: ## Открыть psql внутри контейнера базы
	docker compose exec postgres psql -U clients -d clients

# ---------------------------------------------------------------------------
# Сборка и запуск без Docker
# ---------------------------------------------------------------------------

build: ## Собрать бинарник в bin/api
	@mkdir -p bin
	go build -trimpath -ldflags="-s -w -X main.version=$(VERSION)" -o $(BINARY) ./cmd/api
	@echo "Собрано: $(BINARY) (версия $(VERSION))"

run: ## Запустить локально (нужен .env и поднятый Postgres)
	go run ./cmd/api

# ---------------------------------------------------------------------------
# Тесты и проверки
# ---------------------------------------------------------------------------

test: ## Прогнать тесты
	go test -race ./...

test-cover: ## Тесты с отчётом о покрытии (открыть coverage.html)
	go test -race -coverprofile=coverage.out -covermode=atomic ./...
	go tool cover -html=coverage.out -o coverage.html
	@go tool cover -func=coverage.out | tail -1
	@echo "Отчёт: coverage.html"

# Флаг -race включает детектор гонок. Он замедляет тесты в 2-10 раз,
# но находит ошибки многопоточности, которые иначе всплывают
# раз в месяц в проде и невоспроизводимы.
# Заодно -race автоматически включает checkptr — проверку указателей.

vet: ## Статический анализ стандартным go vet
	go vet ./...

lint: ## Полный линтер (нужен golangci-lint)
	golangci-lint run ./...

fmt: ## Отформатировать код
	go fmt ./...
	gofmt -s -w .

tidy: ## Привести go.mod/go.sum в порядок
	go mod tidy

# ---------------------------------------------------------------------------
# Документация
# ---------------------------------------------------------------------------

swag: ## Перегенерировать Swagger из комментариев в коде
	swag init -g cmd/api/main.go -o docs --parseDependency --parseInternal
	@echo "Документация обновлена: docs/"

# ---------------------------------------------------------------------------
# Миграции
# ---------------------------------------------------------------------------
#
# Сервис применяет миграции сам при старте. Эти команды нужны для
# ручного управления: посмотреть статус, откатить, создать новую.

migrate-up: ## Применить все миграции вручную
	goose -dir $(MIGRATIONS_DIR) postgres "$(DB_DSN)" up

migrate-down: ## Откатить ПОСЛЕДНЮЮ миграцию
	goose -dir $(MIGRATIONS_DIR) postgres "$(DB_DSN)" down

migrate-status: ## Показать, какие миграции применены
	goose -dir $(MIGRATIONS_DIR) postgres "$(DB_DSN)" status

migrate-create: ## Создать новую миграцию: make migrate-create name=add_field
	@test -n "$(name)" || (echo "Укажи имя: make migrate-create name=add_something" && exit 1)
	goose -dir $(MIGRATIONS_DIR) create $(name) sql
