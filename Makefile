# Everyday commands. Run `make` on its own to see them.

.DEFAULT_GOAL := help
.PHONY: help demo demo-docker demo-stop run test race vet fmt check build image clean

BINARY  := dinners
IMAGE   := dinners-rudbeckia
COMPOSE := docker compose -f docker-compose.demo.yml

help: ## Show this help
	@grep -hE '^[a-z-]+:.*?## ' $(MAKEFILE_LIST) \
		| awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[1m%-12s\033[0m %s\n", $$1, $$2}'

demo: ## Try the site out: a season already under way, password "demo", needs only Go
	@echo "→ http://localhost:8080   password: demo   admin: admin"
	@DB_PATH=$${DB_PATH:-data/demo.db} go run ./cmd/server -demo

demo-docker: ## Same, but in a container, with nothing installed but Docker
	@echo "→ http://localhost:8080   password: demo   admin: admin"
	@$(COMPOSE) up --build

demo-stop: ## Stop and remove the demo container
	@$(COMPOSE) down

run: ## Run against your own config.yaml (set DINNER_PASSWORD first)
	go run ./cmd/server

test: ## Run the tests
	go test ./...

race: ## Run the tests with the race detector
	go test -race -count=1 ./...

vet: ## Static checks
	go vet ./...

fmt: ## Format every Go file
	gofmt -w .

check: fmt vet race ## Format, vet and test — run this before pushing
	@go run ./cmd/server -check-config

build: ## Build the binary into ./dinners
	CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o $(BINARY) ./cmd/server

image: ## Build the container image locally, tagged :local and :latest
	docker build -t $(IMAGE):local -t $(IMAGE):latest .
	@docker images --format '  {{.Repository}}:{{.Tag}}  {{.Size}}' $(IMAGE)

clean: ## Remove build output and the local databases
	rm -rf $(BINARY) data/
