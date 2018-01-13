# rate-limiter
一个基于redis的分布式限流器



## 限流原理：
到达本地服务实例的 给定服务(psm)给定方法(method)的请求,需要向本地服务实例获取并消耗一个对应的token，才能得以被执行。

本地服务实例的token的配额 需要通过accquire函数申请(基于redis),每次成功的申请可以得到quota的配额，供本地实例消耗。
redis 中存放1s内各个服务实例申请的配额总数QuotaNumPerSec。

accquire 函数 每次将QuotaNumPerSec 加1。

如果QuotaNumPerSec>=RateLimiter.limitQuotaNumPerSec,也就是说1s内各个服务实例申请的配额累计已经超过界限，则配额申请失败。
从而可以达到流量限制的作用。

redis 中 QuotaNumPerSec 的维护采用lua 脚本。具体参考为：https://redis.io/commands/incr。

accquire 函数本身通过访问间隔控制，防止在配额申请失败的情况下，大量请求打到redis上。

限流器通过互斥锁保护对于临界资源的访问，因此是并发安全的。但锁的粒度过大，后期有优化空间。

每秒配额数与qps 换算关系为 QuotaNumPerSec * quota = qps
