# AshCRM Notify for Windows. The release is built on any OS (pure Go, no cgo):
#   make build [SERVER=https://crm.example.ru — only prefills the address of the portal]
VERSION    := $(shell cat VERSION)
SERVER     ?=

LDFLAGS    := -s -w -H windowsgui -X main.version=$(VERSION) -X main.server=$(SERVER)

.PHONY: build test vet run stand-test version

version:
	@echo $(VERSION)

build: ## dist/AshCRM-Notify.exe (Windows 10/11, x64)
	GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o dist/AshCRM-Notify.exe ./cmd/crm-notify-windows
	@ls -l dist/AshCRM-Notify.exe

test: ## unit tests (the logic is OS-independent)
	go test -race ./...

vet: ## both builds: this OS and Windows
	go vet ./...
	GOOS=windows GOARCH=amd64 go vet ./...

run: ## console mode against a CRM (development; prints the cards)
	go run ./cmd/crm-notify-windows -server $(SERVER)

stand-test: ## the whole client against a running CRM: make stand-test SERVER=http://localhost:8780 PASSWORD=…
	@NOTIFY_SERVER=$(SERVER) NOTIFY_PASSWORD="$(PASSWORD)" go test -race -count=1 -tags stand -run TestAgainstStand -v ./internal/app/
