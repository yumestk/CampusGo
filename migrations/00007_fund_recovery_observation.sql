-- +goose NO TRANSACTION
-- +goose Up
ALTER TABLE fund_request ADD COLUMN last_recovery_error VARCHAR(40) NOT NULL DEFAULT '';
ALTER TABLE credit_score ADD COLUMN last_calibrated_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 ADD INDEX idx_credit_calibration(last_calibrated_at,user_id);
-- +goose Down
ALTER TABLE credit_score DROP INDEX idx_credit_calibration,DROP COLUMN last_calibrated_at;
ALTER TABLE fund_request DROP COLUMN last_recovery_error;
