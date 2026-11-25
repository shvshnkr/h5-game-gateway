package provider

import (
	"fmt"
	"os"
)

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// LoadConfig reads provider-mock settings. CallbackBase must point at the
// operator gateway's /seamless endpoints.
func LoadConfig() Config {
	return Config{
		Addr:            env("PROVIDER_ADDR", ":8082"),
		OperatorCode:    env("OPERATOR_CODE", "DEMO"),
		OperatorSecret:  env("OPERATOR_SECRET", "dev-operator-secret"),
		CallbackBase:    env("CALLBACK_BASE", "http://127.0.0.1:8081"),
		PublicBase:      env("PUBLIC_PROVIDER_BASE", ""),
		PGOperatorCode:  env("PGSOFT_OPERATOR_CODE", "PGDEMO"),
		PGOperatorToken: env("PGSOFT_OPERATOR_TOKEN", "pg-operator-token"),
		PGSecretKey:     env("PGSOFT_SECRET_KEY", "dev-pg-secret"),
	}
}

func (c Config) String() string {
	return fmt.Sprintf("provider(addr=%s operator=%s callback=%s)", c.Addr, c.OperatorCode, c.CallbackBase)
}
