-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE grab_record (
  id BIGINT NOT NULL PRIMARY KEY,
  campus_id BIGINT NOT NULL,
  errand_id BIGINT NOT NULL,
  runner_id BIGINT NOT NULL,
  seq INT NOT NULL,
  round INT NOT NULL DEFAULT 0,
  result VARCHAR(16) NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  UNIQUE KEY uk_errand_round_seq (errand_id,round,seq),
  UNIQUE KEY uk_errand_round_runner (errand_id,round,runner_id),
  KEY idx_runner_errand (runner_id,errand_id),
  CONSTRAINT ck_grab_ids CHECK (id > 0 AND campus_id > 0 AND errand_id > 0 AND runner_id > 0),
  CONSTRAINT ck_grab_round CHECK (round >= 0 AND seq BETWEEN 0 AND 1),
  CONSTRAINT ck_grab_result CHECK (result IN ('GRABBED','CANDIDATE','EXPIRED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
CREATE INDEX idx_campus_status_created_id ON errand(campus_id,status,created_at,id);
CREATE INDEX idx_publisher_created_id ON errand(publisher_id,created_at,id);

-- +goose Down
DROP INDEX idx_publisher_created_id ON errand;
DROP INDEX idx_campus_status_created_id ON errand;
DROP TABLE grab_record;
