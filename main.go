package main

// a prometheus.io exporter, collecting metrics about child elements in a file folder.
// not production ready, first attempt of a solution needed to monitor a working directory at work (producer/consumer type scenario).
//
// missing peaces:
// - filetypes
// - multi platform
// - large folders
// - error handling
//
// man-at-home 2016

import (
	"flag"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/howeyc/fsnotify"
	"github.com/prometheus/client_golang/prometheus"
)

// Metric name parts.
const (
	namespace = "folderstats"
	exporter  = "exporter"
)

// Watches exactly one folder/directory for file change events.
type FolderWatcher struct {
	path          string
	filesCreated  uint32 // monitored file creation events in path counter
	filesModified uint32 // monitored file modification events in path counter
	filesDeleted  uint32 // monitored file deletion (or moved out of path) events counter
}

// Exporter collects end exports file change metrics. It implements prometheus.Collector.
type Exporter struct {
	folderWatcher   []*FolderWatcher
	duration, error prometheus.Gauge
	totalScrapes    prometheus.Counter
	scrapeErrors    prometheus.Counter
	up              prometheus.Gauge
	filesCreated    *prometheus.CounterVec // monitored file creation events in path counter
	filesModified   *prometheus.CounterVec // monitored file modification events in path counter
	filesDeleted    *prometheus.CounterVec // monitored file deletion (or moved out of path) events counter
	filesInPath     *prometheus.GaugeVec   // (up to a limit of 1024) current children in path gauge
}

var (
	pathToWatch   = flag.String("path-to-watch", "c:\\temp", "directory to watch for changes.")
	listenAddress = flag.String("web.listen-address", ":9108", "Address to listen on for web interface on telemetry.")
	metricPath    = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
	landingPage   = []byte("<html><head><title>folderstats exporter</title></head><body><h1>folderstats exporter</h1><p><a href='" + *metricPath + "'>metrics can be found there!</a></p></body></html>")
)

// count files in watched path
func (fw *FolderWatcher) CountFilesInPath() int32 {
	dir, err := os.Open(fw.path)
	if err != nil {
		log.Println("    error on open ", err)
		return -1
	} else {
		defer dir.Close()
		files, err := dir.Readdirnames(1024)
		if err != nil {
			log.Println("    error on readdirnames", err)
			return -1

		} else {
			return int32(len(files))
		}
	}
	return -1
}

// monitor call function
func (fw *FolderWatcher) OnFileChangeEvent(watcher *fsnotify.Watcher) {
	for {
		select {
		case ev := <-watcher.Event:
			if ev.IsCreate() {
				fw.filesCreated += 1
			}
			if ev.IsDelete() {
				fw.filesDeleted += 1
			}
			if ev.IsModify() {
				fw.filesModified += 1
			}
			log.Println("event:", ev, fw.filesCreated, fw.filesModified, fw.filesDeleted)

		case err := <-watcher.Error:
			log.Println("error:", err)
		}
	}
	return
}

// NewFolderWatcher ctor
func NewFolderWatcher(path string) *FolderWatcher {
	fw := new(FolderWatcher)
	fw.path = path

	log.Println("will watch folder " + fw.path)

	return fw
}

// NewExporter ctor for prometheus exporter.
func NewExporter(folderWatcher []*FolderWatcher) *Exporter {
	return &Exporter{
		folderWatcher: folderWatcher,
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
			Help:      "Total number of times an error occurred scraping/file watching.",
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
			Help:      "Whether the file watching is up for directory",
		}),
		filesCreated: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "files_created",
			Help:      "Total number of files in directory created during monitoring session.",
		}, []string{"path"}),
		filesModified: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "files_modified",
			Help:      "Total number of files in directory modified during monitoring session.",
		}, []string{"path"}),
		filesDeleted: prometheus.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace,
			Name:      "files_deleted",
			Help:      "Total number of files in directory removed during monitoring session.",
		}, []string{"path"}),
		filesInPath: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace,
			Name:      "files_in_path",
			Help:      "current filecount (HAVE CARE: Only up to 1024 are shown!)",
		}, []string{"path"}),
	}
}

// Describe all the metrics exported.
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

// Collect implements prometheus.Collector.
func (e *Exporter) Collect(ch chan<- prometheus.Metric) {
	e.scrape(ch)
	ch <- e.duration
	ch <- e.totalScrapes
	ch <- e.error
	ch <- e.scrapeErrors
	e.filesCreated.Collect(ch)
	e.filesModified.Collect(ch)
	e.filesDeleted.Collect(ch)
	e.filesInPath.Collect(ch)
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

	for _, fw := range e.folderWatcher {
		log.Println("reporting ", fw.filesCreated, fw.filesModified, fw.filesDeleted)

		// race conditions waiting here...
		e.filesCreated.WithLabelValues(cleanName(fw.path)).Add(float64(fw.filesCreated))
		fw.filesCreated = 0
		e.filesModified.WithLabelValues(cleanName(fw.path)).Add(float64(fw.filesModified))
		fw.filesModified = 0
		e.filesDeleted.WithLabelValues(cleanName(fw.path)).Add(float64(fw.filesDeleted))
		fw.filesDeleted = 0
		e.filesInPath.WithLabelValues(cleanName(fw.path)).Set(float64(fw.CountFilesInPath()))
	}

	e.up.Set(1)
}

// sanitize folder path to get valid prometheus label name
func cleanName(s string) string {
	s = strings.Replace(s, " ", "_", -1)  // Remove spaces
	s = strings.Replace(s, "(", "_", -1)  // Remove open parenthesis
	s = strings.Replace(s, ":", "_", -1)  // Remove open parenthesis
	s = strings.Replace(s, ")", "_", -1)  // Remove close parenthesis
	s = strings.Replace(s, "\\", "_", -1) // Remove backward slashes
	s = strings.ToLower(s)
	return s
}

// start main.
func main() {
	flag.Parse()

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}

	pathList := strings.Split(*pathToWatch, ";")

	fws := make([]*FolderWatcher, len(pathList))

	for index, path := range pathList {
		fws[index] = NewFolderWatcher(path)
	}

	exporter := NewExporter(fws)

	for _, fw := range fws {
		go fw.OnFileChangeEvent(watcher)
		// start monitoring directory
		err = watcher.Watch(fw.path)
		if err != nil {
			log.Fatal(err)
		}
	}

	// start http metrics exporter
	prometheus.MustRegister(exporter)
	http.Handle(*metricPath, prometheus.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write(landingPage)
	})

	log.Println("metrics exporter listening on", *listenAddress)
	log.Fatal(http.ListenAndServe(*listenAddress, nil))

	// done with
	log.Println("exporter for file system ends now.")
	watcher.Close()
}
