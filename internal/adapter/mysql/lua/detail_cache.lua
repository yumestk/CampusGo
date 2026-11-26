-- KEYS: watermark, negative entry, four detail shards (one task hash tag).
-- ARGV: operation, version, serialized body, body TTL ms, watermark TTL ms.
local function less(a, b)
    if #a ~= #b then return #a < #b end
    return a < b
end

local operation = ARGV[1]
local watermark = redis.call('GET', KEYS[1])
local version = ARGV[2]
if operation == 'negative' and watermark then return 0 end
if watermark and less(version, watermark) then return 0 end

-- A shard can outlive a deleted/evicted watermark. Its JSON encodes version as a string.
for i = 3, #KEYS do
    local raw = redis.call('GET', KEYS[i])
    if raw then
        if operation == 'negative' then return 0 end
        local ok, stored = pcall(cjson.decode, raw)
        if ok and type(stored) == 'table' and type(stored.data) == 'table' then
            local existing = stored.data.version
            if type(existing) == 'string' and existing:match('^%d+$') and less(version, existing) then
                return 0
            end
        end
    end
end

if operation == 'negative' then
    redis.call('PSETEX', KEYS[2], ARGV[4], ARGV[3])
    return 1
end

-- Delete before allocating the watermark on noeviction Redis. With no
-- keys to reclaim, PSETEX remains the first write so Redis can reject it.
if operation == 'invalidate' then
    if redis.call('EXISTS', unpack(KEYS, 2)) > 0 then
        redis.call('DEL', unpack(KEYS, 2))
    end
    return redis.call('PSETEX', KEYS[1], ARGV[5], version)
end
if operation == 'published' then
    if redis.call('EXISTS', KEYS[2]) == 1 then
        redis.call('DEL', KEYS[2])
    elseif watermark and version == watermark then
        return 0
    end
    return redis.call('PSETEX', KEYS[1], ARGV[5], version)
end
redis.call('PSETEX', KEYS[1], ARGV[5], version)
redis.call('DEL', KEYS[2])
for i = 3, #KEYS do
    redis.call('PSETEX', KEYS[i], ARGV[4], ARGV[3])
end
return 1
