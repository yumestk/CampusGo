-- CampusGo-specific entry policy. APISIX owns proxying, native concurrency and WS.
local core = require('apisix.core')
local redis = require('resty.redis')
local ipmatcher = require('resty.ipmatcher')
local bucket = [[
 local t=redis.call('TIME'); local now=t[1]*1000+math.floor(t[2]/1000)
 local rate=tonumber(ARGV[1]);local cap=tonumber(ARGV[2]);local v=redis.call('HMGET',KEYS[1],'tokens','time')
 local last=tonumber(v[2]) or now;local stamp=math.max(last,now)
 local n=math.min(cap,(tonumber(v[1]) or cap)+math.max(0,now-last)*rate/1000)
 local wait=0;if n<1 then wait=math.ceil((1-n)*1000/rate)+math.max(0,last-now) else n=n-1 end
 redis.call('HSET',KEYS[1],'tokens',n,'time',stamp);redis.call('PEXPIRE',KEYS[1],math.ceil(cap*1000/rate)+60000+math.max(0,stamp-now))
 return wait
]]
local schema={type='object',properties={
 route_rate={type='number',minimum=0.001,default=1000},route_burst={type='integer',minimum=1,default=2000},
 ip_rate={type='number',minimum=0.001,default=100},ip_burst={type='integer',minimum=1,default=200},
 auth_rate={type='number',minimum=0.001,default=2},auth_burst={type='integer',minimum=1,default=10},
 recovery_count={type='integer',minimum=1,default=20},namespace={type='string',pattern='^[a-zA-Z0-9_-]+$',default='v1'},
 trusted_proxies={type='array',items={type='string'},default={}}
}}
local M={version=0.1,priority=1002,name='campusgo-entry',schema=schema}
function M.check_schema(conf) return core.schema.check(schema,conf) end
local function reject(status,code,retry)
 ngx.ctx.campusgo_policy_error=true
 ngx.header['Content-Type']='application/json';ngx.header['Cache-Control']='no-store'
 if retry then ngx.header['Retry-After']=tostring(math.max(1,math.ceil(retry/1000))) end
 return status,{code=code,message='请求暂时无法处理',data=core.json.null}
end
function M.rewrite(conf,ctx)
 local raw=ngx.var.request_uri:match('^[^?]*') or ''
 if raw:find('%%') or raw:find(';',1,true) or raw:find('\\',1,true) or raw:find('..',1,true) or raw:find('//',1,true) then return reject(400,'INVALID_PATH') end
 local path=ngx.var.uri
 if (path=='/api/internal' or path:match('^/api/internal/')) or path=='/metrics' then return reject(404,'NOT_FOUND') end
 local peer=ngx.var.remote_addr;local trusted={}
 for _,ip in ipairs(conf.trusted_proxies) do trusted[ip]=true end
 local source=peer
 if trusted[peer] then
  local chain=ngx.req.get_headers()['X-Forwarded-For']
  if type(chain)=='string' and #chain<=2048 then
   local hops={};local valid=true
   for value in (chain..','):gmatch('(.-),') do
    local ip=value:match('^%s*(.-)%s*$');if ip=='' or ip:find('/',1,true) or not ipmatcher.new({ip}) then valid=false;break end
    hops[#hops+1]=ip
   end
   if valid then for i=#hops,1,-1 do if not trusted[hops[i]] then source=hops[i];break end end end
  end
 end
 for _,h in ipairs({'X-User-Id','X-Runner-Id','X-Campus-Id','X-Internal-Token','Forwarded','X-Forwarded-For','X-Forwarded-Host','X-Forwarded-Proto','X-Forwarded-Port','X-Forwarded-Prefix','X-Real-IP','X-CampusGo-Gateway','X-CampusGo-Source'}) do ngx.req.clear_header(h) end
 local secret=os.getenv('CAMPUSGO_GATEWAY_SECRET');if not secret or #secret<32 then return reject(503,'GATEWAY_UNAVAILABLE',1000) end
 ngx.req.set_header('X-CampusGo-Gateway',secret)
 ngx.req.set_header('X-CampusGo-Source',source)
 ctx.campusgo_source=source
 local auth=path:match('^/api/auth/')~=nil
 local query=ngx.req.get_method()=='GET' and (path:match('^/api/errands/%d+/grab%-requests/[%x%-]+$') or path:match('^/api/fund%-requests/[%x%-]+$') or path:match('^/api/publish%-requests/[%x%-]+$'))
 ctx.campusgo_recovery=query
 ctx.campusgo_route=auth and 'auth' or path:match('/grab$') and 'grab' or query and 'result' or 'api'
end
function M.access(conf,ctx)
 local client=redis:new();client:set_timeout(300)
 local function fail()
  client:close()
  if ctx.campusgo_recovery then
   local shared=ngx.shared['plugin-limit-count'];local key='campusgo-recovery:'..conf.namespace..':'..math.floor(ngx.now())
   local count=shared:incr(key,1,0,2)
   if count and count<=conf.recovery_count then return end
  end
  return reject(503,'RATE_LIMIT_UNAVAILABLE',1000)
 end
 local ok=client:connect('redis',6379);if not ok then return fail() end
 ok=client:auth(os.getenv('CAMPUSGO_REDIS_PASSWORD'));if not ok then return fail() end
 local prefix='campusgo:gateway:'..conf.namespace
 local wait=client:eval(bucket,1,prefix..':{route:'..ctx.campusgo_route..'}',conf.route_rate,conf.route_burst)
 if not wait then return fail() end
 if wait>0 then client:set_keepalive(10000,20);return reject(429,'RATE_LIMITED',wait) end
 local auth=ctx.campusgo_route=='auth'
 wait=client:eval(bucket,1,prefix..':{ip:'..ctx.campusgo_source..'}:'..(auth and 'auth' or 'api'),auth and conf.auth_rate or conf.ip_rate,auth and conf.auth_burst or conf.ip_burst)
 if not wait then return fail() end
 client:set_keepalive(10000,20)
 if wait>0 then return reject(429,'RATE_LIMITED',wait) end
end
function M.header_filter(conf,ctx)
 if ngx.status==503 and not ngx.ctx.campusgo_policy_error and (not ngx.var.upstream_status or ngx.var.upstream_status=='') then ctx.campusgo_busy=true;ngx.header['Content-Length']=nil;ngx.header['Content-Type']='application/json' end
 if ngx.status==502 or ngx.status==504 then
  ngx.status=503;ngx.header['Content-Length']=nil;ngx.header['Content-Type']='application/json';ctx.campusgo_upstream_error=true
 end
 if ngx.status==503 then ngx.header['Retry-After']='1';ngx.header['Cache-Control']='no-store' end
end
function M.body_filter(conf,ctx)
 if ctx.campusgo_busy then if ngx.arg[2] then ngx.arg[1]='{"code":"GATEWAY_BUSY","message":"入口繁忙，请稍后重试","data":null}' else ngx.arg[1]=nil end;return end
 if ctx.campusgo_upstream_error then
  if ngx.arg[2] then ngx.arg[1]='{"code":"UPSTREAM_UNAVAILABLE","message":"上游服务暂不可用","data":null}' else ngx.arg[1]=nil end
 end
end
return M
