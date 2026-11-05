-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE local_message ADD COLUMN replay_count INT NOT NULL DEFAULT 0,
  ADD COLUMN last_replay_reason VARCHAR(200) NULL,
  ADD INDEX idx_message_lease (state,locked_until,id);
CREATE INDEX idx_errand_confirm_due ON errand(status,confirm_deadline,id);
CREATE INDEX idx_errand_runner_status ON errand(grabber_id,status);
CREATE TABLE credit_event (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  user_id BIGINT NOT NULL,
  errand_id BIGINT NOT NULL,
  round INT NOT NULL,
  kind VARCHAR(40) NOT NULL,
  delta INT NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uk_credit_event (errand_id,round,kind),
  CONSTRAINT fk_credit_event_user FOREIGN KEY (user_id) REFERENCES app_user(id),
  CONSTRAINT ck_credit_event CHECK (round>=0 AND kind='GRAB_TIMEOUT_REVERT' AND delta=-5)
) ENGINE=InnoDB;
-- +goose Down
DROP TABLE credit_event;
DROP INDEX idx_errand_runner_status ON errand;
DROP INDEX idx_errand_confirm_due ON errand;
ALTER TABLE local_message DROP INDEX idx_message_lease, DROP COLUMN last_replay_reason, DROP COLUMN replay_count;
