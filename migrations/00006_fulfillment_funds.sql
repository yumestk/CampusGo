-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE wallet_ledger DROP CHECK ck_ledger_ref,
 ADD CONSTRAINT ck_ledger_ref CHECK (ref_type IN ('OPENING','ESCROW','SETTLE','REFUND'));
ALTER TABLE credit_event DROP CHECK ck_credit_event,
 ADD CONSTRAINT ck_credit_event CHECK (round>=0 AND ((kind='GRAB_TIMEOUT_REVERT' AND delta=-5) OR (kind='SETTLE' AND delta=2) OR (kind='DISPUTE_LOSE' AND delta=-8)));
CREATE INDEX idx_credit_window ON credit_event(user_id,created_at);
CREATE INDEX idx_errand_delivered_due ON errand(status,delivered_at,id);
CREATE TABLE fund_request (
 request_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 actor_id BIGINT NOT NULL,
 campus_id BIGINT NOT NULL,
 errand_id BIGINT NOT NULL,
 fingerprint BINARY(32) NOT NULL,
 payload JSON NOT NULL,
 state VARCHAR(16) NOT NULL DEFAULT 'PREPARED',
 error_code VARCHAR(40) NOT NULL DEFAULT '',
 expires_at DATETIME(3) NOT NULL,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 committed_at DATETIME(3) NULL,
 recovery_state VARCHAR(16) NOT NULL DEFAULT 'PENDING',
 recovery_attempts INT NOT NULL DEFAULT 0,
 recovery_token CHAR(36) NULL,
 recovery_until DATETIME(3) NULL,
 next_recovery_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 replay_count INT NOT NULL DEFAULT 0,
 replay_reason VARCHAR(200) NULL,
 UNIQUE KEY uk_fund_actor_key (actor_id,request_id),
 KEY idx_fund_recovery (state,recovery_state,next_recovery_at),
 KEY idx_fund_expiry (state,expires_at),
 KEY idx_fund_task (errand_id),
 CONSTRAINT fk_fund_task FOREIGN KEY (errand_id) REFERENCES errand(id),
 CONSTRAINT ck_fund_state CHECK (state IN ('PREPARED','COMMITTED','ABORTED')),
 CONSTRAINT ck_fund_recovery CHECK (recovery_state IN ('PENDING','RUNNING','DONE','DEAD') AND recovery_attempts>=0 AND replay_count>=0),
 CONSTRAINT ck_fund_values CHECK (campus_id>0 AND errand_id>0 AND (actor_id>0 OR actor_id=-1))
) ENGINE=InnoDB;
CREATE TABLE fund_event_receipt (
 request_id CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL PRIMARY KEY,
 received_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 CONSTRAINT fk_fund_receipt FOREIGN KEY (request_id) REFERENCES fund_request(request_id)
) ENGINE=InnoDB;
-- +goose Down
DROP TABLE fund_event_receipt;
DROP TABLE fund_request;
DROP INDEX idx_errand_delivered_due ON errand;
DROP INDEX idx_credit_window ON credit_event;
ALTER TABLE credit_event DROP CHECK ck_credit_event,
 ADD CONSTRAINT ck_credit_event CHECK (round>=0 AND kind='GRAB_TIMEOUT_REVERT' AND delta=-5);
ALTER TABLE wallet_ledger DROP CHECK ck_ledger_ref,
 ADD CONSTRAINT ck_ledger_ref CHECK (ref_type IN ('OPENING','ESCROW'));
