# lint-config

Единый `.golangci.yml` для всех Go-репозиториев платформы.

## Как подключить в сервисе

1. Добавить зависимость (появится в `go.mod` сервиса, версия пинится
   как у любого другого пакета):

```bash
go get github.com/oberlevd/lint-config@v1.2.0
```

2. В `Makefile` сервиса — таргет, который находит путь до конфига
   через `go list -m` и передаёт его в `-c`:

```justfile
GOLANGCI_LINT_VERSION := v2.1.6

install-linter:
    @echo "🔧 Установка golangci-lint {{GOLANGCI_LINT_VERSION}}..."
    {{GO}} install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@{{GOLANGCI_LINT_VERSION}}
```
