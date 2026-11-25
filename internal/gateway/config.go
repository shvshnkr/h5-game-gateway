package gateway

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config holds all gateway runtime settings, provided via environment
// variables with sane local defaults.
type Config struct {
	Addr          string        // GATEWAY_ADDR
	MySQLDSN      string        // MYSQL_DSN
	RedisAddr     string        // REDIS_ADDR
	JWTSecret     string        // JWT_SECRET
	AdminToken    string        // ADMIN_TOKEN
	OperatorCode  string        // OPERATOR_CODE
	OperatorSecret string       // OPERATOR_SECRET
	ProviderBase  string        // PROVIDER_BASE (internal http url of provider-mock)
	WebDir        string        // WEB_DIR (serve PWA directly in dev; nginx does it in compose)
	ReadTimeout   time.Duration // GATEWAY_READ_TIMEOUT_SEC

	// Provider integration style: "gsc" (GSC+-style seamless wallet, the
	// default) or "pgsoft" (PG Soft agent API: GetLaunchURLHTML +
	// Cash/Get + Cash/TransferInOut callbacks).
	ProviderStyle string // PROVIDER_STYLE

	// PG Soft agent credentials (what a real merchant account provides).
	PGSoftOperatorToken string // PGSOFT_OPERATOR_TOKEN
	PGSoftSecretKey     string // PGSOFT_SECRET_KEY
	PGSoftHashAuth      bool   // PGSOFT_HASH_AUTH (PWS-HMAC-SHA256 headers)
	PGSoftHashSalt      string // PGSOFT_HASH_SALT
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func LoadConfig() Config {
	readSec, _ := strconv.Atoi(env("GATEWAY_READ_TIMEOUT_SEC", "10"))
	style := env("PROVIDER_STYLE", "gsc")
	if style != "gsc" && style != "pgsoft" {
		style = "gsc"
	}
	hashAuth := strings.EqualFold(env("PGSOFT_HASH_AUTH", "false"), "true")
	return Config{
		Addr:           env("GATEWAY_ADDR", ":8081"),
		MySQLDSN:       env("MYSQL_DSN", "gateway:gateway@tcp(127.0.0.1:3306)/gateway?parseTime=true&multiStatements=true"),
		RedisAddr:      env("REDIS_ADDR", "127.0.0.1:6379"),
		JWTSecret:      env("JWT_SECRET", "dev-jwt-secret"),
		AdminToken:     env("ADMIN_TOKEN", "dev-admin-token"),
		OperatorCode:   env("OPERATOR_CODE", "DEMO"),
		OperatorSecret: env("OPERATOR_SECRET", "dev-operator-secret"),
		ProviderBase:   env("PROVIDER_BASE", "http://127.0.0.1:8082"),
		WebDir:         env("WEB_DIR", ""),
		ReadTimeout:    time.Duration(readSec) * time.Second,
		ProviderStyle:  style,

		PGSoftOperatorToken: env("PGSOFT_OPERATOR_TOKEN", "pg-operator-token"),
		PGSoftSecretKey:     env("PGSOFT_SECRET_KEY", "dev-pg-secret"),
		PGSoftHashAuth:      hashAuth,
		PGSoftHashSalt:      env("PGSOFT_HASH_SALT", "dev-pg-salt"),
	}
}

// DSNName returns the database name part of the DSN, used by the migrator.
func (c Config) DBName() string {
	var name string
	// gateway:user@tcp(host)/dbname?params
	for i := 0; i < len(c.MySQLDSN); i++ {
		if c.MySQLDSN[i] == '/' && (i == 0 || c.MySQLDSN[i-1] != '@') {
			rest := c.MySQLDSN[i+1:]
			for j := 0; j < len(rest); j++ {
				if rest[j] == '?' {
					break
				}
				name += string(rest[j])
			}
			break
		}
	}
	if name == "" {
		panic(fmt.Sprintf("cannot parse db name from DSN %q", c.MySQLDSN))
	}
	return name
}
