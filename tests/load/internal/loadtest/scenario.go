package loadtest

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"strings"
	"sync"
	"time"
)

// 工作负载运行函数按照统一开始时间、升压窗口和运行时长执行任务。
func RunWorkload(ctx context.Context, config WorkloadConfig, metrics *Metrics) error {
	if err := config.Validate(); err != nil {
		return err
	}
	if metrics == nil {
		return errors.New("指标集合不能为空")
	}
	if config.StartAt.IsZero() {
		config.StartAt = time.Now().UTC()
	}
	if wait := time.Until(config.StartAt); wait > 0 {
		timer := time.NewTimer(wait)
		select {
		case <-timer.C:
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		}
	}

	endAt := config.StartAt.Add(config.Ramp).Add(config.Duration)
	runContext, cancel := context.WithDeadline(ctx, endAt)
	defer cancel()

	cache := &tokenCache{}
	var users sync.WaitGroup
	for index := 0; index < config.Sessions; index++ {
		launchAt := config.StartAt
		if config.Ramp > 0 {
			launchAt = launchAt.Add(
				time.Duration(index) * config.Ramp / time.Duration(config.Sessions),
			)
		}
		if wait := time.Until(launchAt); wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-runContext.Done():
				timer.Stop()
				break
			}
		}
		if runContext.Err() != nil {
			break
		}

		account := config.Accounts[index%len(config.Accounts)]
		users.Add(1)
		go func(userIndex int, userAccount Account) {
			defer users.Done()
			runVirtualUser(runContext, config, metrics, cache, userIndex, userAccount)
		}(index, account)
	}

	completed := make(chan struct{})
	go func() {
		users.Wait()
		close(completed)
	}()

	select {
	case <-completed:
	case <-runContext.Done():
		<-completed
	}
	if metrics.connectionsSucceeded.Load() == 0 {
		return errors.New("没有连接成功建立")
	}
	if ctx.Err() != nil && time.Now().Before(endAt) {
		return ctx.Err()
	}
	return nil
}

func runVirtualUser(
	ctx context.Context,
	config WorkloadConfig,
	metrics *Metrics,
	cache *tokenCache,
	userIndex int,
	account Account,
) {
	metrics.connectionsAttempted.Add(1)
	requestPrefix := fmt.Sprintf("%s-%s-%d", config.RunID, config.WorkerID, userIndex)
	client, err := dialWebSocketClient(
		ctx,
		config,
		requestPrefix,
		func(data wireData) {
			recordDelivery(config.RunID, metrics, data)
		},
	)
	if err != nil {
		metrics.RecordError("connect")
		return
	}
	metrics.connectionsSucceeded.Add(1)
	metrics.activeConnections.Add(1)
	defer func() {
		client.Close()
		metrics.activeConnections.Add(-1)
	}()

	if err = client.handshake(ctx, config.ProtocolVersion); err != nil {
		metrics.RecordError("handshake")
		return
	}
	if err = client.login(ctx, account, cache); err != nil {
		metrics.RecordError("login")
		return
	}
	metrics.loginsSucceeded.Add(1)

	random := rand.New(rand.NewSource(time.Now().UnixNano() + int64(userIndex)*7919))
	switch config.Scenario {
	case ScenarioMe:
		runMeScenario(ctx, client, metrics)
	case ScenarioHotTopic:
		runHotTopicScenario(ctx, config, client, metrics, random, userIndex)
	case ScenarioMixed:
		runMixedScenario(ctx, config, client, metrics, random, userIndex)
	}
}

func runMeScenario(ctx context.Context, client *websocketClient, metrics *Metrics) {
	if err := client.subscribe(ctx, "me"); err != nil {
		metrics.RecordError("subscribe_me")
		return
	}
	metrics.subscriptions.Add(1)
	<-ctx.Done()
}

func runHotTopicScenario(
	ctx context.Context,
	config WorkloadConfig,
	client *websocketClient,
	metrics *Metrics,
	random *rand.Rand,
	userIndex int,
) {
	if err := client.subscribe(ctx, config.Topic); err != nil {
		metrics.RecordError("subscribe_hot_topic")
		return
	}
	metrics.subscriptions.Add(1)
	if !waitUntil(ctx, config.StartAt.Add(config.Ramp)) {
		return
	}
	publishMessages(ctx, config, client, metrics, random, userIndex, config.Topic)
	<-ctx.Done()
}

func waitUntil(ctx context.Context, target time.Time) bool {
	wait := time.Until(target)
	if wait <= 0 {
		return ctx.Err() == nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}

func runMixedScenario(
	ctx context.Context,
	config WorkloadConfig,
	client *websocketClient,
	metrics *Metrics,
	random *rand.Rand,
	userIndex int,
) {
	if err := client.subscribe(ctx, "me"); err != nil {
		metrics.RecordError("subscribe_me")
		return
	}
	metrics.subscriptions.Add(1)

	topics, err := client.subscriptions(ctx)
	if err != nil {
		metrics.RecordError("get_subscriptions")
		return
	}
	random.Shuffle(len(topics), func(left, right int) {
		topics[left], topics[right] = topics[right], topics[left]
	})
	if config.MaxTopics > 0 && len(topics) > config.MaxTopics {
		topics = topics[:config.MaxTopics]
	}

	for _, topic := range topics {
		if ctx.Err() != nil || topic == "" || topic == "me" || topic == "fnd" {
			continue
		}
		if err = client.subscribe(ctx, topic); err != nil {
			metrics.RecordError("subscribe_topic")
			continue
		}
		metrics.subscriptions.Add(1)
		if !strings.HasPrefix(topic, "chn") {
			publishMessages(ctx, config, client, metrics, random, userIndex, topic)
		}
		if err = client.leave(ctx, topic); err != nil && ctx.Err() == nil {
			metrics.RecordError("leave_topic")
		}
	}
}

func publishMessages(
	ctx context.Context,
	config WorkloadConfig,
	client *websocketClient,
	metrics *Metrics,
	random *rand.Rand,
	userIndex int,
	topic string,
) {
	for index := 0; index < config.PublishCount && ctx.Err() == nil; index++ {
		sentAt := time.Now()
		clientID := fmt.Sprintf(
			"%s-%s-%d-%d-%d",
			config.RunID,
			config.WorkerID,
			userIndex,
			sentAt.UnixNano(),
			index,
		)
		content := loadMessageContent{
			RunID:  config.RunID,
			SentAt: sentAt.UnixNano(),
			Index:  index,
		}
		metrics.publishesAttempted.Add(1)
		if err := client.publish(ctx, topic, clientID, content); err != nil {
			if ctx.Err() == nil {
				metrics.RecordError("publish")
			}
			continue
		}
		metrics.publishesAcknowledged.Add(1)
		metrics.ackLatency.Observe(time.Since(sentAt))

		if config.PublishInterval > 0 && index+1 < config.PublishCount {
			wait := time.Duration(random.Int63n(int64(config.PublishInterval) + 1))
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-ctx.Done():
				timer.Stop()
				return
			}
		}
	}
}

func recordDelivery(runID string, metrics *Metrics, data wireData) {
	if len(data.Content) == 0 {
		return
	}
	var content loadMessageContent
	if json.Unmarshal(data.Content, &content) != nil ||
		content.RunID != runID ||
		content.SentAt <= 0 {
		return
	}
	latency := time.Since(time.Unix(0, content.SentAt))
	if latency < 0 || latency > 10*time.Minute {
		return
	}
	metrics.deliveries.Add(1)
	metrics.deliveryLatency.Observe(latency)
}
