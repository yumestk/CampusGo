CREATE TEMPORARY TABLE d3_browser_owned AS SELECT id FROM errand WHERE id BETWEEN 9007199254741301 AND 9007199254741308 AND publisher_id=1001 AND title LIKE 'D3页面验证:%';
START TRANSACTION;
DELETE t FROM grab_repair_task t JOIN d3_browser_owned o ON o.id=t.errand_id;
DELETE t FROM local_message t JOIN d3_browser_owned o ON o.id=t.errand_id;
DELETE t FROM grab_record t JOIN d3_browser_owned o ON o.id=t.errand_id;
DELETE t FROM grab_request t JOIN d3_browser_owned o ON o.id=t.errand_id;
DELETE t FROM errand_status_log t JOIN d3_browser_owned o ON o.id=t.errand_id;
DELETE t FROM errand t JOIN d3_browser_owned o ON o.id=t.id;
COMMIT;
