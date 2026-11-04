-- +goose NO TRANSACTION
-- +goose Up
CREATE TABLE d0_protocol_probe (
  id VARCHAR(100) NOT NULL PRIMARY KEY,
  state VARCHAR(20) NOT NULL,
  created_at TIMESTAMP(6) NOT NULL DEFAULT CURRENT_TIMESTAMP(6)
) ENGINE=InnoDB;

-- +goose Down
DROP TABLE d0_protocol_probe;
