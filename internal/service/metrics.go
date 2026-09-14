package service

import (
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// What an operator watching this service needs to know, on /prometheus.
//
// Package level, registered once on the default registry that the SDK's
// PrometheusHandler serves. Per-Service registration would panic the moment a
// second one existed, which is every test after the first.
var (
	jobsTotal = promauto.NewCounterVec(prometheus.CounterOpts{
		Name: "devicemigration_jobs_total",
		Help: "Jobs that have finished, by kind and outcome.",
	}, []string{"kind", "status"})

	jobDuration = promauto.NewHistogramVec(prometheus.HistogramOpts{
		Name: "devicemigration_job_duration_seconds",
		Help: "How long jobs took, by kind.",
		// A hierarchy without data moves in seconds; one with a month of
		// measurements takes hours. Buckets spanning that, rather than the
		// default set which stops at ten seconds.
		Buckets: []float64{1, 5, 15, 60, 300, 900, 1800, 3600, 7200, 21600},
	}, []string{"kind"})

	queueDepth = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "devicemigration_queue_depth",
		Help: "Jobs accepted and waiting to run.",
	})

	jobRunning = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "devicemigration_job_running",
		Help: "1 while a job is running, 0 otherwise. One runs at a time.",
	})

	subscribedTenants = promauto.NewGauge(prometheus.GaugeOpts{
		Name: "devicemigration_subscribed_tenants",
		Help: "Tenants this microservice can act on.",
	})

	recordsPruned = promauto.NewCounter(prometheus.CounterOpts{
		Name: "devicemigration_records_pruned_total",
		Help: "Job records deleted because they were older than the retention period.",
	})

	recordsDeleted = promauto.NewCounter(prometheus.CounterOpts{
		Name: "devicemigration_records_deleted_total",
		Help: "Job records deleted because somebody asked for them to be.",
	})
)
