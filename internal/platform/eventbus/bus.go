package eventbus

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

type EventHandler func(ctx context.Context, event *Event) error
type EventBus interface {
	Publish(ctx context.Context, event *Event) error
	Subscribe(eventType EventType, handler EventHandler) (cleanup func(), err error)
	StartConsumers(ctx context.Context) error
	Close() error
}

type redisEventBus struct {
	mu          sync.RWMutex
	client      *redis.Client
	handlers    map[EventType][]EventHandler
	consumers   []*streamConsumer
	started     bool
	stopOnce    sync.Once
	consumerWg  sync.WaitGroup
}

func NewRedisEventBus(client *redis.Client) EventBus {
	return &redisEventBus{client: client, handlers: make(map[EventType][]EventHandler)}
}

func (b *redisEventBus) Publish(ctx context.Context, event *Event) error {
	if event == nil {
		return fmt.Errorf("eventbus: nil event")
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("eventbus: marshal event: %w", err)
	}
	err = b.client.XAdd(ctx, &redis.XAddArgs{
		Stream: event.Type.StreamName(), MaxLen: 10000, Approx: true,
		Values: map[string]interface{}{
			"raw":        string(raw),
			"event_id":   event.ID.String(),
			"event_type": string(event.Type),
			"shop_id":    event.ShopID.String(),
		},
	}).Err()
	if err != nil {
		slog.Error("event", "type", string(event.Type), "action", "publish_failed", "shop_id", event.ShopID.String(), "error", err)
		return err
	}
	slog.Info("event", "type", string(event.Type), "action", "published", "shop_id", event.ShopID.String())
	return nil
}

func (b *redisEventBus) Subscribe(et EventType, h EventHandler) (func(), error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// 防止在启动后再添加订阅
	if b.started {
		return nil, fmt.Errorf("eventbus: cannot subscribe after StartConsumers")
	}

	b.handlers[et] = append(b.handlers[et], h)
	idx := len(b.handlers[et]) - 1 // index of the just-added handler

	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if handlers, ok := b.handlers[et]; ok && idx < len(handlers) {
			b.handlers[et] = append(handlers[:idx], handlers[idx+1:]...)
		}
	}, nil
}

func (b *redisEventBus) StartConsumers(ctx context.Context) error {
	b.mu.Lock()
	if b.started {
		b.mu.Unlock()
		return fmt.Errorf("eventbus: consumers already started")
	}
	b.started = true

	handlersSnapshot := make(map[EventType][]EventHandler)
	for et, hs := range b.handlers {
		if len(hs) > 0 {
			// 创建副本，防止后续修改
			handlersSnapshot[et] = append([]EventHandler{}, hs...)
		}
	}
	b.mu.Unlock()

	for et, hs := range handlersSnapshot {
		c := newStreamConsumer(b.client, et, hs)
		b.mu.Lock()
		b.consumers = append(b.consumers, c)
		b.mu.Unlock()

		b.consumerWg.Add(1)
		go func(consumer *streamConsumer) {
			defer b.consumerWg.Done()
			consumer.run(ctx)
		}(c)
	}
	return nil
}

func (b *redisEventBus) Close() error {
	b.mu.Lock()
	consumers := append([]*streamConsumer{}, b.consumers...)
	b.mu.Unlock()

	// 停止所有消费者
	for _, c := range consumers {
		c.stop()
	}

	// 等待所有消费者 goroutine 完成
	done := make(chan struct{})
	go func() {
		b.consumerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		return nil
	case <-time.After(10 * time.Second):
		return fmt.Errorf("eventbus: close timeout waiting for consumers")
	}
}

type streamConsumer struct {
	client       *redis.Client
	eventType    EventType
	handlers     []EventHandler
	stopCh       chan struct{}
	consumerID   string
	groupCreated bool
	mu           sync.Mutex
}

func newStreamConsumer(c *redis.Client, et EventType, hs []EventHandler) *streamConsumer {
	return &streamConsumer{
		client:    c,
		eventType: et,
		handlers:  hs,
		stopCh:    make(chan struct{}),
		consumerID: fmt.Sprintf("c-%s-%d", et, time.Now().UnixNano()),
	}
}

func (c *streamConsumer) run(ctx context.Context) {
	sn := c.eventType.StreamName()
	gn := ConsumerGroupName("vela-core")

	// 创建消费者组（仅创建一次）
	if !c.ensureGroupCreated(ctx, sn, gn) {
		slog.Error("eventbus: failed to create consumer group", "stream", sn)
		return
	}

	for {
		select {
		case <-c.stopCh:
			return
		case <-ctx.Done():
			return
		default:
		}

		streams, err := c.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group:    gn,
			Consumer: c.consumerID,
			Streams:  []string{sn, ">"},
			Count:    10,
			Block:    2 * time.Second,
		}).Result()

		if err != nil {
			if err == redis.Nil {
				// 超时，继续
				continue
			}
			slog.Error("eventbus: XReadGroup error", "stream", sn, "error", err)
			time.Sleep(time.Second)
			continue
		}

		if len(streams) == 0 {
			continue
		}

		c.processMessages(ctx, sn, gn, streams)
	}
}

// ensureGroupCreated 确保消费者组仅创建一次
func (c *streamConsumer) ensureGroupCreated(ctx context.Context, streamName, groupName string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.groupCreated {
		return true
	}

	err := c.client.XGroupCreateMkStream(ctx, streamName, groupName, "0").Err()
	if err != nil {
		// BUSYGROUP 错误表示组已存在，这是预期的
		if strings.Contains(err.Error(), "BUSYGROUP") {
			c.groupCreated = true
			return true
		}
		slog.Error("eventbus: failed to create consumer group", "group", groupName, "stream", streamName, "error", err)
		return false
	}

	c.groupCreated = true
	return true
}

// processMessages 处理消息并返回处理成功的消息 ID
func (c *streamConsumer) processMessages(ctx context.Context, streamName, groupName string, streams []redis.XStream) {
	for _, s := range streams {
		for _, msg := range s.Messages {
			raw, ok := msg.Values["raw"].(string)
			if !ok {
				slog.Error("eventbus: missing or invalid 'raw' field", "msg_id", msg.ID)
				// 虽然数据无效，但仍然确认以避免重复处理
				c.client.XAck(ctx, streamName, groupName, msg.ID)
				continue
			}

			var event Event
			if err := json.Unmarshal([]byte(raw), &event); err != nil {
				slog.Error("eventbus: failed to unmarshal event", "msg_id", msg.ID, "error", err)
				// 数据损坏，确认并继续（不重新处理）
				c.client.XAck(ctx, streamName, groupName, msg.ID)
				continue
			}

			// 处理事件
			if c.handleEvent(ctx, &event) {
				// 仅在所有处理器成功时确认
				c.client.XAck(ctx, streamName, groupName, msg.ID)
				slog.Info("event", "type", string(event.Type), "action", "consumed", "shop_id", event.ShopID.String())
			} else {
				slog.Warn("eventbus: event handling failed, will be retried", "event_id", event.ID)
			}
		}
	}
}

// handleEvent 执行所有处理器，返回是否全部成功
func (c *streamConsumer) handleEvent(ctx context.Context, event *Event) bool {
	for _, h := range c.handlers {
		if !c.executeHandler(ctx, h, event) {
			return false
		}
	}
	return true
}

// executeHandler 安全地执行单个处理器
func (c *streamConsumer) executeHandler(ctx context.Context, handler EventHandler, event *Event) bool {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("eventbus: handler panic", "event_id", event.ID, "panic", r)
		}
	}()

	if err := handler(ctx, event); err != nil {
		slog.Error("eventbus: handler error", "event_id", event.ID, "error", err)
		return false
	}
	return true
}

func (c *streamConsumer) stop() {
	close(c.stopCh)
}
