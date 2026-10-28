package contextworker

import (
	"arkom/bia/internal/store"
	"context"
	"github.com/redis/go-redis/v9"
	"time"
)

// Redis is an acceleration layer. Postgres owns the durable input and deadline.
type Buffer struct{ Client *redis.Client }

func (b *Buffer) Touch(ctx context.Context, h store.BufferHint) error {
	if h.ID == "" || h.Due.IsZero() {
		return nil
	}
	return b.Client.Eval(ctx, `local old=tonumber(redis.call('HGET',KEYS[1],'version') or '-1')
 if old>tonumber(ARGV[1]) then return 0 end
 redis.call('HSET',KEYS[1],'version',ARGV[1])
 redis.call('EXPIRE',KEYS[1],86400)
 if ARGV[4]~='' then redis.call('SADD',KEYS[3],ARGV[4]); redis.call('EXPIRE',KEYS[3],86400) end
 redis.call('ZADD',KEYS[2],ARGV[2],ARGV[3]); return 1`, []string{"bia:buffer:" + h.ID, "bia:due", "bia:messages:" + h.ID}, h.Version, h.Due.UnixMilli(), h.ID, h.MessageID).Err()
}
func (b *Buffer) Ready(ctx context.Context, now time.Time) (string, error) {
	r, e := b.Client.ZRangeByScore(ctx, "bia:due", &redis.ZRangeBy{Min: "-inf", Max: formatMillis(now), Count: 1}).Result()
	if e != nil || len(r) == 0 {
		return "", e
	}
	return r[0], nil
}
func (b *Buffer) Clear(ctx context.Context, h store.BufferHint) error {
	return b.Client.Eval(ctx, `local old=tonumber(redis.call('HGET',KEYS[1],'version') or '-1')
 if old>tonumber(ARGV[1]) then return 0 end
 redis.call('DEL',KEYS[1],KEYS[3]);redis.call('ZREM',KEYS[2],ARGV[2]);return 1`, []string{"bia:buffer:" + h.ID, "bia:due", "bia:messages:" + h.ID}, h.Version, h.ID).Err()
}
