-- players: minimal identity for the demo
CREATE TABLE players (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  username      VARCHAR(64) NOT NULL UNIQUE,
  password_hash VARCHAR(255) NOT NULL,
  currency      CHAR(3) NOT NULL DEFAULT 'USD',
  created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- wallets: one row per player, int64 minor units (scale 1e4)
CREATE TABLE wallets (
  player_id BIGINT UNSIGNED PRIMARY KEY,
  balance   BIGINT NOT NULL DEFAULT 0,
  version   BIGINT NOT NULL DEFAULT 0,
  CONSTRAINT fk_wallet_player FOREIGN KEY (player_id) REFERENCES players(id)
) ENGINE=InnoDB;

-- ledger: append-only, unique (scope, tx_id) gives idempotency for
-- provider transaction replays; balance_after makes it auditable
CREATE TABLE ledger_entries (
  id            BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  player_id     BIGINT UNSIGNED NOT NULL,
  scope         VARCHAR(32) NOT NULL,          -- provider | payment
  tx_id         VARCHAR(128) NOT NULL,
  entry_type    VARCHAR(16) NOT NULL,          -- deposit | withdraw | bet | win
  amount        BIGINT NOT NULL,               -- signed, minor units
  balance_after BIGINT NOT NULL,
  ref           VARCHAR(255) NULL,
  created_at    TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  UNIQUE KEY uq_ledger_tx (scope, tx_id),
  KEY idx_ledger_player (player_id, created_at),
  CONSTRAINT fk_ledger_player FOREIGN KEY (player_id) REFERENCES players(id)
) ENGINE=InnoDB;

-- deposits opened by the player, confirmed by the payment webhook
CREATE TABLE deposits (
  id           BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  player_id    BIGINT UNSIGNED NOT NULL,
  amount_minor BIGINT NOT NULL,
  status       VARCHAR(16) NOT NULL DEFAULT 'pending', -- pending|confirmed|failed
  created_at   TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  confirmed_at TIMESTAMP NULL,
  CONSTRAINT fk_deposit_player FOREIGN KEY (player_id) REFERENCES players(id)
) ENGINE=InnoDB;

-- raw provider audit pushes (pushbetdata), stored as-is
CREATE TABLE bet_pushes (
  id             BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
  member_account VARCHAR(64) NOT NULL,
  payload        JSON NULL,
  created_at     TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
  KEY idx_push_account (member_account)
) ENGINE=InnoDB;
