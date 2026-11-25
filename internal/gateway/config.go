package gateway

import (
	"fmt"
	"os"
	"strconv"
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
}

func env(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func LoadConfig() Config {
	readSec, _ := strconv.Atoi(env("GATEWAY_READ_TIMEOUT_SEC", "10"))
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
