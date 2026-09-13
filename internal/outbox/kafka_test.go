package outbox

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/twmb/franz-go/pkg/kgo"

	"github.com/umars28/marspay/internal/id"
	"github.com/umars28/marspay/internal/testdb"
)

const EnvBrokers = "MARSPAY_TEST_KAFKA_BROKERS"

func brokers(t *testing.T) []string {
	t.Helper()
	v := os.Getenv(EnvBrokers)
	if v == "" {
		t.Skipf("set %s to run Kafka integration tests", EnvBrokers)
	}
	return []string{v}
}

func newKafka(t *testing.T) (*KafkaPublisher, string) {
	t.Helper()

	addrs := brokers(t)
	topic := "marspay.test." + id.ULID()

	pub, err := NewKafkaPublisher(addrs)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	t.Cleanup(pub.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := pub.Ping(ctx); err != nil {
		t.Fatalf("ping: %v", err)
	}
	return pub, topic
}

func consume(t *testing.T, topic string, want int) []*kgo.Record {
	t.Helper()

	client, err := kgo.NewClient(
		kgo.SeedBrokers(brokers(t)...),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	if err != nil {
		t.Fatalf("consumer: %v", err)
	}
	defer client.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	var got []*kgo.Record
	for len(got) < want {
		fetches := client.PollRecords(ctx, want-len(got))
		if err := fetches.Err(); err != nil {
			t.Fatalf("poll after %d records: %v", len(got), err)
		}
		fetches.EachRecord(func(r *kgo.Record) { got = append(got, r) })
	}
	return got
}

func TestRelayDeliversToKafkaByteForByte(t *testing.T) {
	pub, topic := newKafka(t)
	pool, ctx := testdb.New(t)
	relay := NewRelay(pool, pub)

	payloads := []string{
		`{"type":"payment.succeeded","data":{"id":"pay_1","amount":3200000}}`,
		`{"type":"payment.succeeded","data":{"id":"pay_2","amount":4800000}}`,
	}
	for i, p := range payloads {
		write(t, ctx, pool, Message{
			Topic: topic, PartitionKey: "merch_1",
			EventType: "payment.succeeded", Payload: []byte(p),
		})
		_ = i
	}

	n, err := relay.Sweep(ctx)
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if n != 2 {
		t.Fatalf("published %d, want 2", n)
	}

	records := consume(t, topic, 2)
	for i, r := range records {
		if string(r.Value) != payloads[i] {
			t.Errorf("record %d value = %s, want %s", i, r.Value, payloads[i])
		}
		if string(r.Key) != "merch_1" {
			t.Errorf("record %d key = %q, want merch_1", i, r.Key)
		}
	}

	pending, err := relay.Pending(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if pending != 0 {
		t.Errorf("pending = %d after a successful sweep, want 0", pending)
	}
}

func TestHeadersCarryWhatAConsumerNeedsToDeduplicate(t *testing.T) {
	pub, topic := newKafka(t)
	pool, ctx := testdb.New(t)
	relay := NewRelay(pool, pub)

	write(t, ctx, pool, Message{
		Topic: topic, PartitionKey: "merch_1",
		EventType: "payment.succeeded", Payload: []byte(`{"id":"pay_1"}`),
	})
	if _, err := relay.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	r := consume(t, topic, 1)[0]

	headers := map[string]string{}
	for _, h := range r.Headers {
		headers[h.Key] = string(h.Value)
	}

	if headers[HeaderEventType] != "payment.succeeded" {
		t.Errorf("%s = %q, want payment.succeeded", HeaderEventType, headers[HeaderEventType])
	}
	if headers[HeaderOutboxID] == "" {
		t.Errorf("%s is missing; a consumer has nothing stable to deduplicate on", HeaderOutboxID)
	}
}

func TestEverythingForOneKeyArrivesInOneOrderedPartition(t *testing.T) {
	pub, topic := newKafka(t)
	pool, ctx := testdb.New(t)
	relay := NewRelay(pool, pub)

	const n = 60
	for i := 1; i <= n; i++ {
		write(t, ctx, pool, Message{
			Topic: topic, PartitionKey: "merch_hot",
			EventType: "payment.succeeded",
			Payload:   []byte(fmt.Sprintf(`{"seq":%d}`, i)),
		})
	}

	for published := 0; published < n; {
		sent, err := relay.Sweep(ctx)
		if err != nil {
			t.Fatalf("sweep: %v", err)
		}
		if sent == 0 {
			t.Fatal("sweep stalled before publishing everything")
		}
		published += sent
	}

	records := consume(t, topic, n)

	partition := records[0].Partition
	for i, r := range records {
		if r.Partition != partition {
			t.Fatalf("record %d landed on partition %d, want %d: one key must map to one partition",
				i, r.Partition, partition)
		}
		want := []byte(fmt.Sprintf(`{"seq":%d}`, i+1))
		if !bytes.Equal(r.Value, want) {
			t.Fatalf("record %d = %s, want %s: ordering was not preserved", i, r.Value, want)
		}
		if i > 0 && r.Offset <= records[i-1].Offset {
			t.Fatalf("offset went backwards at record %d", i)
		}
	}
}

func TestABrokerThatRefusesLeavesEverythingUnpublished(t *testing.T) {
	brokers(t)
	pool, ctx := testdb.New(t)

	dead, err := NewKafkaPublisher([]string{"127.0.0.1:1"},
		kgo.RetryTimeout(2*time.Second),
		kgo.RecordDeliveryTimeout(3*time.Second),
		kgo.RequestTimeoutOverhead(time.Second),
	)
	if err != nil {
		t.Fatalf("new publisher: %v", err)
	}
	defer dead.Close()

	relay := NewRelay(pool, dead)
	write(t, ctx, pool, Message{
		Topic: "marspay.test.dead", PartitionKey: "merch_1",
		EventType: "payment.succeeded", Payload: []byte(`{"id":"pay_1"}`),
	})

	started := time.Now()
	sweepCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if _, err := relay.Sweep(sweepCtx); err == nil {
		t.Fatal("sweep reported success against an unreachable broker")
	}
	if elapsed := time.Since(started); elapsed > 15*time.Second {
		t.Errorf("the sweep took %s to give up; a relay must not block indefinitely", elapsed)
	}

	pending, err := relay.Pending(ctx)
	if err != nil {
		t.Fatalf("pending: %v", err)
	}
	if pending != 1 {
		t.Errorf("pending = %d, want 1: an unreachable broker must never lose a message", pending)
	}

	var attempts int
	var lastError *string
	if err := pool.QueryRow(ctx,
		`SELECT attempts, last_error FROM outbox LIMIT 1`).Scan(&attempts, &lastError); err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	if attempts != 1 || lastError == nil {
		t.Errorf("attempts = %d, last_error = %v, want 1 and a recorded reason", attempts, lastError)
	}
}

func TestDifferentKeysSpreadAcrossPartitions(t *testing.T) {
	pub, topic := newKafka(t)
	pool, ctx := testdb.New(t)
	relay := NewRelay(pool, pub)

	keys := []string{"merch_a", "merch_b", "merch_c", "merch_d", "merch_e", "merch_f"}
	for _, k := range keys {
		write(t, ctx, pool, Message{
			Topic: topic, PartitionKey: k,
			EventType: "payment.succeeded", Payload: []byte(`{"k":"` + k + `"}`),
		})
	}
	if _, err := relay.Sweep(ctx); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	records := consume(t, topic, len(keys))

	byKey := map[string]int32{}
	for _, r := range records {
		key := string(r.Key)
		if seen, ok := byKey[key]; ok && seen != r.Partition {
			t.Errorf("key %s landed on partitions %d and %d", key, seen, r.Partition)
		}
		byKey[key] = r.Partition
	}
	if len(byKey) != len(keys) {
		t.Errorf("saw %d distinct keys, want %d", len(byKey), len(keys))
	}
}
