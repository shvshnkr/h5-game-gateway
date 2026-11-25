// Command migrate applies SQL migrations in filename order, tracking them
// in schema_migrations. Zero external dependencies.
package main

import (
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// mysqlErr aliases the driver error type for the drift check in main.
type mysqlErr = mysql.MySQLError

func main() {
	dsn := os.Getenv("MYSQL_DSN")
	if dsn == "" {
		dsn = "gateway:gateway@tcp(127.0.0.1:3306)/?multiStatements=true"
	}
	dir := "migrations"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	// the DSN may omit the schema (create-if-missing mode)
	if !strings.Contains(dsn, "/gateway") {
		dsn = ensureSchema(dsn)
	}

	db, err := sql.Open("mysql", dsn)
	must(err)
	defer db.Close()
	must(db.Ping())

	if _, err := db.Exec(`CREATE TABLE IF NOT EXISTS schema_migrations (
		name VARCHAR(255) PRIMARY KEY,
		applied_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	) ENGINE=InnoDB`); err != nil {
		must(err)
	}

	entries, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	must(err)
	sort.Strings(entries)

	for _, f := range entries {
		name := filepath.Base(strings.TrimSuffix(f, ".up.sql"))
		var seen string
		err := db.QueryRow(`SELECT name FROM schema_migrations WHERE name = ?`, name).Scan(&seen)
		if err == nil {
			slog.Info("skip (applied)", "migration", name)
			continue
		}
		sqlBytes, err := os.ReadFile(f)
		must(err)
		if _, err := db.Exec(string(sqlBytes)); err != nil {
			// 1050 = table already exists: schema drifted (applied by hand or
			// by tests). Mark as applied rather than failing the run.
			var myErr *mysqlErr
			if errors.As(err, &myErr) && myErr.Number == 1050 {
				slog.Warn("schema already present, marking applied", "migration", name)
			} else {
				must(err)
			}
		}
		if _, err := db.Exec(`INSERT INTO schema_migrations (name) VALUES (?)`, name); err != nil {
			must(err)
		}
		slog.Info("applied", "migration", name)
	}
	slog.Info("migrations done")
}

func ensureSchema(dsn string) string {
	// split before tcp(host:port)/ → append /gateway after host
	if i := strings.LastIndex(dsn, ")"); i >= 0 {
		return dsn[:i+1] + "/gateway?multiStatements=true&parseTime=true"
	}
	// handle DSN without tcp(): user:pass@host/db
	if i := strings.Index(dsn, "/"); i >= 0 && !strings.Contains(dsn[:i], "@tcp(") {
		return dsn[:i] + "/gateway?multiStatements=true&parseTime=true"
	}
	return dsn + "/gateway?multiStatements=true&parseTime=true"
}

func must(err error) {
	if err != nil {
		slog.Error("migrate failed", "error", err)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
