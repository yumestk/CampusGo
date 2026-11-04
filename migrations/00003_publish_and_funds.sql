-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE wallet_account (
  id BIGINT NOT NULL PRIMARY KEY,
  owner_id BIGINT NOT NULL,
  owner_type VARCHAR(16) NOT NULL,
  available BIGINT NOT NULL DEFAULT 0,
  frozen BIGINT NOT NULL DEFAULT 0,
  version BIGINT NOT NULL DEFAULT 0,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uk_wallet_owner (owner_id,owner_type),
  CONSTRAINT ck_wallet_balance CHECK (available >= 0 AND frozen >= 0 AND version >= 0),
  CONSTRAINT ck_wallet_owner CHECK (id > 0 AND ((owner_type='USER' AND owner_id > 0) OR (owner_type='ESCROW' AND owner_id=-1) OR (owner_type='COMMISSION' AND owner_id=-2)))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE wallet_ledger (
  id BIGINT NOT NULL PRIMARY KEY,
  biz_no VARCHAR(64) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  account_id BIGINT NOT NULL,
  user_id BIGINT NOT NULL,
  direction VARCHAR(8) NOT NULL,
  amount BIGINT NOT NULL,
  balance_after BIGINT NOT NULL,
  account_version BIGINT NOT NULL,
  ref_type VARCHAR(24) NOT NULL,
  ref_id BIGINT NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uk_ledger_business (biz_no,account_id,direction),
  UNIQUE KEY uk_ledger_sequence (account_id,account_version),
  KEY idx_ledger_ref (ref_type,ref_id),
  CONSTRAINT fk_ledger_account FOREIGN KEY (account_id) REFERENCES wallet_account(id),
  CONSTRAINT ck_ledger_values CHECK (id > 0 AND user_id > 0 AND amount > 0 AND balance_after >= 0 AND account_version > 0 AND ref_id > 0),
  CONSTRAINT ck_ledger_direction CHECK (direction IN ('DEBIT','CREDIT')),
  CONSTRAINT ck_ledger_ref CHECK (ref_type IN ('OPENING','ESCROW'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE escrow_order (
  id BIGINT NOT NULL PRIMARY KEY,
  campus_id BIGINT NOT NULL,
  errand_id BIGINT NOT NULL,
  publisher_id BIGINT NOT NULL,
  amount BIGINT NOT NULL,
  status VARCHAR(16) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uk_escrow_errand (errand_id),
  KEY idx_escrow_publisher (publisher_id,id),
  CONSTRAINT fk_escrow_errand FOREIGN KEY (errand_id) REFERENCES errand(id),
  CONSTRAINT ck_escrow_values CHECK (id > 0 AND campus_id > 0 AND publisher_id > 0 AND amount > 0),
  CONSTRAINT ck_escrow_status CHECK (status IN ('HELD','RELEASED','REFUNDED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE publish_request (
  publisher_id BIGINT NOT NULL,
  request_key CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  fingerprint BINARY(32) NOT NULL,
  state VARCHAR(16) NOT NULL,
  errand_id BIGINT NULL,
  reward_amount BIGINT NOT NULL,
  error_code VARCHAR(32) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (publisher_id,request_key),
  UNIQUE KEY uk_publish_errand (errand_id),
  CONSTRAINT fk_publish_errand FOREIGN KEY (errand_id) REFERENCES errand(id),
  CONSTRAINT ck_publish_values CHECK (publisher_id > 0 AND reward_amount > 0),
  CONSTRAINT ck_publish_state CHECK ((state='PROCESSING' AND errand_id IS NULL AND error_code='') OR (state='SUCCEEDED' AND errand_id IS NOT NULL AND error_code='') OR (state='REJECTED' AND errand_id IS NULL AND error_code IN ('INSUFFICIENT_BALANCE','BALANCE_LIMIT','ACCOUNT_NOT_FOUND')))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

INSERT INTO wallet_account (id,owner_id,owner_type) VALUES (1,-1,'ESCROW'),(2,-2,'COMMISSION');

-- +goose Down
DROP TABLE publish_request;
DROP TABLE escrow_order;
DROP TABLE wallet_ledger;
DROP TABLE wallet_account;
