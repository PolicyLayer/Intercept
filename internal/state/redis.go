package state

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// RedisOptions configures a RedisStore.
type RedisOptions struct {
	FailMode FailMode
	Clock    Clock
}

// RedisStore implements Store backed by Redis using Lua scripts for atomicity.
type RedisStore struct {
	rdb            *redis.Client
	failMode       FailMode
	clock          Clock
	reserveScript  *redis.Script
	rollbackScript *redis.Script
	getScript      *redis.Script
}

// Lua scripts execute atomically in Redis, ensuring counter operations are
// race-free across multiple intercept instances.
var (
	// luaReserve checks the counter against a limit and increments if allowed.
	// It resets the counter when the window start changes.
	luaReserve = redis.NewScript(`
local key = KEYS[1]
local amount = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local wsArg = ARGV[3]
local wdArg = ARGV[4]
local val = 0
local ws = redis.call('HGET', key, 'ws')
if ws and ws ~= wsArg then
    val = 0
elseif ws then
    val = tonumber(redis.call('HGET', key, 'v') or '0')
end
if val + amount > limit then
    return {0, val}
end
val = val + amount
redis.call('HSET', key, 'v', val, 'ws', wsArg, 'wd', wdArg)
local ttlMs = math.ceil(tonumber(wdArg) / 1000000) * 2
if ttlMs > 0 then redis.call('PEXPIRE', key, ttlMs) end
return {1, val}
`)

	// luaRollback decrements a counter, flooring at zero.
	luaRollback = redis.NewScript(`
local key = KEYS[1]
local amount = tonumber(ARGV[1])
local exists = redis.call('EXISTS', key)
if exists == 0 then return 0 end
local val = tonumber(redis.call('HGET', key, 'v') or '0')
val = val - amount
if val < 0 then val = 0 end
redis.call('HSET', key, 'v', val)
return val
`)

	// luaGet reads the current counter value and window start.
	luaGet = redis.NewScript(`
local key = KEYS[1]
local exists = redis.call('EXISTS', key)
if exists == 0 then return {0, ''} end
local val = tonumber(redis.call('HGET', key, 'v') or '0')
local ws = redis.call('HGET', key, 'ws') or ''
return {val, ws}
`)
)

// NewRedisStore connects to Redis at the given URL and returns a RedisStore.
func NewRedisStore(url string, opts RedisOptions) (*RedisStore, error) {
	ropts, err := redis.ParseURL(url)
	if err != nil {
		return nil, fmt.Errorf("parse redis URL: %w", err)
	}

	rdb := redis.NewClient(ropts)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := rdb.Ping(ctx).Err(); err != nil {
		rdb.Close()
		return nil, fmt.Errorf("redis ping: %w", err)
	}

	clock := opts.Clock
	if clock == nil {
		clock = realClock{}
	}

	return &RedisStore{
		rdb:            rdb,
		failMode:       opts.FailMode,
		clock:          clock,
		reserveScript:  luaReserve,
		rollbackScript: luaRollback,
		getScript:      luaGet,
	}, nil
}

// Reserve atomically checks and increments a counter.
func (s *RedisStore) Reserve(key string, amount int64, limit int64, window time.Duration) (bool, int64, error) {
	now := s.clock.Now()
	ws := windowStart(now, window)

	res, err := s.reserveScript.Run(
		context.Background(), s.rdb, []string{key},
		amount, limit, ws.Format(time.RFC3339Nano), int64(window),
	).Int64Slice()
	if err != nil {
		if s.failMode == FailOpen {
			return true, 0, nil
		}
		return false, 0, fmt.Errorf("redis reserve: %w", err)
	}

	return res[0] == 1, res[1], nil
}

// Rollback decrements a counter by amount, flooring at zero.
func (s *RedisStore) Rollback(key string, amount int64) error {
	_, err := s.rollbackScript.Run(
		context.Background(), s.rdb, []string{key}, amount,
	).Result()
	if err != nil {
		if s.failMode == FailOpen {
			return nil
		}
		return fmt.Errorf("redis rollback: %w", err)
	}
	return nil
}

// Get returns the raw counter value and window start time.
func (s *RedisStore) Get(key string) (int64, time.Time, error) {
	res, err := s.getScript.Run(
		context.Background(), s.rdb, []string{key},
	).Slice()
	if err != nil {
		if s.failMode == FailOpen {
			return 0, time.Time{}, nil
		}
		return 0, time.Time{}, fmt.Errorf("redis get: %w", err)
	}

	val, _ := toInt64(res[0])
	wsStr, _ := res[1].(string)
	if wsStr == "" {
		return 0, time.Time{}, nil
	}
	ws, err := time.Parse(time.RFC3339Nano, wsStr)
	if err != nil {
		return 0, time.Time{}, fmt.Errorf("parse window_start for %q: %w", key, err)
	}
	return val, ws, nil
}

// Reset deletes a counter key entirely.
func (s *RedisStore) Reset(key string) error {
	err := s.rdb.Del(context.Background(), key).Err()
	if err != nil {
		if s.failMode == FailOpen {
			return nil
		}
		return fmt.Errorf("redis reset: %w", err)
	}
	return nil
}

// Close closes the Redis client connection.
func (s *RedisStore) Close() error {
	return s.rdb.Close()
}

// Resolve implements engine.StateResolver via structural typing.
func (s *RedisStore) Resolve(path string) (any, bool, error) {
	key := strings.TrimPrefix(path, "state.")

	res := s.rdb.HGetAll(context.Background(), key)
	if res.Err() != nil {
		if s.failMode == FailOpen {
			return int64(0), true, nil
		}
		return nil, false, fmt.Errorf("redis resolve: %w", res.Err())
	}

	m := res.Val()
	if len(m) == 0 {
		return int64(0), true, nil
	}

	val, _ := strconv.ParseInt(m["v"], 10, 64)
	wsStr := m["ws"]
	wdStr := m["wd"]

	if wsStr == "" || wdStr == "" {
		return val, true, nil
	}

	storedWS, err := time.Parse(time.RFC3339Nano, wsStr)
	if err != nil {
		return nil, false, fmt.Errorf("parse window_start for %q: %w", key, err)
	}
	wdNanos, _ := strconv.ParseInt(wdStr, 10, 64)

	now := s.clock.Now()
	ws := windowStart(now, time.Duration(wdNanos))
	if !storedWS.Equal(ws) {
		return int64(0), true, nil
	}

	return val, true, nil
}

// toInt64 converts a Redis result value (int64 or string) to int64.
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case int64:
		return n, true
	case string:
		i, err := strconv.ParseInt(n, 10, 64)
		return i, err == nil
	default:
		return 0, false
	}
}
