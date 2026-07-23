// Package config даёт единый способ загрузки конфигурации сервиса из
// переменных окружения через struct tags — без завязки на конкретный
// секрет-провайдер (Vault, SOPS и т.д.). Секреты сюда попадают уже как
// значения ENV-переменных: как они туда доставляются (Vault agent
// injector, k8s Secret → env, SOPS-decrypt на старте пода) — вопрос
// деплоя, а не этого пакета. Такое разделение позволяет менять
// секрет-провайдера, не трогая код сервисов.
//
// Пример структуры конфига сервиса:
//
//	type Config struct {
//	    MSSQLHost     string         `env:"MSSQL_HOST,required"`
//	    MSSQLPassword string         `env:"MSSQL_PASSWORD,required" redact:"true"`
//	    HTTPPort      int            `env:"HTTP_PORT" default:"8080"`
//	    RequestTimeout time.Duration `env:"REQUEST_TIMEOUT" default:"5s"`
//	    Debug         bool           `env:"DEBUG" default:"false"`
//	}
//
//	var cfg Config
//	if err := config.Load(&cfg); err != nil {
//	    log.Fatal(err)
//	}
//
// Структурные значения (map, slice, вложенные struct) через отдельные
// скалярные ENV-переменные не разложить — для них есть тег
// `env_json:"true"`: значение переменной интерпретируется как JSON и
// анмаршалится прямо в поле. Например, для роутинга по нескольким
// MSSQL-хостам:
//
//	type Config struct {
//	    MSSQLRoutes map[string]string `env:"MSSQL_ROUTES" env_json:"true"`
//	}
//
//	MSSQL_ROUTES='{"orders":"mssql-orders-01","billing":"mssql-billing-02"}'
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"time"
)

// Load заполняет поля структуры, на которую указывает target, значениями
// из переменных окружения согласно тегам `env`. target должен быть
// указателем на структуру.
//
// Поддерживаемые теги на поле:
//   - `env:"NAME"`          — имя переменной окружения.
//   - `env:"NAME,required"` — Load вернёт ошибку, если переменная не задана.
//   - `default:"value"`     — значение по умолчанию, если переменная не задана
//     и не помечена как required.
//   - `env_json:"true"`     — значение переменной парсится как JSON и
//     анмаршалится в поле напрямую (encoding/json.Unmarshal). Нужен для
//     map/slice/struct-полей, которые не выразить одной скалярной строкой.
//
// Поддерживаемые типы полей без env_json: string, int, int64, bool,
// time.Duration, float64. С env_json — любой тип, для которого валиден
// json.Unmarshal (map, slice, struct, указатель и т.д.). Для остальных
// случаев Load вернёт ошибку с указанием поля — это лучше, чем молча
// проигнорировать поле, которое разработчик забыл сюда добавить.
func Load(target any) error {
	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("config: Load expects a pointer to a struct, got %T", target)
	}

	elem := v.Elem()
	t := elem.Type()

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fieldValue := elem.Field(i)

		envTag, ok := field.Tag.Lookup("env")
		if !ok {
			continue // поле без env-тега — не наша забота, пропускаем
		}

		name, required := parseEnvTag(envTag)
		jsonMode := field.Tag.Get("env_json") == "true"

		raw, present := os.LookupEnv(name)
		if !present {
			if required {
				return fmt.Errorf("config: required environment variable %q is not set (field %s)", name, field.Name)
			}
			if def, hasDefault := field.Tag.Lookup("default"); hasDefault {
				raw = def
				present = true
			}
		}

		if !present {
			continue // необязательное поле без значения и без default — оставляем zero value
		}

		if err := setField(fieldValue, raw, jsonMode); err != nil {
			return fmt.Errorf("config: field %s (env %q): %w", field.Name, name, err)
		}
	}

	return nil
}

func parseEnvTag(tag string) (name string, required bool) {
	name = tag
	for i := 0; i < len(tag); i++ {
		if tag[i] == ',' {
			name = tag[:i]
			if tag[i+1:] == "required" {
				required = true
			}
			break
		}
	}
	return name, required
}

func setField(fieldValue reflect.Value, raw string, jsonMode bool) error {
	if jsonMode {
		if !fieldValue.CanAddr() {
			return fmt.Errorf("field is not addressable, cannot unmarshal JSON into it")
		}
		if err := json.Unmarshal([]byte(raw), fieldValue.Addr().Interface()); err != nil {
			return fmt.Errorf("invalid JSON %q: %w", raw, err)
		}
		return nil
	}

	switch fieldValue.Interface().(type) {
	case time.Duration:
		d, err := time.ParseDuration(raw)
		if err != nil {
			return fmt.Errorf("invalid duration %q: %w", raw, err)
		}
		fieldValue.Set(reflect.ValueOf(d))
		return nil
	}

	switch fieldValue.Kind() {
	case reflect.String:
		fieldValue.SetString(raw)
	case reflect.Int, reflect.Int64:
		n, err := strconv.ParseInt(raw, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid int %q: %w", raw, err)
		}
		fieldValue.SetInt(n)
	case reflect.Bool:
		b, err := strconv.ParseBool(raw)
		if err != nil {
			return fmt.Errorf("invalid bool %q: %w", raw, err)
		}
		fieldValue.SetBool(b)
	case reflect.Float64:
		f, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			return fmt.Errorf("invalid float %q: %w", raw, err)
		}
		fieldValue.SetFloat(f)
	default:
		return fmt.Errorf("unsupported field type %s (use `env_json:\"true\"` for map/slice/struct fields)", fieldValue.Kind())
	}

	return nil
}
