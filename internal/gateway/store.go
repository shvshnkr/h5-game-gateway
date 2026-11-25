package gateway

import (
	"context"
	"database/sql"
	"errors"
	"strings"

	"github.com/go-sql-driver/mysql"
)

// Store wraps MySQL access for players, wallets and the ledger.
//
// Design notes (the iGaming-relevant part):
//   - wallet balance lives in `wallets.balance` (int64 minor units) and is
//     always mutated inside a transaction with SELECT ... FOR UPDATE;
//   - every money movement appends a `ledger_entries` row with a UNIQUE
//     (scope, tx_id) index — provider transaction ids give us idempotency:
//     a replayed callback fails the insert and returns the current balance;
//   - `balance_after` snapshots make the ledger auditable.
type Store struct {
	DB *sql.DB
}

func OpenStore(dsn string) (*Store, error) {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	// connection pool sizing: gateway is IO-bound and thin
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(10)
	if err := db.Ping(); err != nil {
		return nil, err
	}
	return &Store{DB: db}, nil
}

var (
	ErrPlayerNotFound     = errors.New("player not found")
	ErrPlayerExists       = errors.New("player exists")
	ErrInsufficient       = errors.New("insufficient balance")
	ErrDuplicate          = errors.New("duplicate transaction")
	ErrWalletOverflow     = errors.New("balance overflow")
)

type Player struct {
	ID           int64
	Username     string
	PasswordHash string
	Currency     string
}

type Wallet struct {
	PlayerID int64
	Balance  int64
	Version  int64
	Currency string
}

const createPlayer = `
INSERT INTO players (username, password_hash, currency) VALUES (?, ?, ?)`

func (s *Store) CreatePlayer(ctx context.Context, username, passwordHash, currency string) (int64, error) {
	res, err := s.DB.ExecContext(ctx, createPlayer, username, passwordHash, currency)
	if isDuplicateErr(err) {
		return 0, ErrPlayerExists
	}
	if err != nil {
		return 0, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO wallets (player_id, balance) VALUES (?, 0)`, id)
	if err != nil {
		return 0, err
	}
	return id, nil
}

const selectPlayerByName = `
SELECT id, username, password_hash, currency FROM players WHERE username = ?`

func (s *Store) PlayerByName(ctx context.Context, username string) (*Player, error) {
	var p Player
	err := s.DB.QueryRowContext(ctx, selectPlayerByName, username).
		Scan(&p.ID, &p.Username, &p.PasswordHash, &p.Currency)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPlayerNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

func (s *Store) PlayerByID(ctx context.Context, id int64) (*Player, error) {
	var p Player
	err := s.DB.QueryRowContext(ctx, `SELECT id, username, password_hash, currency FROM players WHERE id = ?`, id).
		Scan(&p.ID, &p.Username, &p.PasswordHash, &p.Currency)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPlayerNotFound
	}
	if err != nil {
		return nil, err
	}
	return &p, nil
}

// WalletByPlayerID returns the wallet without locking (read-only paths).
func (s *Store) WalletByPlayerID(ctx context.Context, playerID int64) (*Wallet, error) {
	var w Wallet
	err := s.DB.QueryRowContext(ctx,
		`SELECT w.player_id, w.balance, w.version, p.currency
		 FROM wallets w JOIN players p ON p.id = w.player_id
		 WHERE w.player_id = ?`, playerID).
		Scan(&w.PlayerID, &w.Balance, &w.Version, &w.Currency)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, ErrPlayerNotFound
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// MoveMoney applies one money movement inside a single locked transaction.
//
//	scope  — grouping for idempotency (e.g. "provider", "payment-webhook")
//	txID   — unique transaction id within the scope (provider tx id, deposit id…)
//	delta  — signed amount in minor units (negative = debit)
//
// Returns the balance after the movement. If (scope, txID) was already
// applied the function returns ErrDuplicate and the caller should treat the
// operation as already-succeeded (idempotent replay).
func (s *Store) MoveMoney(ctx context.Context, playerID int64, scope, txID, entryType, ref string, delta int64) (int64, error) {
	tx, err := s.DB.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var balance, version int64
	err = tx.QueryRowContext(ctx,
		`SELECT balance, version FROM wallets WHERE player_id = ? FOR UPDATE`, playerID).
		Scan(&balance, &version)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, ErrPlayerNotFound
	}
	if err != nil {
		return 0, err
	}

	// idempotency check inside the same lock: if the tx was applied, report
	// duplicate without touching anything
	var seen int
	err = tx.QueryRowContext(ctx,
		`SELECT 1 FROM ledger_entries WHERE scope = ? AND tx_id = ?`, scope, txID).Scan(&seen)
	if err == nil {
		return balance, ErrDuplicate
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	newBalance, ok := addMoney(balance, delta)
	if !ok {
		return 0, ErrWalletOverflow
	}
	if newBalance < 0 {
		return 0, ErrInsufficient
	}

	res, err := tx.ExecContext(ctx,
		`UPDATE wallets SET balance = ?, version = version + 1 WHERE player_id = ? AND version = ?`,
		newBalance, playerID, version)
	if err != nil {
		return 0, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return 0, errors.New("wallet version conflict")
	}

	_, err = tx.ExecContext(ctx,
		`INSERT INTO ledger_entries (player_id, scope, tx_id, entry_type, amount, balance_after, ref)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		playerID, scope, txID, entryType, delta, newBalance, ref)
	if err != nil {
		if isDuplicateErr(err) {
			return balance, ErrDuplicate
		}
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return newBalance, nil
}

type StatsRow struct {
	EntryType string
	Count     int64
	Sum       int64
}

func (s *Store) Stats(ctx context.Context) ([]StatsRow, error) {
	rows, err := s.DB.QueryContext(ctx,
		`SELECT entry_type, COUNT(*), COALESCE(SUM(amount),0) FROM ledger_entries GROUP BY entry_type`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StatsRow
	for rows.Next() {
		var r StatsRow
		if err := rows.Scan(&r.EntryType, &r.Count, &r.Sum); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func addMoney(a, b int64) (int64, bool) {
	s := a + b
	if (b > 0 && s < a) || (b < 0 && s > a) {
		return 0, false
	}
	return s, true
}

func isDuplicateErr(err error) bool {
	if err == nil {
		return false
	}
	// MySQL error 1062 (duplicate entry) — the driver exposes it as
	// *mysql.MySQLError; fall back to message matching for tests.
	var myErr *mysql.MySQLError
	if errors.As(err, &myErr) {
		return myErr.Number == 1062
	}
	msg := err.Error()
	return strings.Contains(msg, "Error 1062") || strings.Contains(msg, "Duplicate entry")
}
