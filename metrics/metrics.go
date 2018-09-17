package metrics

import (
	"fmt"
	"log"
	"time"

	"github.com/Ziyang2go/tgik-controller/mongo"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/push"
)

type JobMetrics struct {
	registry prometheus.Registry
	gateway  string
}

func NewJobMetrics(gateway string) *JobMetrics {
	var (
		completionTime = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "job_completion_time",
		})
		duration = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "job_duration",
		})
	)
	registry := prometheus.NewRegistry()
	registry.MustRegister(completionTime, duration)
	c := &JobMetrics{
		registry: *registry,
		gateway:  gateway,
	}
	return c
}

func (c *JobMetrics) Push(job mongo.Job, status string) {
	log.Println("Pushing metrics to gateway")

	var (
		completionTime = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "job_completion_time",
		})
		successTime = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "job_success_time",
		})
		duration = prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "job_duration",
		})
	)
	pusher := push.New(c.gateway, job.NAME).Gatherer(&c.registry)
	duration.Set(time.Since(job.CREATEDAT).Seconds())
	completionTime.SetToCurrentTime()
	if status == "ok" {
		pusher.Collector(successTime)
		successTime.SetToCurrentTime()
	}

	if err := pusher.Add(); err != nil {
		fmt.Println("Could not push to Pushgateway:", err)
	}

	fmt.Println("Metrics pushed to gateway ")
}
