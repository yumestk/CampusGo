-- +goose Up
CREATE TABLE notification (
 id BIGINT NOT NULL AUTO_INCREMENT PRIMARY KEY,
 msg_key CHAR(36) CHARACTER SET ascii COLLATE ascii_bin NOT NULL,
 user_id BIGINT NOT NULL,
 errand_id BIGINT NOT NULL,
 type VARCHAR(24) NOT NULL,
 content VARCHAR(300) NOT NULL,
 read_flag BOOLEAN NOT NULL DEFAULT FALSE,
 created_at DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
 UNIQUE KEY uk_notice_event_user (msg_key,user_id),
 KEY idx_notice_user (user_id,id),
 KEY idx_notice_unread (user_id,read_flag,id),
 CONSTRAINT fk_notice_user FOREIGN KEY (user_id) REFERENCES app_user(id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4;
-- Existing receipts precede notifications. Reopen only those committed requests
-- so the existing finite funds recovery protocol republishes the immutable event.
UPDATE fund_request SET recovery_state='PENDING',recovery_attempts=0,next_recovery_at=CURRENT_TIMESTAMP(3),recovery_token=NULL,recovery_until=NULL WHERE state='COMMITTED';
-- +goose Down
DROP TABLE notification;
