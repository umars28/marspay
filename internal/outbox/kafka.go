package outbox

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	HeaderEventType = "event-type"
	HeaderOutboxID  = "outbox-id"
)

type KafkaPublisher struct {
	client *kgo.Client
}

func NewKafkaPublisher(brokers []string, extra ...kgo.Opt) (*KafkaPublisher, error) {
	if len(brokers) == 0 {
		return nil, errors.New("outbox: at least one broker is required")
	}

	opts := []kgo.Opt{
		kgo.SeedBrokers(brokers...),
		kgo.RequiredAcks(kgo.AllISRAcks()),
		kgo.RecordPartitioner(kgo.StickyKeyPartitioner(nil)),
		kgo.ProducerLinger(5 * time.Millisecond),
		kgo.RetryTimeout(30 * time.Second),
		kgo.RecordDeliveryTimeout(30 * time.Second),
		kgo.AllowAutoTopicCreation(),
	}
	opts = append(opts, extra...)

	client, err := kgo.NewClient(opts...)
	if err != nil {
		return nil, fmt.Errorf("outbox: kafka client: %w", err)
	}
	return &KafkaPublisher{client: client}, nil
}

func (p *KafkaPublisher) Publish(ctx context.Context, msgs []Message) error {
	records := make([]*kgo.Record, 0, len(msgs))
	for _, m := range msgs {
		records = append(records, &kgo.Record{
			Topic: m.Topic,
			Key:   []byte(m.PartitionKey),
			Value: m.Payload,
			Headers: []kgo.RecordHeader{
				{Key: HeaderEventType, Value: []byte(m.EventType)},
				{Key: HeaderOutboxID, Value: []byte(strconv.FormatInt(m.ID, 10))},
			},
		})
	}

	if err := p.client.ProduceSync(ctx, records...).FirstErr(); err != nil {
		return fmt.Errorf("outbox: produce %d records: %w", len(records), err)
	}
	return nil
}

func (p *KafkaPublisher) Ping(ctx context.Context) error {
	if err := p.client.Ping(ctx); err != nil {
		return fmt.Errorf("outbox: ping brokers: %w", err)
	}
	return nil
}

func (p *KafkaPublisher) Close() {
	p.client.Close()
}
