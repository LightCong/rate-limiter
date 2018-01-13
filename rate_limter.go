package ratelimter

import (
	"sync"
	"time"
)
/*
限流原理：

到达本地服务实例的 给定服务(psm)给定方法(method)的请求,需要向本地服务实例获取并消耗一个对应的token，才能得以被执行。

本地服务实例的token的配额 需要通过accquire函数申请(基于redis),每次成功的申请可以得到quota=50的配额，供本地实例消耗。

redis 中存放1s内各个服务实例申请的配额总数QuotaNumPerSec。
accquire 函数 每次将QuotaNumPerSec 加1。
如果QuotaNumPerSec>=RateLimiter.limitQuotaNumPerSec,也就是说1s内各个服务实例申请的配额累计已经超过界限，则配额申请失败。
从而可以达到流量限制的作用。

redis 中 QuotaNumPerSec 的维护采用lua 脚本。具体参考为：https://redis.io/commands/incr。

accquire 函数本身通过访问间隔控制，防止在配额申请失败的情况下，大量请求打到redis上。

限流器通过互斥锁保护对于临界资源的访问，因此是并发安全的。但锁的粒度过大，后期有优化空间。

每秒配额数与qps 换算关系为 QuotaNumPerSec * quota = qps
 */

const tokenQuota = 10
const limiterScript = `
local QuotaNumPerSec
QuotaNumPerSec = redis.call("incr",KEYS[1])
if tonumber(QuotaNumPerSec) == 1 then
    redis.call("expire",KEYS[1],1)
end
return QuotaNumPerSec
`
const loadScriptRetryTimes = 3

var redisLimitDuration = 100*int64(time.Microsecond)  //0.1 ms

type RedisClient interface {
	ScriptLoad(script string) (string, error)
	EvalSha(sha1 string, keys []string, args ...interface{}) (interface{}, error)
	ScriptExists(scripts ...string)([]bool,error)
}

//RateLimiter 限流器
type RateLimiter struct {
	redisKey                 string
	redisClient              RedisClient
	limitQuotaNumPerSec      int //每秒允许的quota数上限  ： limit qps/quota
	leftQuota                int //当前quota余额
	scriptSHA1               string
	locker                   sync.Mutex
	lastAqquireRedisTmeStamp int64
}

//NewRateLimiter 创建一个限流器
func NewRateLimiter(psm string, method string, redisClient RedisClient, limitQuotaNumPerSec int) *RateLimiter {
	if redisClient == nil || limitQuotaNumPerSec <= 0 || len(psm) == 0 {
		//log.Error("create NewRateLimiter failed ! psm is %v method is %v redisClient exist : %v", psm, method, redisClient != nil)
		return nil
	}
	sha1, err := redisloadScript(redisClient, limiterScript, 0)
	if err != nil {
		//log.Error("create NewRateLimiter failed ! psm is %v method is %v load script failed %v",psm,method, err)
		//log.Error(err.Error())
		return nil
	}

	limiter := &RateLimiter{
		redisKey:                 psm + "_" + method,
		redisClient:              redisClient,
		limitQuotaNumPerSec:      limitQuotaNumPerSec,
		leftQuota:                0,
		scriptSHA1:               sha1,
		locker:                   sync.Mutex{},
		lastAqquireRedisTmeStamp: 0,
	}

	return limiter
}

//CanPass 判断是否可以访问key 对应的服务/方法 , 限流器对外暴露的接口
func (limiter *RateLimiter) CanPass() bool {
	limiter.locker.Lock()
	defer limiter.locker.Unlock()

	if limiter.leftQuota >= 0 {
		limiter.leftQuota--
		return true
	}

	//向redis申请配额
	if limiter.aqquireQuota() == false {
		return false
	}

	limiter.leftQuota = tokenQuota
	limiter.leftQuota--
	return true
}

func (limiter *RateLimiter) aqquireQuota() bool {
	if time.Now().UnixNano()-limiter.lastAqquireRedisTmeStamp<redisLimitDuration{   
        //限制单机对于redis 的请求qps 不超过1w
        return false
    }

	//v, err := limiter.redisClient.EvalSha(limiter.scriptSHA1, []string{limiter.redisKey})
	v, err :=limiter.redisExcuteLua()
	if err != nil {
		//无法从redis中申请quota
		//log.Error("get quota from redis failed key is %v", limiter.redisKey)
		return false
	}
	// 设置上次成功访问的时间
	limiter.lastAqquireRedisTmeStamp=time.Now().UnixNano()

	nowQuotaNum, ok := v.(int)
	if !ok {
		//log.Error("invalid value in the redis key is %v", limiter.redisKey)
		return false
	}
	if nowQuotaNum >= limiter.limitQuotaNumPerSec {
		return false
	}
	return true
}

//加载脚本
func redisloadScript(cli RedisClient, script string, retryTimes int) (string, error) {
	if retryTimes <= 0 {
		return cli.ScriptLoad(script)
	}
	for i := 0; i < retryTimes-1; i++ {
		sha1, err := cli.ScriptLoad(script)
		if err == nil {
			return sha1, err
		}
		time.Sleep(10 * time.Millisecond)
	}
	return cli.ScriptLoad(script)
}

//执行redis lua 脚本
func (limiter *RateLimiter) redisExcuteLua()(interface{},error){
	v, err := limiter.redisClient.EvalSha(limiter.scriptSHA1, []string{limiter.redisKey})
	if err == nil {
		return v,nil
	}
	// 如果脚本丢失，那么可以重新加载一次
	boollst,serr:= limiter.redisClient.ScriptExists(limiter.scriptSHA1)
	if serr!=nil || len(boollst)==0||boollst[0]!=true{
		//log.Info("reload script to redis !")
		sha1, rerr := redisloadScript(limiter.redisClient, limiterScript, 0)
		if rerr!=nil{
			//log.Error("reload script to redis failed ! %v",rerr)
			return v,err
		}
		limiter.scriptSHA1=sha1
		return  limiter.redisClient.EvalSha(limiter.scriptSHA1, []string{limiter.redisKey})
	}
	return v,err
}

