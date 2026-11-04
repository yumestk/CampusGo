-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE app_user (
  id BIGINT NOT NULL PRIMARY KEY,
  campus_id BIGINT NOT NULL,
  display_name VARCHAR(64) NOT NULL,
  role VARCHAR(16) NOT NULL DEFAULT 'STUDENT',
  active BOOLEAN NOT NULL DEFAULT TRUE,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  CONSTRAINT ck_user_id CHECK (id > 0 AND campus_id > 0),
  CONSTRAINT ck_user_role CHECK (role IN ('STUDENT','ARBITRATOR'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE identity_binding (
  issuer VARBINARY(512) NOT NULL,
  subject VARBINARY(255) NOT NULL,
  user_id BIGINT NOT NULL,
  PRIMARY KEY (issuer,subject),
  KEY idx_identity_user (user_id),
  CONSTRAINT fk_identity_user FOREIGN KEY (user_id) REFERENCES app_user(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE errand (
  id BIGINT NOT NULL PRIMARY KEY,
  campus_id BIGINT NOT NULL,
  publisher_id BIGINT NOT NULL,
  grabber_id BIGINT NULL,
  type VARCHAR(16) NOT NULL,
  title VARCHAR(64) NOT NULL,
  reward_amount BIGINT NOT NULL,
  slot_total INT NOT NULL DEFAULT 1,
  slot_taken INT NOT NULL DEFAULT 0,
  status VARCHAR(16) NOT NULL,
  round INT NOT NULL DEFAULT 0,
  version BIGINT NOT NULL DEFAULT 0,
  locked_at DATETIME(3) NULL,
  confirm_deadline DATETIME(3) NULL,
  delivered_at DATETIME(3) NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  KEY idx_campus_status_id (campus_id,status,id),
  KEY idx_publisher_id (publisher_id,id),
  KEY idx_grabber_id (grabber_id,id),
  CONSTRAINT ck_errand_id CHECK (id > 0 AND campus_id > 0 AND publisher_id > 0),
  CONSTRAINT ck_errand_amount CHECK (reward_amount > 0),
  CONSTRAINT ck_errand_slot CHECK (slot_total = 1 AND slot_taken BETWEEN 0 AND 1),
  CONSTRAINT ck_errand_round CHECK (round >= 0 AND version >= 0),
  CONSTRAINT ck_errand_type CHECK (type IN ('DELIVERY','BUY','QUEUE','OTHER')),
  CONSTRAINT ck_errand_status CHECK (status IN ('DRAFT','PUBLISHED','LOCKED','ACCEPTED','PICKED_UP','DELIVERED','SETTLED','CLOSED','CANCELLED','DISPUTED','REFUNDED'))
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

CREATE TABLE errand_status_log (
  id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
  campus_id BIGINT NOT NULL,
  errand_id BIGINT NOT NULL,
  from_status VARCHAR(16) NOT NULL,
  to_status VARCHAR(16) NOT NULL,
  round INT NOT NULL,
  operator_id BIGINT NOT NULL,
  created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  KEY idx_errand_log (campus_id,errand_id,id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;

-- +goose Down
DROP TABLE errand_status_log;
DROP TABLE errand;
DROP TABLE identity_binding;
DROP TABLE app_user;
