# platform-lib

Не фреймворк и не монолитный `SDK`, а отдельные пакеты с едиными соглашениями: конфиг из `ENV`, `JSON`-логи, `gRPC middleware`, `health-check`, `graceful shutdown`, `RED`-метрики и подключение к `MSSQL`. Секреты в код не вшиваются - только переменные окружения (`Vault`, `k8s Secret`, `SOPS` - на стороне деплоя).

| Пакет       | Зачем                                                                         |
| ----------- | ----------------------------------------------------------------------------- |
| config      | "ENV → struct (tags, defaults, required, JSON-поля, redact вложенные struct)" |
| logger      | "slog JSON в stdout, service/version/env, request_id, маскировка секретов"    |
| metrics     | стандартные RED-метрики gRPC для Prometheus                                   |
| grpcmw      | "unary/stream interceptors: request_id, лог, метрики, recovery"               |
| healthcheck | "/healthz, /readyz, gRPC health (Check)"                                      |
| lifecycle   | SIGTERM/SIGINT → упорядоченный shutdown                                       |
| mssql       | "пул, retry на старте, SafeDSN, checker для readiness"                        |
| mongo       | "клиент, retry на старте, SafeURI, checker для readiness (аналог mssql)"      |
| lint-config | общий .golangci.yml                                                           |
| example     | "минимальный сервис, склеивающий всё вместе"                                  |

Цель - один раз зафиксировать "как у нас принято" (логи, метрики, readiness, shutdown), чтобы сервисы не копировали boilerplate и оставались взаимозаменяемыми в дашбордах и деплое
