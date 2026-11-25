-- ARGV: dbVersion, round, status, slotTaken, ownerRunnerId, ttlMillis, active, verifiedRepair
local function compare(a, b)
    if #a ~= #b then return #a < #b and -1 or 1 end
    if a == b then return 0 end
    return a < b and -1 or 1
end
if redis.call('HGET', KEYS[1], 'mode') ~= 'READY' then return 3 end
local current = redis.call('HGET', KEYS[1], 'dbVersion')
if not current then return 3 end
local order = compare(ARGV[1], current)
if order < 0 then return 2 end
local inactive = ARGV[7] ~= 'true'
local function clearPending()
    redis.call('HDEL', KEYS[1], 'requestDbId','reservationToken','baseVersion','pendingRound','runnerId','leaseUntil')
end
if order == 0 then
    local same = redis.call('HGET', KEYS[1], 'round') == ARGV[2]
        and redis.call('HGET', KEYS[1], 'status') == ARGV[3]
        and redis.call('HGET', KEYS[1], 'slotTaken') == ARGV[4]
        and redis.call('HGET', KEYS[1], 'ownerRunnerId') == ARGV[5]
    if same then
        local ttl = redis.call('PTTL', KEYS[1])
        if ttl == -1 or (inactive and ttl > tonumber(ARGV[6])) then
            redis.call('PEXPIRE', KEYS[1], ARGV[6])
        end
        if inactive then clearPending() end
        return 1
    end
    if ARGV[8] ~= '1' then return 4 end
end
redis.call('HSET', KEYS[1], 'dbVersion', ARGV[1], 'round', ARGV[2],
    'status', ARGV[3], 'slotTaken', ARGV[4], 'ownerRunnerId', ARGV[5])
-- 同版本重放或校准绝不清理在途占位。新版本只清理已经失效的占位。
local pendingBase = redis.call('HGET', KEYS[1], 'baseVersion')
if inactive or (order > 0 and pendingBase and (compare(pendingBase, ARGV[1]) < 0
        or redis.call('HGET', KEYS[1], 'pendingRound') ~= ARGV[2])) then
    clearPending()
end
redis.call('PEXPIRE', KEYS[1], ARGV[6])
return 0
