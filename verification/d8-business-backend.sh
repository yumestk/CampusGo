#!/bin/sh
# D8 business acceptance only. Fault drills and performance runs are separate.
set -eu
cd "$(dirname "$0")/.."
mkdir -p work/d8-business
docker compose --env-file .env -f deploy/local/compose.yaml up -d --wait mysql redis
for db in campusgo campusgo_verification; do ./bin/migrate -database "$db" up; ./bin/seed -database "$db"; done
cleanup(){
 python3 verification/d3-cleanup.py
 python3 verification/d4-cleanup.py
 python3 verification/d5-cleanup.py
}
cleanup
trap cleanup EXIT
run(){
 name=$1;shift
 if "$@" >"work/d8-business/$name.jsonl" 2>&1;then
  python3 verification/d8-results.py "work/d8-business/$name.jsonl"
 else
  cat "work/d8-business/$name.jsonl"
  return 1
 fi
}
export CAMPUSGO_D2_INTEGRATION=1 CAMPUSGO_D3_INTEGRATION=1 CAMPUSGO_D4_INTEGRATION=1 CAMPUSGO_D5_INTEGRATION=1 CAMPUSGO_D8_BUSINESS=1
# Whitelists intentionally omit dependency outages, lost acknowledgments,
# injected SQL failures, corrupt facts, SIGKILL and recovery-worker experiments.
run publishing go test -race -count=1 -json ./internal/adapter/mysql -run '^TestIntegrationPublishFunds$/(atomic_publish_receipt_and_seed_does_not_refill|same_key_concurrency_and_fingerprint_conflict|distinct_keys_concurrent_balance_exhaustion|rejections_and_boundaries_are_stable|missing_wallet_and_stale_identity_do_not_create_funds|exact_int64_capacity_and_decimal_global_totals)$'
run grabbing go test -race -count=1 -json ./internal/adapter/mysql -run '^TestIntegrationGrab$/(same_round_48_runners_one_SQL_winner|same_request_24_concurrent_replays_and_fingerprint_conflict|eligibility_round_roles_credit_and_inflight_excludes_LOCKED|string_versions_above_JS_precision_and_missing_projection_rebuild|cache_negative_bloom_stale_rebuild_and_SQL_authorization)$'
python3 verification/d3-cleanup.py
run confirmation go test -race -count=1 -json ./internal/adapter/mysql -run '^TestIntegrationD4$/(confirmation_authority_round_deadline_and_replay|cross_task_concurrent_quota_never_exceeds_five|promotion_filters_identity_credit_quota_and_registration_is_best_effort|before_deadline_confirmation_wins_against_early_timeout|weighted_queue_same_round_and_exact_token_removal|early_duplicate_old_round_and_transfer_atomicity|confirm_vs_timeout_has_one_winner|candidate_eligibility_rechecked_and_budget_not_empty|repools_credit_once_new_cycle_and_old_candidates_filtered|five_transfers_per_cycle_then_repool|strict_message_validation_and_early_old_payload)$'
python3 verification/d4-cleanup.py
run fulfillment go test -race -count=1 -json ./internal/adapter/mysql -run '^TestIntegrationD5$/(fulfillment_permissions_idempotency_and_delivery_time|refund_and_both_arbitration_directions|tiny_commission_zero_and_integer_rounding|same_key_concurrency_and_fingerprint|distinct_key_settlements_and_shared_account_snapshots|dispute_races_manual_settlement|opposite_arbitration_race|auto_settlement_deadline_early_duplicate_and_scan|credit_30day_calibration_and_clamp|concurrent_due_credit_calibration_runs_once)$'
python3 verification/d5-cleanup.py
run admission go test -race -count=1 -json ./internal/adapter/mysql -run '^TestIntegrationD8GrabLimits$'
run contracts go test -race -count=1 -json ./internal/transport/httpapi -run '^(TestIntegrationD8Business|TestIntegrationD3HTTP|TestIntegrationD4HTTP)$'
export CAMPUSGO_INTEGRATION=1
run identity go test -race -count=1 -json ./internal/auth -run '^TestIntegrationJWTLifecycle$/(logout_revokes_access_and_refresh_including_expired_access|unknown_refresh_cannot_revoke_known_family|claims_algorithm_signature_expiry_and_environment|rotation_keeps_absolute_family_expiry_and_encrypted_receipt|expired_access_can_revoke_family)$'
run oidc go test -race -count=1 -json ./internal/auth -run '^TestIntegrationOIDCSecurity$/(valid|nonce|issuer|audience|expired|signature|authorized_party|access_hash|unbound)$'
run notifications go test -race -count=1 -json ./internal/transport/httpapi -run '^TestIntegrationD7Notifications$'
./bin/reconcile -database campusgo_verification
./bin/reconcile
printf '%s\n' 'D8 business component and HTTP assertions passed; system fault/performance/release acceptance is pending.'
