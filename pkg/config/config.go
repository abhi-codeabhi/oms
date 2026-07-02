// Package config loads 12-factor configuration from environment variables using
// reflection over `env` and `default` struct tags. It works anywhere there is
// an env (Render, ECS, Kubernetes) with no provider-specific code.
package config

import (
	"fmt"
	"os"
	"reflect"
	"strconv"
)

// Base is the configuration every service shares. Services embed it in their
// own config struct and add fields with the same tag conventions.
type Base struct {
	Port         string `env:"PORT" default:"8080"`
	DatabaseURL  string `env:"DATABASE_URL"`
	NatsURL      string `env:"NATS_URL"`
	JWTPubKey    string `env:"JWT_PUBLIC_KEY"`
	OTLPEndpoint string `env:"OTEL_EXPORTER_OTLP_ENDPOINT"`
	Env          string `env:"APP_ENV" default:"dev"`
}

// Load reflects over T's fields, reading each `env` tag from the environment and
// falling back to the `default` tag. Supported field kinds: string, bool, and
// the signed/unsigned integer kinds.
func Load[T any]() (T, error) {
	var out T
	v := reflect.ValueOf(&out).Elem()
	t := v.Type()
	if t.Kind() != reflect.Struct {
		return out, fmt.Errorf("config: Load expects a struct, got %s", t.Kind())
	}
	if err := loadStruct(v); err != nil {
		return out, err
	}
	return out, nil
}

func loadStruct(v reflect.Value) error {
	t := v.Type()
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fv := v.Field(i)

		// Recurse into embedded/nested structs (e.g. an embedded Base).
		if fv.Kind() == reflect.Struct && field.Tag.Get("env") == "" {
			if err := loadStruct(fv); err != nil {
				return err
			}
			continue
		}
		if !fv.CanSet() {
			continue
		}
		envKey := field.Tag.Get("env")
		if envKey == "" {
			continue
		}
		val, ok := os.LookupEnv(envKey)
		if !ok || val == "" {
			val = field.Tag.Get("default")
		}
		if val == "" {
			continue
		}
		if err := setField(fv, val); err != nil {
			return fmt.Errorf("config: field %s (%s): %w", field.Name, envKey, err)
		}
	}
	return nil
}

func setField(fv reflect.Value, val string) error {
	switch fv.Kind() {
	case reflect.String:
		fv.SetString(val)
	case reflect.Bool:
		b, err := strconv.ParseBool(val)
		if err != nil {
			return err
		}
		fv.SetBool(b)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		n, err := strconv.ParseInt(val, 10, 64)
		if err != nil {
			return err
		}
		fv.SetInt(n)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		n, err := strconv.ParseUint(val, 10, 64)
		if err != nil {
			return err
		}
		fv.SetUint(n)
	default:
		return fmt.Errorf("unsupported kind %s", fv.Kind())
	}
	return nil
}
