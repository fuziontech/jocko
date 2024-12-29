package main

import (
	"context"
	"fmt"
	"io/ioutil"
	"os"

	"testing"

	"github.com/Shopify/sarama"
	"github.com/sirupsen/logrus"
	"github.com/travisjeffery/jocko/jocko"
	"github.com/travisjeffery/jocko/jocko/config"
	"github.com/travisjeffery/jocko/protocol"
)

type check struct {
	partition int32
	offset    int64
	message   string
}

const (
	topic         = "test_topic"
	messageCount  = 15
	clientID      = "test_client"
	numPartitions = int32(8)
)

var (
	logDir string
)

func init() {
	var err error
	logDir, err = ioutil.TempDir("/tmp", "jocko-client-test")
	if err != nil {
		panic(err)
	}
}

func main() {
	s, clean := setup()
	defer clean()

	config := sarama.NewConfig()
	config.ChannelBufferSize = 1
	config.Version = sarama.V0_10_0_1
	config.Producer.Return.Successes = true

	brokers := []string{s.Addr().String()}
	producer, err := sarama.NewSyncProducer(brokers, config)
	if err != nil {
		panic(err)
	}

	pmap := make(map[int32][]check)

	for i := 0; i < messageCount; i++ {
		message := fmt.Sprintf("Hello from Jocko #%d!", i)
		partition, offset, err := producer.SendMessage(&sarama.ProducerMessage{
			Topic: topic,
			Value: sarama.StringEncoder(message),
		})
		if err != nil {
			panic(err)
		}
		pmap[partition] = append(pmap[partition], check{
			partition: partition,
			offset:    offset,
			message:   message,
		})
	}
	if err = producer.Close(); err != nil {
		panic(err)
	}

	var totalChecked int
	for partitionID := range pmap {
		checked := 0
		consumer, err := sarama.NewConsumer(brokers, config)
		if err != nil {
			panic(err)
		}
		partition, err := consumer.ConsumePartition(topic, partitionID, 0)
		if err != nil {
			panic(err)
		}
		i := 0
		for msg := range partition.Messages() {
			logrus.WithFields(logrus.Fields{
				"partition": msg.Partition,
				"offset":    msg.Offset,
			}).Info("received message")

			check := pmap[partitionID][i]
			if string(msg.Value) != check.message {
				logrus.WithFields(logrus.Fields{
					"partition": msg.Partition,
					"offset":    msg.Offset,
				}).Fatal("msg values not equal")
			}
			if msg.Offset != check.offset {
				logrus.WithFields(logrus.Fields{
					"partition": msg.Partition,
					"offset":    msg.Offset,
				}).Fatal("msg offsets not equal")
			}

			logrus.WithFields(logrus.Fields{
				"partition": msg.Partition,
				"offset":    msg.Offset,
			}).Info("message is ok")

			i++
			checked++

			logrus.WithFields(logrus.Fields{
				"i":   i,
				"len": len(pmap[partitionID]),
			}).Debug("message processing progress")

			if i == len(pmap[partitionID]) {
				totalChecked += checked
				logrus.WithField("partition", partitionID).Info("finished checking partition")
				if err = consumer.Close(); err != nil {
					panic(err)
				}
				break
			} else {
				logrus.WithField("partition", partitionID).Debug("still checking partition")
			}
		}
	}
	fmt.Printf("producer and consumer worked! %d messages ok\n", totalChecked)
}

func setup() (*jocko.Server, func()) {
	c, _ := jocko.NewTestServer(&testing.T{}, func(cfg *config.Config) {
		cfg.Bootstrap = true
		cfg.BootstrapExpect = 1
		cfg.StartAsLeader = true
	}, nil)
	if err := c.Start(context.Background()); err != nil {
		fmt.Fprintf(os.Stderr, "failed to start cluster: %v\n", err)
		os.Exit(1)
	}

	conn, err := jocko.Dial("tcp", c.Addr().String())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error connecting to broker: %v\n", err)
		os.Exit(1)
	}
	resp, err := conn.CreateTopics(&protocol.CreateTopicRequests{
		Requests: []*protocol.CreateTopicRequest{{
			Topic:             topic,
			NumPartitions:     numPartitions,
			ReplicationFactor: 1,
		}},
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed with request to broker: %v\n", err)
		os.Exit(1)
	}
	for _, topicErrCode := range resp.TopicErrorCodes {
		if topicErrCode.ErrorCode != protocol.ErrNone.Code() && topicErrCode.ErrorCode != protocol.ErrTopicAlreadyExists.Code() {
			err := protocol.Errs[topicErrCode.ErrorCode]
			fmt.Fprintf(os.Stderr, "error code: %v\n", err)
			os.Exit(1)
		}
	}

	return c, func() {
		c.Shutdown()
		os.RemoveAll(logDir)
	}
}
