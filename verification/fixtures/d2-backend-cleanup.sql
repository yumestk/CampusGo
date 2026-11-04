-- Only for campusgo_verification after an interrupted D2 backend test.
START TRANSACTION;
DELETE r FROM publish_request r JOIN app_user u ON u.id=r.publisher_id WHERE u.display_name='D2资金验证';
DELETE e FROM escrow_order e JOIN app_user u ON u.id=e.publisher_id WHERE u.display_name='D2资金验证';
DELETE l FROM errand_status_log l JOIN app_user u ON u.id=l.operator_id WHERE u.display_name='D2资金验证';
DELETE e FROM errand e JOIN app_user u ON u.id=e.publisher_id WHERE u.display_name='D2资金验证';
DELETE l FROM wallet_ledger l JOIN app_user u ON u.id=l.user_id WHERE u.display_name='D2资金验证';
DELETE a FROM wallet_account a JOIN app_user u ON u.id=a.owner_id WHERE a.owner_type='USER' AND u.display_name='D2资金验证';
DELETE FROM app_user WHERE display_name='D2资金验证';
UPDATE wallet_account SET available=0,version=0 WHERE id=1 AND NOT EXISTS (SELECT 1 FROM wallet_ledger WHERE account_id=1);
COMMIT;
