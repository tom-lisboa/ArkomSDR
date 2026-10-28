package contextworker

import (
	"arkom/bia/internal/store"
	"context"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"testing"
	"time"
)

func TestBufferVersionFence(t *testing.T) {
	r := miniredis.RunT(t)
	c := redis.NewClient(&redis.Options{Addr: r.Addr()})
	defer c.Close()
	b := Buffer{Client: c}
	ctx := context.Background()
	now := time.Now()
	old := store.BufferHint{ID: "conversation", Version: 1, MessageID: "one", Due: now.Add(-time.Second)}
	newer := store.BufferHint{ID: "conversation", Version: 2, MessageID: "two", Due: now.Add(time.Second)}
	for _, h := range []store.BufferHint{old, newer, old} {
		if e := b.Touch(ctx, h); e != nil {
			t.Fatal(e)
		}
	}
	if e := b.Clear(ctx, old); e != nil {
		t.Fatal(e)
	}
	if id, e := b.Ready(ctx, now); e != nil || id != "" {
		t.Fatalf("debounce lost: %s %v", id, e)
	}
	if id, e := b.Ready(ctx, now.Add(2*time.Second)); e != nil || id != "conversation" {
		t.Fatalf("newer buffer deleted: %s %v", id, e)
	}
	if n := c.SCard(ctx, "bia:messages:conversation").Val(); n != 2 {
		t.Fatal(n)
	}
	if e := b.Clear(ctx, newer); e != nil {
		t.Fatal(e)
	}
	if id, e := b.Ready(ctx, now.Add(3*time.Second)); e != nil || id != "" {
		t.Fatal(id, e)
	}
}
