package ratelimit

import (
	"context"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

const (
	keyPrefix = "ratelimit:"
	// redisTimeout bounds each check so a hung Redis can't stall requests.
	redisTimeout = 200 * time.Millisecond
)

// tokenBucket refills and spends one token atomically. It reads the clock with
// Redis TIME rather than taking it from the caller, so instances with skewed
// clocks still share one consistent clock. The bucket expires after a full
// window idle (by then it would be full anyway), which keeps Redis memory bounded.
//
// KEYS[1] bucket, ARGV[1] tokens per ms, ARGV[2] capacity, ARGV[3] ttl ms.
// Returns {allowed (0|1), retry after ms}.
var tokenBucket = redis.NewScript(`
local t = redis.call('TIME')
local now = tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
local rate = tonumber(ARGV[1])
local cap = tonumber(ARGV[2])
local b = redis.call('HMGET', KEYS[1], 'tokens', 'ts')
local tokens = tonumber(b[1])
local ts = tonumber(b[2])
if tokens == nil or ts == nil then
  tokens = cap
  ts = now
end
tokens = math.min(cap, tokens + math.max(0, now - ts) * rate)
local allowed = 0
local wait = 0
if tokens >= 1 then
  tokens = tokens - 1
  allowed = 1
else
  wait = math.ceil((1 - tokens) / rate)
end
redis.call('HSET', KEYS[1], 'tokens', tokens, 'ts', now)
redis.call('PEXPIRE', KEYS[1], ARGV[3])
return {allowed, wait}
`)

// Redis is a token bucket limiter shared by every instance using the same Redis.
type Redis struct {
	client *redis.Client
	rate   string // tokens per millisecond
	burst  string
	ttl    string // milliseconds
}

func NewRedis(client *redis.Client, cfg Config) *Redis {
	ms := cfg.Window.Milliseconds()
	return &Redis{
		client: client,
		rate:   strconv.FormatFloat(float64(cfg.Requests)/float64(ms), 'g', -1, 64),
		burst:  strconv.Itoa(cfg.Requests),
		ttl:    strconv.FormatInt(ms, 10),
	}
}

func (l *Redis) Allow(ctx context.Context, key string) (bool, time.Duration, error) {
	ctx, cancel := context.WithTimeout(ctx, redisTimeout)
	defer cancel()
	res, err := tokenBucket.Run(ctx, l.client, []string{keyPrefix + key}, l.rate, l.burst, l.ttl).Int64Slice()
	if err != nil {
		return false, 0, err
	}
	return res[0] == 1, time.Duration(res[1]) * time.Millisecond, nil
}
