package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io/ioutil"
	"math"
	"net/http"
	_ "net/http/pprof"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "github.com/go-bindata/go-bindata"

	"cloud.google.com/go/storage"
	"github.com/gorilla/mux"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/spf13/cobra"
	gcpoption "google.golang.org/api/option"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/klog"

	"github.com/weshayutin/ci-search/bugzilla"
	"github.com/weshayutin/ci-search/metricdb"
	"github.com/weshayutin/ci-search/metricdb/httpgraph"
	"github.com/weshayutin/ci-search/pkg/bindata"
	"github.com/weshayutin/ci-search/pkg/proc"
	"github.com/weshayutin/ci-search/prow"
)

func main() {
	original := flag.CommandLine
	klog.InitFlags(original)
	original.Set("alsologtostderr", "true")
	original.Set("v", "2")

	// the reaper handles duties running as PID 1 when in a contanier
	go proc.StartPeriodicReaper(10)

	opt := &options{
		ListenAddr:        ":8080",
		MaxAge:            14 * 24 * time.Hour,
		JobURIPrefix:      "https://prow.ci.openshift.org/view/gs/",
		ArtifactURIPrefix: "https://storage.googleapis.com/",
		IndexBucket:       "origin-ci-test",
	}
	cmd := &cobra.Command{
		Run: func(cmd *cobra.Command, arguments []string) {
			if err := opt.Run(); err != nil {
				klog.Fatalf("error: %v", err)
			}
		},
	}
	flag := cmd.Flags()

	flag.StringVar(&opt.Path, "path", opt.Path, "The directory to save index results to.")
	flag.StringVar(&opt.ListenAddr, "listen", opt.ListenAddr, "The address to serve search results on")
	flag.StringVar(&opt.DebugAddr, "debug-listen", opt.DebugAddr, "The address to serve debug handlers on")
	flag.AddGoFlag(original.Lookup("v"))

	flag.DurationVar(&opt.MaxAge, "max-age", opt.MaxAge, "The maximum age of entries to keep cached. Set to 0 to keep all. Defaults to 14 days.")
	flag.DurationVar(&opt.Interval, "interval", opt.Interval, "(Disabled) The interval to index jobs.")
	flag.StringVar(&opt.ConfigPath, "config", opt.ConfigPath, "(Disabled) Path on disk to a testgrid config for indexing.")
	flag.StringVar(&opt.GCPServiceAccount, "gcp-service-account", opt.GCPServiceAccount, "(Disabled) Path to a GCP service account file.")
	flag.StringVar(&opt.JobURIPrefix, "job-uri-prefix", opt.JobURIPrefix, "URI prefix for converting job-detail pages to index names.  For example, https://prow.ci.openshift.org/view/gs/origin-ci-test/logs/release-openshift-origin-installer-e2e-aws-4.1/309 has an index name of origin-ci-test/logs/release-openshift-origin-installer-e2e-aws-4.1/309 with the default job-URI prefix.")
	flag.StringVar(&opt.ArtifactURIPrefix, "artifact-uri-prefix", opt.ArtifactURIPrefix, "URI prefix for artifacts.  For example, origin-ci-test/logs/release-openshift-origin-installer-e2e-aws-4.1/309 has build logs at https://storage.googleapis.com/origin-ci-test/logs/release-openshift-origin-installer-e2e-aws-4.1/309/build-log.txt with the default artifact-URI prefix.")
	flag.StringVar(&opt.DeckURI, "deck-uri", opt.DeckURI, "URL to the Deck server to index prow job failures into search.")
	flag.StringVar(&opt.IndexBucket, "index-bucket", opt.IndexBucket, "A GCS bucket to look for job indices in.")
	flag.StringVar(&opt.MetricDBPath, "metric-db", opt.MetricDBPath, "Path where metrics should be recorded as a SQLite database. If empty, no metrics will be stored.")
	flag.DurationVar(&opt.MetricMaxAge, "metric-max-age", opt.MetricMaxAge, "The maximum age to retain metrics. If negative, metrics are retained forever. If zero, no metrics are gathered.")

	flag.StringVar(&opt.BugzillaURL, "bugzilla-url", opt.BugzillaURL, "The URL of a bugzilla server to index bugs from.")
	flag.StringVar(&opt.BugzillaTokenPath, "bugzilla-token-file", opt.BugzillaTokenPath, "A file to read a bugzilla token from.")
	flag.StringVar(&opt.BugzillaSearch, "bugzilla-search", opt.BugzillaSearch, "A quicksearch query to search for bugs to index.")

	flag.BoolVar(&opt.NoIndex, "disable-indexing", opt.NoIndex, "Disable all indexing to disk.")

	if err := cmd.Execute(); err != nil {
		klog.Exitf("error: %v", err)
	}
}

type options struct {
	ListenAddr string
	DebugAddr  string
	Path       string

	// arguments to indexing
	MaxAge            time.Duration
	Interval          time.Duration
	GCPServiceAccount string
	JobURIPrefix      string
	ArtifactURIPrefix string
	ConfigPath        string
	DeckURI           string
	IndexBucket       string

	MetricDBPath string
	MetricMaxAge time.Duration

	BugzillaURL       string
	BugzillaSearch    string
	BugzillaTokenPath string

	NoIndex bool

	generator CommandGenerator

	jobsIndex    *pathIndex
	jobAccessor  prow.JobAccessor
	jobsPath     string
	jobURIPrefix *url.URL

	bugs         *bugzilla.CommentStore
	bugsPath     string
	bugURIPrefix *url.URL

	metrics *metricdb.DB
}

type IndexStats struct {
	Size    int64
	Bugs    int
	Entries int

	Jobs       int
	FailedJobs int

	Buckets []JobCountBucket
}

type JobCountBucket struct {
	T          int64
	Jobs       int
	FailedJobs int
}

// Stats returns aggregate statistics for the indexed paths.
func (o *options) Stats() IndexStats {
	j := o.jobsIndex.Stats()
	b := o.bugs.Stats()

	var totalJobs, failedJobs int
	jobs, _ := o.jobAccessor.List(labels.Everything())
	var buckets []JobCountBucket
	if len(jobs) > 1 {
		var min, max int64 = math.MaxInt64, 0
		for _, job := range jobs {
			t := job.Status.CompletionTime.Time.Unix()
			if t <= 0 {
				t = job.Status.StartTime.Time.Unix()
			}
			if t < 0 {
				continue
			}
			if t < min {
				min = t
			}
			if t > max {
				max = t
			}
		}
		begin := time.Unix(min, 0).Truncate(time.Hour).Unix()
		bins := (max-begin)/3600 + 1
		buckets = make([]JobCountBucket, bins)
		for i := range buckets {
			buckets[i].T = begin + int64(i)*3600
		}
		for _, job := range jobs {
			failed := job.Status.State != "success"
			totalJobs++
			if failed {
				failedJobs++
			}
			t := job.Status.CompletionTime.Time.Unix()
			if t <= 0 {
				t = job.Status.StartTime.Time.Unix()
			}
			if t <= 0 {
				continue
			}
			i := (t - begin) / 3600
			buckets[i].Jobs++
			if failed {
				buckets[i].FailedJobs++
			}
		}
	} else {
		for _, job := range jobs {
			totalJobs++
			if job.Status.State != "success" {
				failedJobs++
			}
		}
	}
	return IndexStats{
		Entries:    j.Entries,
		Size:       j.Size,
		Bugs:       b.Bugs,
		Jobs:       totalJobs,
		FailedJobs: failedJobs,
		Buckets:    buckets,
	}
}

func (o *options) RipgrepSourceArguments(index *Index, jobNames sets.String) ([]string, []string, error) {
	var args []string
	var additionalPaths []string
	switch index.SearchType {
	case "bug":
		if o.bugURIPrefix == nil {
			return nil, nil, fmt.Errorf("searching on bugs is not enabled")
		}
		return []string{"--glob", "bug-*"}, []string{o.bugsPath}, nil
	case "all", "bug+junit":
		if o.bugURIPrefix != nil {
			args = []string{"--glob", "bug-*"}
			additionalPaths = []string{o.bugsPath}
		}
		fallthrough
	default:
		if o.jobURIPrefix == nil {
			return nil, nil, fmt.Errorf("searching on jobs is not enabled")
		}
		paths, err := o.jobsIndex.SearchPaths(index, jobNames)
		if err != nil {
			return nil, nil, err
		}
		if paths == nil {
			if names := o.jobsIndex.FilenamesForSearchType(index.SearchType); len(names) > 0 {
				for _, name := range names {
					// WES: this is where build-log.txt is appended to the search term
					args = append(args, "--glob", name+"*")
					//args = append(args, "--glob", "*.log")
				}
				args = append(args, o.jobsPath)
			}
		}
		// WES: overide the default build-log.txt junit etc.. w/ *.log
		args = []string{"--glob", "*.log", "/var/tmp/oadp_ci_search/jobs"}
		return args, append(paths, additionalPaths...), nil
	}
}

func (o *options) MetadataFor(path string) (Result, error) {
	var result Result
	switch {
	case strings.HasPrefix(path, "bugs/"):
		if o.bugURIPrefix == nil {
			return result, fmt.Errorf("searching on bugs is not enabled")
		}
		path = strings.TrimPrefix(path, "bugs/")

		result.FileType = "bug"
		name := path
		if !strings.HasPrefix(name, "bug-") {
			return result, fmt.Errorf("expected path bugs/bug-NUMBER: %s", path)
		}
		name = name[4:]
		id, err := strconv.Atoi(name)
		if err != nil {
			return result, fmt.Errorf("expected path bugs/bug-NUMBER: %s", path)
		}
		result.Name = fmt.Sprintf("Bug %d", id)
		result.Number = id

		copied := *o.bugURIPrefix
		copied.RawQuery = url.Values{"id": []string{strconv.Itoa(id)}}.Encode()
		result.URI = &copied

		if comments, ok := o.bugs.Get(id); ok {
			// take the time of last bug update or comment, whichever is newer
			if l := len(comments.Comments); l > 0 {
				result.LastModified = comments.Comments[l-1].CreationTime.Time
			}
			if comments.Info.LastChangeTime.After(result.LastModified) {
				result.LastModified = comments.Info.LastChangeTime.Time
			}
			if len(comments.Info.Summary) > 0 {
				if len(comments.Info.Status) > 0 {
					result.Name = fmt.Sprintf("Bug %d: %s %s", id, comments.Info.Summary, comments.Info.Status)
				} else {
					result.Name = fmt.Sprintf("Bug %d: %s", id, comments.Info.Summary)
				}
			}
			result.Bug = &comments.Info
		}

		result.IgnoreAge = true

		return result, nil

	case strings.HasPrefix(path, "jobs/"):
		if o.jobURIPrefix == nil {
			return result, fmt.Errorf("searching on jobs is not enabled")
		}
		path = strings.TrimPrefix(path, "jobs/")

		parts := strings.SplitN(path, "/", 8)
		last := len(parts) - 1

		var result Result
		result.URI = o.jobURIPrefix.ResolveReference(&url.URL{Path: strings.Join(parts[:last], "/")})

		switch parts[last] {
		case "build-log.txt":
			result.FileType = "build-log"
		case "junit.failures":
			result.FileType = "junit"
		default:
			result.FileType = parts[last]
		}

		switch parts[1] {
		case "logs":
			result.Trigger = "build"
		case "pr-logs":
			result.Trigger = "pull"
		default:
			result.Trigger = parts[1]
		}

		var err error
		result.Number, err = strconv.Atoi(parts[last-1])
		if err != nil {
			return result, err
		}

		if last < 3 {
			return result, fmt.Errorf("not enough parts (%d < 3)", last)
		}
		result.Name = parts[last-2]

		result.LastModified = o.jobsIndex.LastModified(path)

		return result, nil
	default:
		return result, fmt.Errorf("unrecognized result path: %s", path)
	}
}

func (o *options) Run() error {
	jobURIPrefix, err := url.Parse(o.JobURIPrefix)
	if err != nil {
		klog.Exitf("Unable to parse --job-uri-prefix: %v", err)
	}
	o.jobURIPrefix = jobURIPrefix
	o.jobsPath = filepath.Join(o.Path, "jobs")
	o.bugsPath = filepath.Join(o.Path, "bugs")

	indexedPaths := &pathIndex{
		base:    o.jobsPath,
		baseURI: jobURIPrefix,
		maxAge:  o.MaxAge,
	}

	o.jobsIndex = indexedPaths

	if len(o.BugzillaURL) > 0 {
		url, err := url.Parse(o.BugzillaURL)
		if err != nil {
			klog.Exitf("Unable to parse --bugzilla-url: %v", err)
		}

		u := *url
		u.Path = "show_bug.cgi"
		o.bugURIPrefix = &u

		if len(o.BugzillaSearch) == 0 {
			klog.Exitf("--bugzilla-search is required")
		}
		tokenData, err := ioutil.ReadFile(o.BugzillaTokenPath)
		if err != nil {
			klog.Exitf("Failed to load --bugzilla-token-file: %v", err)
		}
		token := string(bytes.TrimSpace(tokenData))
		c := bugzilla.NewClient(*url)
		c.APIKey = token
		rt, err := rest.TransportFor(&rest.Config{})
		if err != nil {
			klog.Exitf("Unable to build bugzilla client: %v", err)
		}
		c.Client = &http.Client{Transport: rt}
		informer := bugzilla.NewInformer(
			c,
			10*time.Minute,
			8*time.Hour,
			30*time.Minute,
			func(metav1.ListOptions) bugzilla.SearchBugsArgs {
				return bugzilla.SearchBugsArgs{
					Quicksearch: o.BugzillaSearch,
				}
			},
			func(info *bugzilla.BugInfo) bool {
				return !contains(info.Keywords, "Security")
			},
		)
		lister := bugzilla.NewBugLister(informer.GetIndexer())
		if err := os.MkdirAll(o.bugsPath, 0777); err != nil {
			return fmt.Errorf("unable to create directory for artifact: %v", err)
		}
		diskStore := bugzilla.NewCommentDiskStore(o.bugsPath, o.MaxAge)
		store := bugzilla.NewCommentStore(c, 2*time.Minute, false, diskStore)

		o.bugs = store

		ctx := context.Background()
		go informer.Run(ctx.Done())
		go store.Run(ctx, informer)
		go diskStore.Run(ctx, lister, store, o.NoIndex)
		klog.Infof("Started indexing bugzilla %s with query %q", o.BugzillaURL, o.BugzillaSearch)
	} else {
		o.bugs = bugzilla.NewCommentStore(nil, 0, false, nil)
	}

	if len(o.DeckURI) > 0 {
		if o.MaxAge > 0 {
			klog.Infof("Results expire after %s", o.MaxAge)
		}

		u, err := url.Parse(o.DeckURI)
		if err != nil {
			klog.Exitf("Unable to parse --deck-uri: %v", err)
		}
		deckURI := u
		// this is the initial list of all the jobs.
		deckURI.Path = "/prowjobs.js"

		rt, err := rest.TransportFor(&rest.Config{})
		if err != nil {
			klog.Exitf("Unable to build prow client: %v", err)
		}
		c := prow.NewClient(*deckURI)
		c.Client = &http.Client{Transport: rt}

		gcsClient, err := storage.NewClient(context.Background(), gcpoption.WithoutAuthentication())
		if err != nil {
			klog.Exitf("Unable to build gcs client: %v", err)
		}

		var initialJobLister prow.JobLister
		if len(o.IndexBucket) > 0 {
			//klog.Fatalf("stop here")
			initialJobLister = prow.ListerFunc(func(ctx context.Context) ([]*prow.Job, error) {
				// this is the critical spot in the code where it starts
				// to index all the jobs in gs under origin-ci-test
				klog.Infof(o.IndexBucket)
				//klog.Fatalf("stop")
				return prow.ReadFromIndex(ctx, gcsClient, o.IndexBucket, "job-state", o.MaxAge, *u)
			})
		}

		informer := prow.NewInformer(2*time.Minute, 30*time.Minute, o.MaxAge, initialJobLister, c)
		lister := prow.NewLister(informer.GetIndexer())
		o.jobAccessor = lister
		store := prow.NewDiskStore(gcsClient, o.jobsPath, o.MaxAge)

		if err := os.MkdirAll(o.jobsPath, 0777); err != nil {
			return fmt.Errorf("unable to create directory for artifact: %v", err)
		}

		h := store.Handler()
		informer.AddEventHandler(h)

		ctx := context.Background()
		go informer.Run(ctx.Done())
		go func() {
			cache.WaitForCacheSync(ctx.Done(), informer.HasSynced)
			store.Run(ctx, lister, indexedPaths, o.NoIndex, 40)
		}()

		klog.Infof("Started indexing prow jobs %s", o.DeckURI)
	} else {
		o.jobAccessor = prow.Empty
	}

	// enable metrics
	if len(o.MetricDBPath) > 0 {
		o.metrics, err = metricdb.New(o.MetricDBPath, url.URL{}, o.MetricMaxAge)
		if err != nil {
			return err
		}
		go wait.Forever(func() {
			if err := o.metrics.Run(); err != nil {
				klog.Fatalf("Unable to read metrics: %v", err)
			}
		}, 3*time.Minute)
	}
	g := &httpgraph.Server{DB: o.metrics}

	go wait.Forever(func() {
		if err := indexedPaths.Load(); err != nil {
			klog.Fatalf("Unable to index: %v", err)
		}
	}, 3*time.Minute)

	o.generator, err = NewCommandGenerator(o.Path, o)
	if err != nil {
		return err
	}

	if len(o.DebugAddr) > 0 {
		go func() {
			if err := http.ListenAndServe(o.DebugAddr, nil); err != nil {
				klog.Exitf("Debug server exited: %v", err)
			}
		}()
	}
	if len(o.ListenAddr) > 0 {
		mux := mux.NewRouter()

		h := prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "http_duration",
			Buckets: []float64{0.01, 0.1, 1, 10, 100},
		}, []string{"path", "code", "method"})
		prometheus.MustRegister(h)
		handle := func(path string, handler http.Handler) {
			handler = promhttp.InstrumentHandlerDuration(h.MustCurryWith(prometheus.Labels{"path": path}), handler)
			mux.Handle(path, handler)
		}

		mux.PathPrefix("/static/").Handler(http.StripPrefix("/static/", http.FileServer(bindata.AssetFile())))
		handle("/graph/metrics", http.HandlerFunc(g.HandleGraph))
		handle("/graph/api/metrics/job", http.HandlerFunc(g.HandleAPIJobGraph))
		handle("/chart", http.HandlerFunc(o.handleChart))
		handle("/chart.png", http.HandlerFunc(o.handleChartPNG))
		handle("/config", http.HandlerFunc(o.handleConfig))
		handle("/jobs", http.HandlerFunc(o.handleJobs))
		handle("/search", http.HandlerFunc(o.handleSearch))
		handle("/v2/search", http.HandlerFunc(o.handleSearchV2))
		handle("/metrics", promhttp.Handler())
		handle("/", http.HandlerFunc(o.handleIndex))

		go func() {
			klog.Infof("Listening on %s", o.ListenAddr)
			if err := http.ListenAndServe(o.ListenAddr, mux); err != nil {
				klog.Exitf("Server exited: %v", err)
			}
		}()
	}
	select {}
}

func contains(arr []string, s string) bool {
	for _, item := range arr {
		if s == item {
			return true
		}
	}
	return false
}
