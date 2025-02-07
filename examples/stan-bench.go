// Copyright 2015 Apcera Inc. All rights reserved.

package main

import (
	"flag"
	"fmt"
	"io/ioutil"
	"log"
	"strings"
	"sync"
	"time"

	"github.com/nats-io/go-nats-streaming"
	"github.com/nats-io/nats"
	"github.com/nats-io/nats/bench"
)

// Some sane defaults
const (
	DefaultNumMsgs            = 100000
	DefaultNumPubs            = 1
	DefaultNumSubs            = 0
	DefaultAsync              = false
	DefaultMessageSize        = -1
	DefaultIgnoreOld          = false
	DefaultMaxPubAcksInflight = 1000
	DefaultClientID           = "benchmark"
)

func usage() {
	log.Fatalf("Usage: nats-bench [-s server (%s)] [--tls] [-id CLIENT_ID] [-np NUM_PUBLISHERS] [-ns NUM_SUBSCRIBERS] [-n NUM_MSGS] [-ms MESSAGE_SIZE] [-csv csvfile] [-mpa MAX_NUMBER_OF_PUBLISHED_ACKS_INFLIGHT] [-io] [-a] <subject>\n", nats.DefaultURL)
}

var benchmark *bench.Benchmark

func main() {
	var urls = flag.String("s", nats.DefaultURL, "The nats server URLs (separated by comma)")
	var tls = flag.Bool("tls", false, "Use TLS Secure Connection")
	var numPubs = flag.Int("np", DefaultNumPubs, "Number of Concurrent Publishers")
	var numSubs = flag.Int("ns", DefaultNumSubs, "Number of Concurrent Subscribers")
	var numMsgs = flag.Int("n", DefaultNumMsgs, "Number of Messages to Publish")
	var async = flag.Bool("a", DefaultAsync, "Async Message Publishing")
	var messageSize = flag.Int("ms", DefaultMessageSize, "Message Size in bytes.")
	var ignoreOld = flag.Bool("io", DefaultIgnoreOld, "Subscribers Ignore Old Messages")
	var maxPubAcks = flag.Int("mpa", DefaultMaxPubAcksInflight, "Max number of published acks in flight")
	var clientID = flag.String("id", DefaultClientID, "Benchmark process base client ID.")
	var csvFile = flag.String("csv", "", "Save bench data to csv file")

	log.SetFlags(0)
	flag.Usage = usage
	flag.Parse()

	args := flag.Args()
	if len(args) != 1 {
		usage()
	}

	// Setup the option block
	opts := nats.DefaultOptions
	opts.Servers = strings.Split(*urls, ",")
	for i, s := range opts.Servers {
		opts.Servers[i] = strings.Trim(s, " ")
	}
	opts.Secure = *tls

	benchmark = bench.NewBenchmark("NATS Streaming", *numSubs, *numPubs)

	var startwg sync.WaitGroup
	var donewg sync.WaitGroup

	donewg.Add(*numPubs + *numSubs)

	// Run Subscribers first
	startwg.Add(*numSubs)
	for i := 0; i < *numSubs; i++ {
		subID := fmt.Sprintf("%s-sub-%d", *clientID, i)
		go runSubscriber(&startwg, &donewg, opts, *numMsgs, *messageSize, *ignoreOld, subID)
	}
	startwg.Wait()

	// Now Publishers
	startwg.Add(*numPubs)
	pubCounts := bench.MsgsPerClient(*numMsgs, *numPubs)
	for i := 0; i < *numPubs; i++ {
		pubID := fmt.Sprintf("%s-pub-%d", *clientID, i)
		go runPublisher(&startwg, &donewg, opts, pubCounts[i], *messageSize, *async, pubID, *maxPubAcks)
	}

	log.Printf("Starting benchmark [msgs=%d, msgsize=%d, pubs=%d, subs=%d]\n", *numMsgs, *messageSize, *numPubs, *numSubs)

	startwg.Wait()
	donewg.Wait()

	benchmark.Close()
	fmt.Print(benchmark.Report())

	if len(*csvFile) > 0 {
		csv := benchmark.CSV()
		ioutil.WriteFile(*csvFile, []byte(csv), 0644)
		fmt.Printf("Saved metric data in csv file %s\n", *csvFile)
	}
}

func setMaxPubAcksInflight(val int) stan.Option {
	return func(o *stan.Options) error {
		o.MaxPubAcksInflight = val
		return nil
	}
}

func runPublisher(startwg, donewg *sync.WaitGroup, opts nats.Options, numMsgs int, msgSize int, async bool, pubID string, maxPubAcksInFlight int) {

	snc, err := stan.Connect("test-cluster", pubID, setMaxPubAcksInflight(maxPubAcksInFlight))
	if err != nil {
		log.Fatalf("Publisher %s can't connect: %v\n", pubID, err)
	}

	startwg.Done()

	args := flag.Args()

	subj := args[0]
	var msg []byte
	if msgSize > 0 {
		msg = make([]byte, msgSize)
	}
	published := 0
	start := time.Now()

	if async {
		ch := make(chan bool)
		acb := func(lguid string, err error) {
			published++
			if published >= numMsgs {
				ch <- true
			}
		}
		for i := 0; i < numMsgs; i++ {
			_, err := snc.PublishAsync(subj, msg, acb)
			if err != nil {
				log.Fatal(err)
			}
		}
		<-ch
	} else {
		for i := 0; i < numMsgs; i++ {
			err := snc.Publish(subj, msg)
			if err != nil {
				log.Fatal(err)
			}
			published++
		}
	}

	benchmark.AddPubSample(bench.NewSample(numMsgs, msgSize, start, time.Now(), snc.NatsConn()))
	snc.Close()
	donewg.Done()
}

func runSubscriber(startwg, donewg *sync.WaitGroup, opts nats.Options, numMsgs int, msgSize int, ignoreOld bool, subID string) {
	snc, err := stan.Connect("test-cluster", subID)
	if err != nil {
		log.Fatalf("Subscriber %s can't connect: %v\n", subID, err)
	}

	args := flag.Args()
	subj := args[0]
	ch := make(chan bool)
	start := time.Now()

	received := 0
	mcb := func(msg *stan.Msg) {
		received++
		if received >= numMsgs {
			ch <- true
		}
	}

	if ignoreOld {
		snc.Subscribe(subj, mcb)
	} else {
		snc.Subscribe(subj, mcb, stan.DeliverAllAvailable())
	}
	startwg.Done()

	<-ch
	benchmark.AddSubSample(bench.NewSample(numMsgs, msgSize, start, time.Now(), snc.NatsConn()))
	snc.Close()
	donewg.Done()
}
