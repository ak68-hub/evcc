package server

import (
	"sync"
	"time"

	"github.com/andig/evcc/api"
	"github.com/andig/evcc/core"
	influxdb "github.com/influxdata/influxdb1-client/v2"
)

const (
	influxWriteTimeout  = 10 * time.Second
	influxWriteInterval = 30 * time.Second
	precision           = "s"
)

// Influx is a influx publisher
type Influx struct {
	log    *api.Logger
	client influxdb.Client
	writer *writer
}

// writer batches points for writing
type writer struct {
	sync.Mutex
	exit       chan struct{}
	done       chan struct{}
	log        *api.Logger
	client     influxdb.Client
	points     []*influxdb.Point
	pointsConf influxdb.BatchPointsConfig
	interval   time.Duration
}

// write asynchronously writes the collected points
func (m *writer) add(p *influxdb.Point) {
	m.Lock()
	m.points = append(m.points, p)
	m.Unlock()
}

// write asynchronously writes the collected points
func (m *writer) write() {
	m.Lock()

	// get current batch
	if len(m.points) == 0 {
		m.Unlock()
		return
	}

	// create new batch
	batch, err := influxdb.NewBatchPoints(m.pointsConf)
	if err != nil {
		m.log.ERROR.Print(err)
		m.Unlock()
		return
	}

	// replace current batch
	points := m.points
	m.points = nil
	m.Unlock()

	// write batch
	batch.AddPoints(points)
	m.log.TRACE.Printf("writing %d point(s)", len(points))

	if err := m.client.Write(batch); err != nil {
		m.log.ERROR.Print(err)

		// put points back at beginning of next batch
		m.Lock()
		m.points = append(points, m.points...)
		m.Unlock()
	}
}

func (m *writer) stop() <-chan struct{} {
	m.exit <- struct{}{}
	return m.done
}

func (m *writer) run() {
	ticker := time.NewTicker(m.interval)
	for {
		select {
		case <-ticker.C:
			m.write()
		case <-m.exit:
			ticker.Stop()
			m.write()
			close(m.done)
			return
		}
	}
}

// NewInfluxClient creates new publisher for influx
func NewInfluxClient(
	url string,
	database string,
	interval time.Duration,
	user string,
	password string,
) *Influx {
	log := api.NewLogger("iflx")

	if database == "" {
		log.FATAL.Fatal("missing database")
	}
	if interval == 0 {
		interval = influxWriteInterval
	}

	client, err := influxdb.NewHTTPClient(influxdb.HTTPConfig{
		Addr:     url,
		Username: user,
		Password: password,
		Timeout:  influxWriteTimeout,
	})
	if err != nil {
		log.FATAL.Fatalf("error creating client: %v", err)
	}

	// check connection
	go func(client influxdb.Client) {
		if _, _, err := client.Ping(influxWriteTimeout); err != nil {
			log.FATAL.Fatalf("%v", err)
		}
	}(client)

	writer := &writer{
		log:    log,
		client: client,
		pointsConf: influxdb.BatchPointsConfig{
			Database:  database,
			Precision: precision,
		},
		interval: interval,
		exit:     make(chan struct{}),
		done:     make(chan struct{}),
	}

	return &Influx{
		log:    log,
		client: client,
		writer: writer,
	}
}

// Run Influx publisher
func (m *Influx) Run(in <-chan core.Param) {
	go m.writer.run() // asynchronously write batches

	// add points to batch for async writing
	for param := range in {
		if _, ok := param.Val.(float64); !ok {
			continue
		}
		p, err := influxdb.NewPoint(
			param.Key,
			map[string]string{
				"loadpoint": param.LoadPoint,
			},
			map[string]interface{}{
				"value": param.Val,
			},
			time.Now(),
		)
		if err != nil {
			m.log.ERROR.Printf("failed creating point: %v", err)
			continue
		}

		m.writer.add(p)
	}

	// wait for write loop closed and done
	<-m.writer.stop()

	m.client.Close()
}
