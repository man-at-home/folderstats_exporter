package main

// a prometheus.io exporter, collecting metrics about child elements in a file folder.
// not production ready, first attempt of a solution needed to monitor a working directory at work (producer/consumer type scenario).
//
// https://github.com/man-at-home/folderstats_exporter man-at-home 2016

import (
	"flag"
	"log"
	"net/http"
	"os"
	"time"

	"github.com/howeyc/fsnotify"
	"github.com/prometheus/client_golang/prometheus"
)

// Metric name parts.
const (
	namespace = "folderstats"
	exporter  = "exporter"
)

// Exporter collects file change metrics. It implements prometheus.Collector.
type Exporter struct {
	path            string
	duration, error prometheus.Gauge
	totalScrapes    prometheus.Counter
	scrapeErrors    prometheus.Counter
	up              prometheus.Gauge
	filesCreated    prometheus.Counter // monitored file creation events in path counter
	filesModified   prometheus.Counter // monitored file modification events in path counter
	filesDeleted    prometheus.Counter // monitored file deletion (or moved out of path) events counter
	filesInPath     prometheus.Gauge   // (up to a limit of 1024) current children in path gauge
}

var (
	pathToWatch   = flag.String("path-to-watch", "c:\\temp", "directory to watch for changes.")
	listenAddress = flag.String("web.listen-address", ":9108", "Address to listen on for web interface on telemetry.")
	metricPath    = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	landingPage   = []byte("<html><head><title>folderstats exporter</title></head><body><h1>folderstats exporter</h1><p><a href='" + *metricPath + "'>metrics can be found there!</a></p></body></html>")
)

// ctor
func NewExporter(path string) *Exporter {
	return &Exporter{
		path: path,
		duration: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "last_scrape_duration_seconds",
			Help:      "Duration of the last scrape of metrics from this exporter.",
		}),
		totalScrapes: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "scrapes_total",
			Help:      "Total number of times this exporter was scraped for metrics.",
		}),
		scrapeErrors: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "scrape_errors_total",
			Help:      "Total number of times an error occured scraping/file watching.",
		}),
		error: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Subsystem: exporter,
			Name:      "last_scrape_error",
			Help:      "Whether the last scrape of metrics resulted in an error (1 for error, 0 for success).",
		}),
		up: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "up",
			Help:      "Whether the file watching is up for directory " + path,
		}),
		filesCreated: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "files_created",
			Help:      "Total number of files in directory created during monitoring session.",
		}),
		filesModified: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "files_modified",
			Help:      "Total number of files in directory modified during monitoring session.",
		}),
		filesDeleted: prometheus.NewCounter(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "files_deleted",
			Help:      "Total number of files in directory removed during monitoring session.",
		}),
		filesInPath: prometheus.NewGauge(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "files_in_path",
			Help:      "current filecount (HAVE CARE: Only up to 1024 are shown!) in " + path,
		}),
	}
}

// describes all the metrics exported.
func (e *Exporter) Describe(ch chan<- *prometheus.Desc) {
	metricCh := make(chan prometheus.Metric)
	doneCh := make(chan struct{})

	go func() {
		for m := range metricCh {
			ch <- m.Desc()
		}
		close(doneCh)
	}()

	e.Collect(metricCh)
	close(metricCh)
	<-doneCh
}

// implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.scrape(ch)
	ch <- e.duration
	ch <- e.totalScrapes
	ch <- e.error
	ch <- e.scrapeErrors
	ch <- e.filesCreated
	ch <- e.filesModified
	ch <- e.filesDeleted
	ch <- e.filesInPath
	ch <- e.up
}

// get a new set of metric values.
func (e *Exporter) scrape(ch chan<- prometheus.Metric) {
	e.totalScrapes.Inc()
	var err error
	defer func(begun time.Time) {
		e.duration.Set(time.Since(begun).Seconds())
		if err == nil {
			e.error.Set(0)
		} else {
			e.error.Set(1)
		}
	}(time.Now())

	dir, err := os.Open(*pathToWatch)
	if err != nil {
		log.Println("    error on open ", err)
		e.scrapeErrors.Inc()
		e.filesInPath.Set(-1)
	} else {
		defer dir.Close()
		files, err := dir.Readdirnames(1024)
		if err != nil {
			log.Println("    error on readdirnames", err)
			e.scrapeErrors.Inc()
			e.filesInPath.Set(-1)

		} else {
			e.filesInPath.Set(float64(len(files)))
		}
	}
	e.up.Set(1)
	//	log.Println("  exporter was scraped.")
}

// start main.
func main() {
	flag.Parse()
	log.Println("startup file system monitoring " + *pathToWatch)

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}

	exporter := NewExporter(*pathToWatch)

	// Process events in callbacks
	go func() {
		for {
			select {
			case ev := <-watcher.Event:
				// log.Println("event:", ev)
				if ev.IsCreate() {
					exporter.filesCreated.Inc()
				}
				if ev.IsDelete() {
					exporter.filesDeleted.Inc()
				}
				if ev.IsModify() {
					exporter.filesModified.Inc()
				}

			case err := <-watcher.Error:
				log.Println("error:", err)
				exporter.scrapeErrors.Inc()
			}
		}
	}()

	// start monitoring directory
	err = watcher.Watch(*pathToWatch)
	if err != nil {
		log.Fatal(err)
	} else {
		// start http metrics exporter
		prometheus.MustRegister(exporter)
		http.Handle(*metricPath, prometheus.Handler())
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			w.Write(landingPage)
		})

		log.Println("metrics exporter listening on", *listenAddress)
		log.Fatal(http.ListenAndServe(*listenAddress, nil))
	}

	// done with
	log.Println("exporter for file system ends now.")
	watcher.Close()
}
