GO := "go"
GOLANGCI_LINT := "golangci-lint"
LINT_CONFIG := "lint-config/.golangci.yml"
GOVULNCHECK := "govulncheck"

# help
default:
    @just --list

# установка dev-зависимостей
install:
    @echo "📦 Установка зависимостей Go..."
    {{GO}} mod tidy
    @echo "🔧 Установка инструментов разработки..."
    {{GO}} install github.com/golangci/golangci-lint/cmd/golangci-lint@latest
    {{GO}} install golang.org/x/vuln/cmd/govulncheck@latest
    {{GO}} install github.com/evilmartians/lefthook/v2@latest
    @echo "✅ Готово. Запусти 'just lefthook-install' для установки хуков."

# линтинг всех модулей
lint:
    @echo "🔍 Запуск линтера..."
    {{GOLANGCI_LINT}} run --config {{LINT_CONFIG}} ./...

# линтинг всех модулей с автоисправлением
lint-fix:
    @echo "🔧 Запуск линтера с автоисправлением..."
    {{GOLANGCI_LINT}} run --fix --config {{LINT_CONFIG}} ./...

# линтинг только изменённых файлов
lint-changed:
    @echo "🔍 Проверка только изменённых файлов..."
    {{GOLANGCI_LINT}} run --fix --config {{LINT_CONFIG}} --new-from-rev HEAD~1 ./...

# запуск тестов
test:
    @echo "🧪 Запуск тестов..."
    {{GO}} test -race -count=1 -shuffle=on ./...

# запуск тестов с покрытием
test-cover:
    @echo "📊 Запуск тестов с покрытием..."
    {{GO}} test -race -count=1 -coverprofile=coverage.out ./...
    {{GO}} tool cover -html=coverage.out -o coverage.html
    @echo "✅ Отчёт сохранён в coverage.html"

# запуск тестов модуля
test-pkg pkg:
    @echo "🧪 Тестирование {{pkg}}..."
    {{GO}} test -race -count=1 {{pkg}}

# проверка уязвимостей
vulncheck:
    @echo "🛡️ Проверка уязвимостей..."
    {{GOVULNCHECK}} ./...

# сборка
build:
    @echo "🏗️ Сборка..."
    {{GO}} build -o bin/example ./example
    @echo "✅ Бинарник: bin/example"

# форматирование кода
fmt:
    @echo "✨ Форматирование кода..."
    {{GO}} fmt ./...

# очистка
clean:
    @echo "🧹 Очистка..."
    rm -f coverage.out coverage.html
    rm -rf bin/
    {{GO}} clean -cache

# установка git-хуков через lefthook
lefthook-install:
    @echo "🔗 Установка Git-хуков через Lefthook..."
    lefthook install

# запуск pre-commit хука
lefthook-pre-commit:
    @echo "🧪 Запуск Lefthook pre-commit..."
    lefthook run pre-commit
# запуск pre-push хука
lefthook-pre-push:
    @echo "🧪 Запуск Lefthook pre-push..."
    lefthook run pre-push

# запуск линтинга, тестов и поиска уязвимостей
all-checks: lint test vulncheck
    @echo "✅ Все проверки пройдены!"

# помощь
help:
    @just --list
