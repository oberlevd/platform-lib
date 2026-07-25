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
//
// Вложенные struct-поля БЕЗ собственного `env`-тега обрабатываются
// рекурсивно: Load и Redacted спускаются в них и работают с их полями
// по тем же правилам. Это нужно, чтобы конфиг сервиса собирался из
// переиспользуемых кусков других пакетов платформы без дублирования
// полей, например:
//
//	type Config struct {
//	    GitSHA string      `env:"GIT_SHA" default:"unknown"`
//	    MSSQL  mssql.Config // без своего env-тега — обработается рекурсивно
//	}
//
// Единственное исключение — типы вроде time.Duration, у которых Kind()
// возвращает Int64, а не Struct, поэтому под рекурсию они не попадают
// (и не должны — у них уже есть отдельная обработка через ParseDuration).
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"strconv"
	"time"
)

// redactedPlaceholder — чем заменяется значение поля, помеченного
// `redact:"true"`, в выводе Redacted().
const redactedPlaceholder = "***REDACTED***"

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
// json.Unmarshal. Поле-структура без собственного env-тега обрабатывается
// рекурсивно (см. комментарий пакета). Для остальных случаев Load вернёт
// ошибку с указанием поля — это лучше, чем молча проигнорировать поле,
// которое разработчик забыл сюда добавить.
func Load(target any) error {
	v := reflect.ValueOf(target)
	if v.Kind() != reflect.Pointer || v.Elem().Kind() != reflect.Struct {
		return fmt.Errorf("config: Load expects a pointer to a struct, got %T", target)
	}
	return loadStruct(v.Elem())
}

// loadStruct обрабатывает одну структуру (возможно, вложенную).
func loadStruct(elem reflect.Value) error {
	t := elem.Type()

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue // неэкспортируемое поле нельзя ни прочитать через тег
			// осмысленно, ни установить через reflect — пропускаем, а не
			// падаем с паникой на SetString/SetInt.
		}
		fieldValue := elem.Field(i)

		envTag, ok := field.Tag.Lookup("env")
		if !ok {
			// Поле без своего env-тега: если это вложенная структура
			// (например, mssql.Config, встроенный или именованный кусок
			// конфига другого пакета платформы), спускаемся в неё
			// рекурсивно — её поля со своими env-тегами обработаются на
			// следующем уровне. time.Duration и подобные "структуры
			// поверх примитива" сюда не попадают: их Kind() — Int64,
			// а не Struct.
			if fieldValue.Kind() == reflect.Struct && fieldValue.CanAddr() {
				if err := loadStruct(fieldValue); err != nil {
					return err
				}
			}
			continue
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

// Redacted возвращает представление конфига в виде map[envName]value,
// пригодное для одного лог-вызова при старте сервиса:
//
//	log.Info(ctx, "config loaded", "config", config.Redacted(&cfg))
//
// Поля, помеченные `redact:"true"`, заменяются плейсхолдером.
// Вложенные struct-поля без собственного env-тега обрабатываются
// рекурсивно, как и в Load — так, например, MSSQL_PASSWORD внутри
// вложенного mssql.Config тоже попадёт в вывод (замаскированным).
//
// target — указатель на ту же структуру, что передавалась в Load (или
// любая struct с `env`-тегами на полях). Поля без `env`-тега (и не
// являющиеся вложенной структурой) в вывод не попадают. Для полей с
// `env_json:"true"` в вывод попадает Go-представление значения через
// fmt.Sprintf("%v", ...), а не исходная JSON-строка — так удобнее для
// логов.
func Redacted(target any) map[string]string {
	result := make(map[string]string)

	v := reflect.ValueOf(target)
	if v.Kind() == reflect.Ptr {
		if v.IsNil() {
			return result
		}
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return result
	}

	collectRedacted(v, result)
	return result
}

func collectRedacted(v reflect.Value, result map[string]string) {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		if !field.IsExported() {
			continue
		}
		fieldValue := v.Field(i)

		envTag, ok := field.Tag.Lookup("env")
		if !ok {
			if fieldValue.Kind() == reflect.Struct {
				collectRedacted(fieldValue, result)
			}
			continue
		}
		name, _ := parseEnvTag(envTag)

		if field.Tag.Get("redact") == "true" {
			result[name] = redactedPlaceholder
			continue
		}

		result[name] = fmt.Sprintf("%v", fieldValue.Interface())
	}
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
