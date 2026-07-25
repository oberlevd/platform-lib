package logger

import (
	"log/slog"
	"regexp"
	"strings"
)

// baseRedactKeys — набор имён полей, которые считаются чувствительными
// по умолчанию во всех сервисах платформы. Сравнение регистронезависимое
// и по подстроке (см. redactor.matches), чтобы ловить варианты вроде
// "AccessToken", "access_token", "user_password" и т.д.
var baseRedactKeys = []string{
	"password",
	"passwd",
	"secret",
	"token",
	"authorization",
	"api_key",
	"apikey",
	"private_key",
	"connection_string",
	"conn_str",
}

const redactedPlaceholder = "***REDACTED***"

// valuePatterns ловит секреты, спрятанные ВНУТРИ значения под безобидным
// ключом — например, кто-то залогирует целиком DSN, сырой SQL или JSON-тело
// ответа от внешнего сервиса под ключом "query"/"dsn"/"error"/"response",
// и внутри окажется "password=..." или `"password":"..."`. Матчинг по
// имени ключа (redactor.matches) в этом случае не сработает, поэтому это
// отдельный, более грубый уровень защиты: ищем в строковых значениях
// характерные последовательности и маскируем только сам секрет, оставляя
// остальной текст читаемым.
//
// Два формата на входе:
//   - "key=value" (DSN, query-string, connection string) — kvPattern.
//   - `"key":"value"` (сериализованный JSON, например залогированное
//     целиком тело ответа внешнего API) — jsonPattern.
//
// Это защита "на всякий случай", а не замена дисциплины — не логируйте
// сырые connection string, SQL с параметрами или JSON-тела целиком, если
// можно этого избежать.
var valuePatterns = []*regexp.Regexp{
	// key=value, разделители по краям — ; & пробел или конец строки.
	regexp.MustCompile(`(?i)(password|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)\s*=\s*[^;&\s"']+`),
	// "key":"value" — сериализованный JSON с тем же секретом внутри строки.
	regexp.MustCompile(`(?i)"(password|pwd|secret|token|api[_-]?key|access[_-]?key|private[_-]?key)"\s*:\s*"[^"]*"`),
}

func redactValuePatterns(s string) string {
	for _, re := range valuePatterns {
		s = re.ReplaceAllStringFunc(s, func(match string) string {
			// Для key=value: маскируем всё после первого "=".
			if idx := strings.IndexByte(match, '='); idx != -1 && !strings.Contains(match, `":`) {
				return match[:idx+1] + redactedPlaceholder
			}
			// Для "key":"value": сохраняем "key": и открывающую/закрывающую
			// кавычку значения, маскируем содержимое между ними.
			if idx := strings.Index(match, `":`); idx != -1 {
				return match[:idx+2] + `"` + redactedPlaceholder + `"`
			}
			return redactedPlaceholder
		})
	}
	return s
}

type redactor struct {
	keys []string
}

func newRedactor(extra []string) *redactor {
	keys := make([]string, 0, len(baseRedactKeys)+len(extra))
	keys = append(keys, baseRedactKeys...)
	for _, k := range extra {
		keys = append(keys, strings.ToLower(k))
	}
	return &redactor{keys: keys}
}

func (r *redactor) matches(key string) bool {
	lk := strings.ToLower(key)
	for _, k := range r.keys {
		if strings.Contains(lk, k) {
			return true
		}
	}
	return false
}

// replaceAttr используется как часть slog.HandlerOptions.ReplaceAttr.
// Маскирует значение атрибута, если имя ключа похоже на чувствительное —
// в этом случае всё значение целиком заменяется плейсхолдером. Если имя
// ключа не совпало, но значение — строка, дополнительно прогоняем её
// через valuePatterns на случай встроенного секрета (см. комментарий
// к valuePatterns). Обходит вложенные группы (slog.Group), т.к. в них
// тоже могут быть секреты (например, domain_fields.db_password).
func (r *redactor) replaceAttr(_ []string, a slog.Attr) slog.Attr {
	if r.matches(a.Key) {
		return slog.String(a.Key, redactedPlaceholder)
	}
	if a.Value.Kind() == slog.KindString {
		original := a.Value.String()
		if sanitized := redactValuePatterns(original); sanitized != original {
			return slog.String(a.Key, sanitized)
		}
	}
	return a
}
