-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE credit_score (
  user_id BIGINT NOT NULL PRIMARY KEY,
  score INT NOT NULL DEFAULT 60,
  version BIGINT NOT NULL DEFAULT 0,
  CONSTRAINT fk_credit_user FOREIGN KEY (user_id) REFERENCES app_user(id),
  CONSTRAINT ck_credit_score CHECK (score BETWEEN 0 AND 100 AND version >= 0)
) ENGINE=InnoDB;

CREATE TABLE grab_request (
  id BIGINT NOT NULL PRIMARY KEY,
  campus_id BIGINT NOT NULL,
  runner_id BIGINT NOT NULL,
  request_key CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  errand_id BIGINT NOT NULL,
  fingerprint BINARY(32) NOT NULL,
  base_version BIGINT NOT NULL,
  round INT NOT NULL,
  reservation_token CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  lease_until DATETIME(3) NOT NULL,
  state VARCHAR(16) NOT NULL DEFAULT 'PROCESSING',
  error_code VARCHAR(40) NOT NULL DEFAULT '',
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  UNIQUE KEY uk_grab_request (campus_id,runner_id,request_key),
  KEY idx_grab_expire (state,lease_until,id),
  KEY idx_grab_task (campus_id,errand_id,id),
  CONSTRAINT ck_request_ids CHECK (id>0 AND campus_id>0 AND runner_id>0 AND errand_id>0 AND base_version>=0 AND round>=0),
  CONSTRAINT ck_request_state CHECK ((state IN ('PROCESSING','SUCCEEDED') AND error_code='') OR (state IN ('REJECTED','EXPIRED') AND error_code<>''))
) ENGINE=InnoDB;

ALTER TABLE grab_record ADD COLUMN request_id BIGINT NULL,
  ADD UNIQUE KEY uk_grab_record_request (request_id),
  ADD CONSTRAINT fk_grab_record_request FOREIGN KEY (request_id) REFERENCES grab_request(id);

CREATE TABLE local_message (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  campus_id BIGINT NOT NULL,
  errand_id BIGINT NOT NULL,
  msg_key VARCHAR(96) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  topic VARCHAR(64) NOT NULL,
  payload JSON NOT NULL,
  deliver_at DATETIME(3) NOT NULL,
  state VARCHAR(16) NOT NULL DEFAULT 'PENDING',
  retry_count INT NOT NULL DEFAULT 0,
  next_run_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  worker_token CHAR(36) NULL,
  locked_until DATETIME(3) NULL,
  last_error VARCHAR(500) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uk_local_message (msg_key),
  KEY idx_message_due (state,next_run_at,id),
  CONSTRAINT ck_message_state CHECK (state IN ('PENDING','RUNNING','DONE','DEAD'))
) ENGINE=InnoDB;

CREATE TABLE grab_repair_task (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  campus_id BIGINT NOT NULL,
  errand_id BIGINT NOT NULL,
  event_key VARCHAR(128) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
  kind VARCHAR(24) NOT NULL,
  request_id BIGINT NULL,
  state VARCHAR(16) NOT NULL DEFAULT 'PENDING',
  retry_count INT NOT NULL DEFAULT 0,
  replay_count INT NOT NULL DEFAULT 0,
  next_run_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  worker_token CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NULL,
  locked_until DATETIME(3) NULL,
  last_error VARCHAR(500) NULL,
  last_replay_reason VARCHAR(200) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  UNIQUE KEY uk_repair_event (event_key),
  KEY idx_repair_due (state,next_run_at,id),
  KEY idx_repair_lease (state,locked_until,id),
  CONSTRAINT fk_repair_request FOREIGN KEY (request_id) REFERENCES grab_request(id),
  CONSTRAINT ck_repair_kind CHECK (kind IN ('PROJECTION','REVOKE','CACHE_INVALIDATE')),
  CONSTRAINT ck_repair_state CHECK (state IN ('PENDING','RUNNING','DONE','DEAD')),
  CONSTRAINT ck_repair_count CHECK (retry_count>=0 AND replay_count>=0)
) ENGINE=InnoDB;

-- +goose Down
DROP TABLE grab_repair_task;
DROP TABLE local_message;
ALTER TABLE grab_record DROP FOREIGN KEY fk_grab_record_request, DROP INDEX uk_grab_record_request, DROP COLUMN request_id;
DROP TABLE grab_request;
DROP TABLE credit_score;
