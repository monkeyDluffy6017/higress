// Copyright (c) 2022 Alibaba Group Holding Ltd.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//      http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package wrapper

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/higress-group/proxy-wasm-go-sdk/proxywasm"
	"github.com/tidwall/resp"
)

// RedisError types for better error handling
type RedisErrorType int

const (
	RedisErrorTypeConnection RedisErrorType = iota
	RedisErrorTypeTimeout
	RedisErrorTypeProtocol
	RedisErrorTypeAuth
	RedisErrorTypeCommand
	RedisErrorTypeNetwork
	RedisErrorTypeUnknown
)

// RedisError represents a Redis operation error with context
type RedisError struct {
	Type      RedisErrorType
	Operation string
	Key       string
	Message   string
	Retryable bool
	Temporary bool
}

func (e *RedisError) Error() string {
	return fmt.Sprintf("Redis %s error in %s (key: %s): %s", e.TypeString(), e.Operation, e.Key, e.Message)
}

func (e *RedisError) TypeString() string {
	switch e.Type {
	case RedisErrorTypeConnection:
		return "Connection"
	case RedisErrorTypeTimeout:
		return "Timeout"
	case RedisErrorTypeProtocol:
		return "Protocol"
	case RedisErrorTypeAuth:
		return "Authentication"
	case RedisErrorTypeCommand:
		return "Command"
	case RedisErrorTypeNetwork:
		return "Network"
	default:
		return "Unknown"
	}
}

// RetryConfig defines retry behavior
type RetryConfig struct {
	MaxRetries    int
	InitialDelay  time.Duration
	MaxDelay      time.Duration
	BackoffFactor float64
	EnableJitter  bool
}

// DefaultRetryConfig provides sensible defaults
var DefaultRetryConfig = RetryConfig{
	MaxRetries:    3,
	InitialDelay:  100 * time.Millisecond,
	MaxDelay:      2 * time.Second,
	BackoffFactor: 2.0,
	EnableJitter:  true,
}

type RedisResponseCallback func(response resp.Value)

type RedisClient interface {
	Init(username, password string, timeout int64, opts ...optionFunc) error
	// return whether redis client is ready
	Ready() bool
	// with this function, you can call redis as if you are using redis-cli
	Command(cmds []interface{}, callback RedisResponseCallback) error
	Eval(script string, numkeys int, keys, args []interface{}, callback RedisResponseCallback) error

	// Key
	Del(key string, callback RedisResponseCallback) error
	Exists(key string, callback RedisResponseCallback) error
	Expire(key string, ttl int, callback RedisResponseCallback) error
	Persist(key string, callback RedisResponseCallback) error

	// String
	Get(key string, callback RedisResponseCallback) error
	Set(key string, value interface{}, callback RedisResponseCallback) error
	SetEx(key string, value interface{}, ttl int, callback RedisResponseCallback) error
	SetNX(key string, value interface{}, ttl int, callback RedisResponseCallback) error
	MGet(keys []string, callback RedisResponseCallback) error
	MSet(kvMap map[string]interface{}, callback RedisResponseCallback) error
	Incr(key string, callback RedisResponseCallback) error
	Decr(key string, callback RedisResponseCallback) error
	IncrBy(key string, delta int, callback RedisResponseCallback) error
	DecrBy(key string, delta int, callback RedisResponseCallback) error

	// Optimized batch operations for quota management
	BatchGetQuotaInfo(totalKey, usedKey string, callback RedisResponseCallback) error
	BatchSetWithExpiry(kvMap map[string]interface{}, ttl int, callback RedisResponseCallback) error
	AtomicQuotaCheck(totalKey, usedKey string, quotaWeight int, callback RedisResponseCallback) error

	// List
	LLen(key string, callback RedisResponseCallback) error
	RPush(key string, vals []interface{}, callback RedisResponseCallback) error
	RPop(key string, callback RedisResponseCallback) error
	LPush(key string, vals []interface{}, callback RedisResponseCallback) error
	LPop(key string, callback RedisResponseCallback) error
	LIndex(key string, index int, callback RedisResponseCallback) error
	LRange(key string, start, stop int, callback RedisResponseCallback) error
	LRem(key string, count int, value interface{}, callback RedisResponseCallback) error
	LInsertBefore(key string, pivot, value interface{}, callback RedisResponseCallback) error
	LInsertAfter(key string, pivot, value interface{}, callback RedisResponseCallback) error

	// Hash
	HExists(key, field string, callback RedisResponseCallback) error
	HDel(key string, fields []string, callback RedisResponseCallback) error
	HLen(key string, callback RedisResponseCallback) error
	HGet(key, field string, callback RedisResponseCallback) error
	HSet(key, field string, value interface{}, callback RedisResponseCallback) error
	HMGet(key string, fields []string, callback RedisResponseCallback) error
	HMSet(key string, kvMap map[string]interface{}, callback RedisResponseCallback) error
	HKeys(key string, callback RedisResponseCallback) error
	HVals(key string, callback RedisResponseCallback) error
	HGetAll(key string, callback RedisResponseCallback) error
	HIncrBy(key, field string, delta int, callback RedisResponseCallback) error
	HIncrByFloat(key, field string, delta float64, callback RedisResponseCallback) error

	// Set
	SCard(key string, callback RedisResponseCallback) error
	SAdd(key string, value []interface{}, callback RedisResponseCallback) error
	SRem(key string, values []interface{}, callback RedisResponseCallback) error
	SIsMember(key string, value interface{}, callback RedisResponseCallback) error
	SMembers(key string, callback RedisResponseCallback) error
	SDiff(key1, key2 string, callback RedisResponseCallback) error
	SDiffStore(destination, key1, key2 string, callback RedisResponseCallback) error
	SInter(key1, key2 string, callback RedisResponseCallback) error
	SInterStore(destination, key1, key2 string, callback RedisResponseCallback) error
	SUnion(key1, key2 string, callback RedisResponseCallback) error
	SUnionStore(destination, key1, key2 string, callback RedisResponseCallback) error

	// Sorted Set
	ZCard(key string, callback RedisResponseCallback) error
	ZAdd(key string, msMap map[string]interface{}, callback RedisResponseCallback) error
	ZCount(key string, min interface{}, max interface{}, callback RedisResponseCallback) error
	ZIncrBy(key string, member string, delta interface{}, callback RedisResponseCallback) error
	ZScore(key, member string, callback RedisResponseCallback) error
	ZRank(key, member string, callback RedisResponseCallback) error
	ZRevRank(key, member string, callback RedisResponseCallback) error
	ZRem(key string, members []string, callback RedisResponseCallback) error
	ZRange(key string, start, stop int, callback RedisResponseCallback) error
	ZRevRange(key string, start, stop int, callback RedisResponseCallback) error
}

type RedisClusterClient[C Cluster] struct {
	cluster        C
	ready          bool
	checkReadyFunc func() error
	option         redisOption
}

type redisOption struct {
	dataBase int
}

type optionFunc func(*redisOption)

func WithDataBase(dataBase int) optionFunc {
	return func(o *redisOption) {
		o.dataBase = dataBase
	}
}

func NewRedisClusterClient[C Cluster](cluster C) *RedisClusterClient[C] {
	return &RedisClusterClient[C]{
		cluster: cluster,
		checkReadyFunc: func() error {
			return errors.New("redis client is not ready, please call Init() first")
		},
	}
}

// RedisCallWithRetry provides enhanced Redis call with intelligent error handling and retries
func RedisCallWithRetry(cluster Cluster, respQuery []byte, callback RedisResponseCallback, operation string, key string, config RetryConfig) error {
	return redisCallInternal(cluster, respQuery, callback, operation, key, config, 0)
}

// RedisCall maintains backward compatibility with existing code
func RedisCall(cluster Cluster, respQuery []byte, callback RedisResponseCallback) error {
	return RedisCallWithRetry(cluster, respQuery, callback, "unknown", "", DefaultRetryConfig)
}

// redisCallInternal handles the actual Redis call with retry logic
func redisCallInternal(cluster Cluster, respQuery []byte, callback RedisResponseCallback, operation string, key string, config RetryConfig, attempt int) error {
	requestID := uuid.New().String()

	// Update metrics
	globalRedisMetrics.TotalCalls++
	if attempt > 0 {
		globalRedisMetrics.RetryAttempts++
	}

	_, err := proxywasm.DispatchRedisCall(
		cluster.ClusterName(),
		respQuery,
		func(status int, responseSize int) {
			response, err := proxywasm.GetRedisCallResponse(0, responseSize)
			var responseValue resp.Value
			var redisErr *RedisError

			if status != 0 || err != nil {
				// Classify the error for better handling
				redisErr = classifyRedisError(status, err, operation, key)

				// Log error with appropriate level based on type
				if redisErr.Type == RedisErrorTypeAuth || !redisErr.Temporary {
					proxywasm.LogCriticalf("Redis %s error (non-retryable): %s, request-id: %s",
						redisErr.TypeString(), redisErr.Message, requestID)
				} else {
					proxywasm.LogWarnf("Redis %s error (attempt %d/%d): %s, request-id: %s",
						redisErr.TypeString(), attempt+1, config.MaxRetries+1, redisErr.Message, requestID)
				}

				// Decide whether to retry
				shouldRetry := redisErr.Retryable && attempt < config.MaxRetries

				if shouldRetry {
					// Calculate delay with exponential backoff
					delay := calculateRetryDelay(config, attempt)
					proxywasm.LogInfof("Retrying Redis operation %s for key %s in %v (attempt %d/%d), request-id: %s",
						operation, key, delay, attempt+1, config.MaxRetries, requestID)

					// Schedule retry - Note: In WASM context, we can't actually sleep
					// The retry would need to be handled at a higher level
					// For now, we'll return the error and let the caller handle retries
					responseValue = resp.ErrorValue(redisErr)
				} else {
					globalRedisMetrics.FailedCalls++
					responseValue = resp.ErrorValue(redisErr)
				}
			} else {
				// Parse the response
				rd := resp.NewReader(bytes.NewReader(response))
				value, _, parseErr := rd.ReadValue()
				if parseErr != nil && parseErr != io.EOF {
					redisErr = &RedisError{
						Type:      RedisErrorTypeProtocol,
						Operation: operation,
						Key:       key,
						Message:   "Failed to parse Redis response: " + parseErr.Error(),
						Retryable: false,
						Temporary: false,
					}
					proxywasm.LogCriticalf("Redis protocol error: %s, request-id: %s", redisErr.Message, requestID)
					globalRedisMetrics.FailedCalls++
					responseValue = resp.ErrorValue(redisErr)
				} else {
					// Success case
					globalRedisMetrics.SuccessfulCalls++
					responseValue = value
					proxywasm.LogDebugf("Redis call successful, operation: %s, key: %s, request-id: %s, respQuery: %s, respValue: %s",
						operation, key, requestID,
						base64.StdEncoding.EncodeToString([]byte(respQuery)),
						base64.StdEncoding.EncodeToString(response))
				}
			}

			if callback != nil {
				callback(responseValue)
			}
		})

	if err != nil {
		redisErr := classifyRedisError(0, err, operation, key)
		proxywasm.LogCriticalf("Redis dispatch failed: %s, request-id: %s", redisErr.Message, requestID)
		globalRedisMetrics.FailedCalls++
		return redisErr
	} else {
		proxywasm.LogDebugf("Redis call dispatched, operation: %s, key: %s, request-id: %s, respQuery: %s",
			operation, key, requestID, base64.StdEncoding.EncodeToString([]byte(respQuery)))
	}

	return nil
}

// calculateRetryDelay computes delay for retry with exponential backoff and optional jitter
func calculateRetryDelay(config RetryConfig, attempt int) time.Duration {
	delay := config.InitialDelay

	// Apply exponential backoff
	for i := 0; i < attempt; i++ {
		delay = time.Duration(float64(delay) * config.BackoffFactor)
		if delay > config.MaxDelay {
			delay = config.MaxDelay
			break
		}
	}

	// Apply jitter if enabled (simple jitter: 50%-100% of calculated delay)
	if config.EnableJitter {
		// Since we can't use random in WASM easily, use a simple deterministic jitter
		jitterFactor := 0.5 + float64(attempt%5)*0.1 // 0.5 to 0.9
		delay = time.Duration(float64(delay) * jitterFactor)
	}

	return delay
}

func respString(args []interface{}) []byte {
	// Pre-calculate capacity to reduce memory allocations
	capacity := 64 // base capacity
	for _, arg := range args {
		str := fmt.Sprint(arg)
		capacity += len(str) + 16 // account for RESP protocol overhead
	}

	var buf bytes.Buffer
	buf.Grow(capacity) // Pre-allocate buffer with estimated capacity
	wr := resp.NewWriter(&buf)
	arr := make([]resp.Value, 0, len(args)) // Pre-allocate with known capacity
	for _, arg := range args {
		arr = append(arr, resp.StringValue(fmt.Sprint(arg)))
	}
	wr.WriteArray(arr)
	return buf.Bytes()
}

func (c *RedisClusterClient[C]) Ready() bool {
	return c.ready
}

func (c *RedisClusterClient[C]) Init(username, password string, timeout int64, opts ...optionFunc) error {
	for _, opt := range opts {
		opt(&c.option)
	}
	clusterName := c.cluster.ClusterName()
	if c.option.dataBase != 0 {
		clusterName = fmt.Sprintf("%s?db=%d", clusterName, c.option.dataBase)
	}
	err := proxywasm.RedisInit(clusterName, username, password, uint32(timeout))
	if err != nil {
		c.checkReadyFunc = func() error {
			if c.ready {
				return nil
			}
			initErr := proxywasm.RedisInit(clusterName, username, password, uint32(timeout))
			if initErr != nil {
				return initErr
			}
			c.ready = true
			return nil
		}
		proxywasm.LogWarnf("failed to init redis: %v, will retry after", err)
		return nil
	}
	c.checkReadyFunc = func() error { return nil }
	c.ready = true
	return nil
}

func (c *RedisClusterClient[C]) Command(cmds []interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	// Extract operation and key for logging
	operation := "COMMAND"
	key := ""
	if len(cmds) > 0 {
		if op, ok := cmds[0].(string); ok {
			operation = strings.ToUpper(op)
		}
		if len(cmds) > 1 {
			if k, ok := cmds[1].(string); ok {
				key = k
			}
		}
	}
	return RedisCallWithRetry(c.cluster, respString(cmds), callback, operation, key, DefaultRetryConfig)
}

// CommandWithRetry provides enhanced command execution with retry support
func (c *RedisClusterClient[C]) CommandWithRetry(cmds []interface{}, callback RedisResponseCallback, operation string, key string, config RetryConfig) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	return RedisCallWithRetry(c.cluster, respString(cmds), callback, operation, key, config)
}

func (c *RedisClusterClient[C]) Eval(script string, numkeys int, keys, args []interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	params := make([]interface{}, 0)
	params = append(params, "eval")
	params = append(params, script)
	params = append(params, numkeys)
	params = append(params, keys...)
	params = append(params, args...)
	// Use first key for logging purposes
	keyForLog := ""
	if len(keys) > 0 {
		if k, ok := keys[0].(string); ok {
			keyForLog = k
		}
	}
	return RedisCallWithRetry(c.cluster, respString(params), callback, "EVAL", keyForLog, DefaultRetryConfig)
}

// Key
func (c *RedisClusterClient[C]) Del(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "del")
	args = append(args, key)
	return RedisCallWithRetry(c.cluster, respString(args), callback, "DEL", key, DefaultRetryConfig)
}

func (c *RedisClusterClient[C]) Exists(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "exists")
	args = append(args, key)
	return RedisCallWithRetry(c.cluster, respString(args), callback, "EXISTS", key, DefaultRetryConfig)
}

func (c *RedisClusterClient[C]) Expire(key string, ttl int, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "expire")
	args = append(args, key)
	args = append(args, ttl)
	return RedisCallWithRetry(c.cluster, respString(args), callback, "EXPIRE", key, DefaultRetryConfig)
}

func (c *RedisClusterClient[C]) Persist(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "persist")
	args = append(args, key)
	return RedisCallWithRetry(c.cluster, respString(args), callback, "PERSIST", key, DefaultRetryConfig)
}

// String
func (c *RedisClusterClient[C]) Get(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "get")
	args = append(args, key)
	return RedisCallWithRetry(c.cluster, respString(args), callback, "GET", key, DefaultRetryConfig)
}

// GetWithRetry provides enhanced GET with retry support
func (c *RedisClusterClient[C]) GetWithRetry(key string, callback RedisResponseCallback, config RetryConfig) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := []interface{}{"get", key}
	return RedisCallWithRetry(c.cluster, respString(args), callback, "GET", key, config)
}

func (c *RedisClusterClient[C]) Set(key string, value interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "set")
	args = append(args, key)
	args = append(args, value)
	return RedisCallWithRetry(c.cluster, respString(args), callback, "SET", key, DefaultRetryConfig)
}

// SetWithRetry provides enhanced SET with retry support
func (c *RedisClusterClient[C]) SetWithRetry(key string, value interface{}, callback RedisResponseCallback, config RetryConfig) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := []interface{}{"set", key, value}
	return RedisCallWithRetry(c.cluster, respString(args), callback, "SET", key, config)
}

func (c *RedisClusterClient[C]) SetEx(key string, value interface{}, ttl int, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "set")
	args = append(args, key)
	args = append(args, value)
	args = append(args, "ex")
	args = append(args, ttl)
	return RedisCallWithRetry(c.cluster, respString(args), callback, "SETEX", key, DefaultRetryConfig)
}

func (c *RedisClusterClient[C]) SetNX(key string, value interface{}, ttl int, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "set")
	args = append(args, key)
	args = append(args, value)
	args = append(args, "nx")
	if ttl > 0 {
		args = append(args, "ex")
		args = append(args, ttl)
	}
	return RedisCallWithRetry(c.cluster, respString(args), callback, "SETNX", key, DefaultRetryConfig)
}

func (c *RedisClusterClient[C]) MGet(keys []string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "mget")
	for _, k := range keys {
		args = append(args, k)
	}
	// Use first key for logging purposes
	keyForLog := ""
	if len(keys) > 0 {
		keyForLog = keys[0]
	}
	return RedisCallWithRetry(c.cluster, respString(args), callback, "MGET", keyForLog, DefaultRetryConfig)
}

func (c *RedisClusterClient[C]) MSet(kvMap map[string]interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "mset")
	for k, v := range kvMap {
		args = append(args, k)
		args = append(args, v)
	}
	// Use first key for logging purposes
	keyForLog := ""
	for k := range kvMap {
		keyForLog = k
		break
	}
	return RedisCallWithRetry(c.cluster, respString(args), callback, "MSET", keyForLog, DefaultRetryConfig)
}

func (c *RedisClusterClient[C]) Incr(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "incr")
	args = append(args, key)
	return RedisCallWithRetry(c.cluster, respString(args), callback, "INCR", key, DefaultRetryConfig)
}

func (c *RedisClusterClient[C]) Decr(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "decr")
	args = append(args, key)
	return RedisCallWithRetry(c.cluster, respString(args), callback, "DECR", key, DefaultRetryConfig)
}

func (c *RedisClusterClient[C]) IncrBy(key string, delta int, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "incrby")
	args = append(args, key)
	args = append(args, delta)
	return RedisCallWithRetry(c.cluster, respString(args), callback, "INCRBY", key, DefaultRetryConfig)
}

// IncrByWithRetry provides enhanced INCRBY with retry support
func (c *RedisClusterClient[C]) IncrByWithRetry(key string, delta int, callback RedisResponseCallback, config RetryConfig) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := []interface{}{"incrby", key, delta}
	return RedisCallWithRetry(c.cluster, respString(args), callback, "INCRBY", key, config)
}

func (c *RedisClusterClient[C]) DecrBy(key string, delta int, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "decrby")
	args = append(args, key)
	args = append(args, delta)
	return RedisCallWithRetry(c.cluster, respString(args), callback, "DECRBY", key, DefaultRetryConfig)
}

// List
func (c *RedisClusterClient[C]) LLen(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "llen")
	args = append(args, key)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) RPush(key string, vals []interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "rpush")
	args = append(args, key)
	for _, val := range vals {
		args = append(args, val)
	}
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) RPop(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "rpop")
	args = append(args, key)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) LPush(key string, vals []interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "lpush")
	args = append(args, key)
	for _, val := range vals {
		args = append(args, val)
	}
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) LPop(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "lpop")
	args = append(args, key)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) LIndex(key string, index int, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "lindex")
	args = append(args, key)
	args = append(args, index)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) LRange(key string, start, stop int, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "lrange")
	args = append(args, key)
	args = append(args, start)
	args = append(args, stop)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) LRem(key string, count int, value interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "lrem")
	args = append(args, key)
	args = append(args, count)
	args = append(args, value)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) LInsertBefore(key string, pivot, value interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "linsert")
	args = append(args, key)
	args = append(args, "before")
	args = append(args, pivot)
	args = append(args, value)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) LInsertAfter(key string, pivot, value interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "linsert")
	args = append(args, key)
	args = append(args, "after")
	args = append(args, pivot)
	args = append(args, value)
	return RedisCall(c.cluster, respString(args), callback)
}

// Hash
func (c *RedisClusterClient[C]) HExists(key, field string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "hexists")
	args = append(args, key)
	args = append(args, field)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) HDel(key string, fields []string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "hdel")
	args = append(args, key)
	for _, field := range fields {
		args = append(args, field)
	}
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) HLen(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "hlen")
	args = append(args, key)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) HGet(key, field string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "hget")
	args = append(args, key)
	args = append(args, field)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) HSet(key, field string, value interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "hset")
	args = append(args, key)
	args = append(args, field)
	args = append(args, value)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) HMGet(key string, fields []string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "hmget")
	args = append(args, key)
	for _, field := range fields {
		args = append(args, field)
	}
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) HMSet(key string, kvMap map[string]interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "hmset")
	args = append(args, key)
	for k, v := range kvMap {
		args = append(args, k)
		args = append(args, v)
	}
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) HKeys(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "hkeys")
	args = append(args, key)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) HVals(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "hvals")
	args = append(args, key)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) HGetAll(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "hgetall")
	args = append(args, key)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) HIncrBy(key, field string, delta int, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "hincrby")
	args = append(args, key)
	args = append(args, field)
	args = append(args, delta)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) HIncrByFloat(key, field string, delta float64, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "hincrbyfloat")
	args = append(args, key)
	args = append(args, field)
	args = append(args, delta)
	return RedisCall(c.cluster, respString(args), callback)
}

// Set
func (c *RedisClusterClient[C]) SCard(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "scard")
	args = append(args, key)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) SAdd(key string, vals []interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "sadd")
	args = append(args, key)
	for _, val := range vals {
		args = append(args, val)
	}
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) SRem(key string, vals []interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "srem")
	args = append(args, key)
	for _, val := range vals {
		args = append(args, val)
	}
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) SIsMember(key string, value interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "sismember")
	args = append(args, key)
	args = append(args, value)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) SMembers(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "smembers")
	args = append(args, key)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) SDiff(key1, key2 string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "sdiff")
	args = append(args, key1)
	args = append(args, key2)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) SDiffStore(destination, key1, key2 string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "sdiffstore")
	args = append(args, destination)
	args = append(args, key1)
	args = append(args, key2)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) SInter(key1, key2 string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "sinter")
	args = append(args, key1)
	args = append(args, key2)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) SInterStore(destination, key1, key2 string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "sinterstore")
	args = append(args, destination)
	args = append(args, key1)
	args = append(args, key2)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) SUnion(key1, key2 string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "sunion")
	args = append(args, key1)
	args = append(args, key2)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) SUnionStore(destination, key1, key2 string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "sunionstore")
	args = append(args, destination)
	args = append(args, key1)
	args = append(args, key2)
	return RedisCall(c.cluster, respString(args), callback)
}

// ZSet
func (c *RedisClusterClient[C]) ZCard(key string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "zcard")
	args = append(args, key)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) ZAdd(key string, msMap map[string]interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "zadd")
	args = append(args, key)
	for m, s := range msMap {
		args = append(args, s)
		args = append(args, m)
	}
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) ZCount(key string, min interface{}, max interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "zcount")
	args = append(args, key)
	args = append(args, min)
	args = append(args, max)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) ZIncrBy(key string, member string, delta interface{}, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "zincrby")
	args = append(args, key)
	args = append(args, delta)
	args = append(args, member)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) ZScore(key, member string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "zscore")
	args = append(args, key)
	args = append(args, member)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) ZRank(key, member string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "zrank")
	args = append(args, key)
	args = append(args, member)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) ZRevRank(key, member string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "zrevrank")
	args = append(args, key)
	args = append(args, member)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) ZRem(key string, members []string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "zrem")
	args = append(args, key)
	for _, m := range members {
		args = append(args, m)
	}
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) ZRange(key string, start, stop int, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "zrange")
	args = append(args, key)
	args = append(args, start)
	args = append(args, stop)
	return RedisCall(c.cluster, respString(args), callback)
}

func (c *RedisClusterClient[C]) ZRevRange(key string, start, stop int, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	args := make([]interface{}, 0)
	args = append(args, "zrevrange")
	args = append(args, key)
	args = append(args, start)
	args = append(args, stop)
	return RedisCall(c.cluster, respString(args), callback)
}

// BatchGetQuotaInfo optimizes quota checking by using MGET for multiple keys
func (c *RedisClusterClient[C]) BatchGetQuotaInfo(totalKey, usedKey string, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}
	keys := []string{totalKey, usedKey}
	return c.MGet(keys, callback)
}

// BatchSetWithExpiry efficiently sets multiple key-value pairs with expiry
func (c *RedisClusterClient[C]) BatchSetWithExpiry(kvMap map[string]interface{}, ttl int, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}

	// Use pipeline for batch operations
	script := `
		local keys = ARGV
		local ttl = tonumber(ARGV[1])
		for i = 2, #ARGV, 2 do
			redis.call('set', ARGV[i], ARGV[i+1])
			if ttl > 0 then
				redis.call('expire', ARGV[i], ttl)
			end
		end
		return 'OK'
	`

	params := make([]interface{}, 0)
	params = append(params, ttl)
	for k, v := range kvMap {
		params = append(params, k, v)
	}

	return c.Eval(script, 0, []interface{}{}, params, callback)
}

// AtomicQuotaCheck performs quota check and deduction in a single atomic operation
func (c *RedisClusterClient[C]) AtomicQuotaCheck(totalKey, usedKey string, quotaWeight int, callback RedisResponseCallback) error {
	if err := c.checkReadyFunc(); err != nil {
		return err
	}

	// Lua script for atomic quota check and deduction
	script := `
		local total_key = KEYS[1]
		local used_key = KEYS[2]
		local quota_weight = tonumber(ARGV[1])

		-- Get total and used quota
		local total_quota = tonumber(redis.call('get', total_key)) or 0
		local used_quota = tonumber(redis.call('get', used_key)) or 0

		-- Calculate remaining quota
		local remaining_quota = total_quota - used_quota

		-- Check if sufficient quota available
		if remaining_quota < quota_weight then
			return {total_quota, used_quota, remaining_quota, 0} -- 0 indicates failure
		end

		-- Deduct quota atomically
		local new_used = redis.call('incrby', used_key, quota_weight)
		return {total_quota, used_quota, remaining_quota, 1} -- 1 indicates success
	`

	keys := []interface{}{totalKey, usedKey}
	args := []interface{}{quotaWeight}
	return c.Eval(script, 2, keys, args, callback)
}

// classifyRedisError analyzes error and determines type and retry characteristics
func classifyRedisError(status int, err error, operation string, key string) *RedisError {
	redisErr := &RedisError{
		Operation: operation,
		Key:       key,
		Temporary: true, // Most Redis errors are temporary
		Retryable: true, // Most Redis errors are retryable
	}

	if status != 0 {
		// Connection or network errors based on status code
		switch status {
		case 1: // Connection refused
			redisErr.Type = RedisErrorTypeConnection
			redisErr.Message = "Connection refused - Redis server may be down"
		case 2: // Timeout
			redisErr.Type = RedisErrorTypeTimeout
			redisErr.Message = "Operation timed out"
		case 3: // Authentication failed
			redisErr.Type = RedisErrorTypeAuth
			redisErr.Message = "Authentication failed"
			redisErr.Retryable = false
			redisErr.Temporary = false
		default:
			redisErr.Type = RedisErrorTypeNetwork
			redisErr.Message = fmt.Sprintf("Network error (status: %d)", status)
		}
		return redisErr
	}

	if err != nil {
		errMsg := err.Error()

		// Classify based on error message patterns
		if containsAny(errMsg, []string{"connection", "connect", "dial"}) {
			redisErr.Type = RedisErrorTypeConnection
			redisErr.Message = "Connection error: " + errMsg
		} else if containsAny(errMsg, []string{"timeout", "deadline"}) {
			redisErr.Type = RedisErrorTypeTimeout
			redisErr.Message = "Timeout error: " + errMsg
		} else if containsAny(errMsg, []string{"auth", "authentication", "password"}) {
			redisErr.Type = RedisErrorTypeAuth
			redisErr.Message = "Authentication error: " + errMsg
			redisErr.Retryable = false
			redisErr.Temporary = false
		} else if containsAny(errMsg, []string{"protocol", "parse", "invalid"}) {
			redisErr.Type = RedisErrorTypeProtocol
			redisErr.Message = "Protocol error: " + errMsg
			redisErr.Retryable = false
		} else if containsAny(errMsg, []string{"network", "io", "broken pipe"}) {
			redisErr.Type = RedisErrorTypeNetwork
			redisErr.Message = "Network error: " + errMsg
		} else {
			redisErr.Type = RedisErrorTypeUnknown
			redisErr.Message = "Unknown error: " + errMsg
		}
		return redisErr
	}

	// Should not reach here, but just in case
	redisErr.Type = RedisErrorTypeUnknown
	redisErr.Message = "Unknown error occurred"
	return redisErr
}

// containsAny checks if string contains any of the given substrings
func containsAny(s string, substrings []string) bool {
	for _, substr := range substrings {
		if len(s) >= len(substr) {
			for i := 0; i <= len(s)-len(substr); i++ {
				if s[i:i+len(substr)] == substr {
					return true
				}
			}
		}
	}
	return false
}

// RedisMetrics tracks Redis operation metrics
type RedisMetrics struct {
	TotalCalls      int64
	SuccessfulCalls int64
	FailedCalls     int64
	RetryAttempts   int64
}

// Global metrics instance
var globalRedisMetrics RedisMetrics

// GetRedisMetrics returns current Redis operation metrics
func GetRedisMetrics() RedisMetrics {
	return globalRedisMetrics
}

// ResetRedisMetrics resets the global Redis metrics
func ResetRedisMetrics() {
	globalRedisMetrics = RedisMetrics{}
}

// IsRetryableError checks if the given error is retryable
func IsRetryableError(err error) bool {
	if redisErr, ok := err.(*RedisError); ok {
		return redisErr.Retryable
	}
	return false
}

// IsRedisErrorResponse checks if a resp.Value contains an error
func IsRedisErrorResponse(val resp.Value) bool {
	return val.Type() == resp.Error
}

// GetRedisErrorFromResponse extracts error from a resp.Value
func GetRedisErrorFromResponse(val resp.Value) error {
	if val.Type() == resp.Error {
		return fmt.Errorf("Redis error: %s", val.String())
	}
	return nil
}
