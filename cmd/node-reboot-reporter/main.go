// - Watches EndpointSlices for a headless Service
// (label kubernetes.io/service-name=node-reboot-detector).
// - Each scraper creates/updates a Lease named by POD_NAME.
// - Watches Leases to maintain a peer list.
// - Uses Rendezvous hashing to own a subset of endpoints and scrapes /metrics from them.
package main

import (
	"context"
	"crypto/rand"
	"flag"
	"fmt"
	"hash/fnv"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"sync"
	"time"

	"github.com/kubereboot/kured/internal/cli"
	"github.com/kubereboot/kured/pkg/conditions"
	"github.com/prometheus/common/expfmt"
	"github.com/prometheus/common/model"
	coordinationv1 "k8s.io/api/coordination/v1"
	v1 "k8s.io/api/core/v1"
	discoveryv1 "k8s.io/api/discovery/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"

	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	// defaults
	defaultScrapeInterval     = 60 * time.Second // find if a reboot is required on a node every 60s
	defaultScrapeTimeout      = 6 * time.Second
	leaseRenewInterval        = 15 * time.Second // renew leases randomly between 80% and 120% of the leaseRenewInterval
	nodeDetectorServiceName   = "node-reboot-detector"
	nodeRebootReporterAppName = "node-reboot-reporter"
	leasesGCInterval          = 1 * time.Hour // same garbage collection interval as kube api-server
	//leasesGCInterval = 10 * time.Second
	rebootRequiredConditionType   = "kured.dev/reboot-required"
	rebootRequiredConditionReason = "ScrapedResult"
)

var (
	version         = "unreleased"
	leaseTTLSeconds = int32(60)           // At least 4 times leaseRenewInterval, in seconds. Type is int32 to match with the Lease objects. Makes it easier than checking bounds for 64->32.
	gcGracePeriod   = 3 * leaseTTLSeconds // the garbage collection at each LeaseInterval tick will delete leases older than 3 * leaseTTLSeconds
)

// RebootDetectorInfo contains the parsed info from the node-reboot-detector's scraping
type RebootDetectorInfo struct {
	NodeName       string `json:"nodeName"`
	RebootRequired bool   `json:"rebootRequired"`
}

// Target represents a single scrape target.
type Target struct {
	ID   string // stable id (prefer targetRef UID or "ip:port")
	IP   string
	Port int32
	// failure tracking
	mu              sync.Mutex
	failCount       int
	lastFailTime    time.Time
	ScrapeInfo      RebootDetectorInfo
	lastSuccessTime time.Time
}

// ShouldSkip implements minimal exponential backoff to determine if the target should not be scraped because of previous failures
func (t *Target) ShouldSkip() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.failCount < 1 {
		return false
	}
	// backoff window = min(1<<failCount seconds, 300 s)
	backoffSec := 1 << (t.failCount - 1)
	if backoffSec > 300 {
		backoffSec = 300
	}
	if time.Since(t.lastFailTime) < time.Duration(backoffSec)*time.Second {
		return true
	}
	return false
}

// RecordFail stored info for exponential back off
func (t *Target) RecordFail() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failCount++
	t.lastFailTime = time.Now()
}

// RecordSuccess resets exponential back off
func (t *Target) RecordSuccess(rdInfo *RebootDetectorInfo) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.failCount = 0
	t.ScrapeInfo = *rdInfo
	t.lastSuccessTime = time.Now()
}

// TargetStore is thread-safe storage for targets.
type TargetStore struct {
	mu      sync.RWMutex
	targets map[string]*Target // keyed by ID
}

// NewTargetStore creates a new TargetStore containing node-detector nodes to scrape
func NewTargetStore() *TargetStore {
	return &TargetStore{targets: map[string]*Target{}}
}

// Upsert inserts or updates a target in the store based on their given IP:port.
func (s *TargetStore) Upsert(t *Target) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.targets[t.ID] = t
}

// Delete removes a node-detector target from the store based on their given IP:Port
func (s *TargetStore) Delete(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.targets, id)
}

// List returns a list of known nodes-detectors.
func (s *TargetStore) List() []*Target {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]*Target, 0, len(s.targets))
	for _, v := range s.targets {
		out = append(out, v)
	}
	return out
}

// Count returns the number of node-detectors in the store.
func (s *TargetStore) Count() int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return len(s.targets)
}

type peerMeta struct {
	RenewTime time.Time
	Duration  time.Duration
}

// PeerStore keeps a map of peers (keyed on pod_names or generated IDs)
type PeerStore struct {
	mu    sync.RWMutex
	peers map[string]peerMeta // leaseName -> peerMeta
}

// NewPeerStore initializes a new PeerStore holding the reboot-reporter peers
func NewPeerStore() *PeerStore {
	return &PeerStore{peers: map[string]peerMeta{}}
}

// List returns a list of reboot-reporter peers in the store.
func (p *PeerStore) List() []string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	now := time.Now()
	out := make([]string, 0, len(p.peers))
	for name, meta := range p.peers {
		if meta.RenewTime.Add(meta.Duration).After(now) {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

// UpsertLease inserts or updates a node-reporter's lease in the store based on their given name or generated ID.
func (p *PeerStore) UpsertLease(lease *coordinationv1.Lease) {
	p.mu.Lock()
	defer p.mu.Unlock()

	if lease == nil {
		return
	}

	renew := time.Now()
	if lease.Spec.RenewTime != nil {
		renew = lease.Spec.RenewTime.Time
	} else if lease.Spec.AcquireTime != nil {
		renew = lease.Spec.AcquireTime.Time
	}
	dur := time.Duration(leaseTTLSeconds) * time.Second
	if lease.Spec.LeaseDurationSeconds != nil {
		dur = time.Duration(*lease.Spec.LeaseDurationSeconds) * time.Second
	}
	p.peers[lease.Name] = peerMeta{RenewTime: renew, Duration: dur}
}

// DeleteLease removes a reboot reporter peer from the store based on their given name or generated ID.
func (p *PeerStore) DeleteLease(name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	delete(p.peers, name)
}

// Rendezvous hashing (HRW) using FNV64a
func hrwChoose(targetID string, peers []string) string {
	if len(peers) == 0 {
		return ""
	}
	var best string
	var bestScore uint64
	for _, peer := range peers {
		h := fnv.New64a()
		_, _ = h.Write([]byte(targetID))
		_, _ = h.Write([]byte("|"))
		_, _ = h.Write([]byte(peer))
		score := h.Sum64()
		if best == "" || score > bestScore {
			best = peer
			bestScore = score
		}
	}
	return best
}

// utility to generate random id if POD_NAME isn't set
//func genRandID() string {
//	b := make([]byte, 6)
//	_, err := rand.Read(b)
//	if err != nil {
//		return fmt.Sprintf("id-%d", time.Now().UnixNano())
//	}
//	return hex.EncodeToString(b)
//}

func randFloat() float64 {
	b := make([]byte, 1)
	if _, err := rand.Read(b); err != nil {
		return 0.42 // don't panic
	}
	return float64(b[0]) / 255.0

}

func main() {

	var (
		// CLI flags sorted alphabetically
		concurrency  int
		debug        bool
		kubeconfig   string
		logFormat    string
		namespace    string
		scrapeIntStr string
		timeoutStr   string
	)

	flag.IntVar(&concurrency, "concurrency", 40, "Concurrent scrapes")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")
	flag.StringVar(&kubeconfig, "kubeconfig", "", "Path to kubeconfig (optional)")
	flag.StringVar(&logFormat, "log-format", "json", "use text or json log format")
	flag.StringVar(&namespace, "namespace", "kube-system", "Namespace where Service and Leases live")
	flag.StringVar(&scrapeIntStr, "scrape-interval", "", "Scrape interval duration (e.g. 60s). Overrides default")
	flag.StringVar(&timeoutStr, "scrape-timeout", "", "Scrape timeout duration (e.g. 6s). Overrides default")
	flag.Parse()

	// Load flags from environment variables. Remember the prefix KURED_!
	cli.LoadFromEnv()

	var logger *slog.Logger
	handlerOpts := &slog.HandlerOptions{}
	if debug {
		handlerOpts.Level = slog.LevelDebug
	}
	switch logFormat {
	case "json":
		logger = slog.New(slog.NewJSONHandler(os.Stdout, handlerOpts))
	case "text":
		logger = slog.New(slog.NewTextHandler(os.Stdout, handlerOpts))
	default:
		logger = slog.New(slog.NewJSONHandler(os.Stdout, handlerOpts))
		logger.Info("incorrect configuration for logFormat, using json handler")
	}
	// For all the old calls using logger
	slog.SetDefault(logger)

	selfID := os.Getenv("POD_NAME")
	if selfID == "" {
		slog.Error("no POD_NAME env var set")
		os.Exit(1)
	}

	// parse durations
	scrapeInterval := defaultScrapeInterval
	if scrapeIntStr != "" {
		if d, err := time.ParseDuration(scrapeIntStr); err == nil {
			scrapeInterval = d
		}
	}
	scrapeTimeout := defaultScrapeTimeout
	if timeoutStr != "" {
		if d, err := time.ParseDuration(timeoutStr); err == nil {
			scrapeTimeout = d
		}
	}

	slog.Info("Starting node-reboot-reporter",
		"version", version,
		"namespace", namespace,
		"selfID", selfID,
		"scrapeInterval", scrapeInterval,
		"scrapeTimeout", scrapeTimeout,
		"concurrency", concurrency,
		"debug", debug,
	)

	// build k8s client
	var cfg *rest.Config
	var err error
	if kubeconfig != "" {
		cfg, err = clientcmd.BuildConfigFromFlags("", kubeconfig)
		if err != nil {
			slog.Error("failed to load kubeconfig while requesting explicitly to use one", "error", err)
			os.Exit(1)
		}
	} else {
		cfg, err = rest.InClusterConfig()
		if err != nil {
			// try default kubeconfig in home for dev
			home := os.Getenv("HOME")
			if home != "" {
				kpath := filepath.Join(home, ".kube", "config")
				cfg, err = clientcmd.BuildConfigFromFlags("", kpath)
				if err != nil {
					slog.Error("no kube config and cannot use in-cluster", "error", err)
					os.Exit(1)
				}
			} else {
				slog.Error("no kube config available", "error", err)
				os.Exit(1)
			}
		}
	}

	clientset, err := kubernetes.NewForConfig(cfg)
	if err != nil {
		slog.Error("create clientset", "error", err)
		os.Exit(1)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// stores
	tstore := NewTargetStore()
	pstore := NewPeerStore()

	// Setup informer factory for EndpointSlices and Leases in the provided namespace
	esFactory := informers.NewSharedInformerFactoryWithOptions(clientset, 0,
		informers.WithNamespace(namespace),
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			// select EndpointSlices for our service
			opts.LabelSelector = "kubernetes.io/service-name=" + nodeDetectorServiceName
		}),
	)

	esInformer := esFactory.Discovery().V1().EndpointSlices().Informer()

	leaseFactory := informers.NewSharedInformerFactoryWithOptions(clientset, 0,
		informers.WithNamespace(namespace),
		informers.WithTweakListOptions(func(opts *metav1.ListOptions) {
			// Only watch leases for this scraper group
			opts.LabelSelector = fmt.Sprintf("app=%s", nodeRebootReporterAppName)
		}),
	)
	leaseInformer := leaseFactory.Coordination().V1().Leases().Informer()

	// EndpointSlice handlers
	_, esInformerEHError := esInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			es := obj.(*discoveryv1.EndpointSlice)
			handleEndpointSliceUpsert(es, tstore)
		},
		UpdateFunc: func(_, newObj interface{}) {
			// I don't handle oldObj, but I should probably clean up/delete the old objects here... (prevent drift)
			es := newObj.(*discoveryv1.EndpointSlice)
			handleEndpointSliceUpsert(es, tstore)
		},
		DeleteFunc: func(obj interface{}) {
			es, ok := obj.(*discoveryv1.EndpointSlice)
			if !ok {
				// tombstone handling
				tombstone, ok := obj.(cache.DeletedFinalStateUnknown)
				if ok {
					es = tombstone.Obj.(*discoveryv1.EndpointSlice)
				} else {
					return
				}
			}
			handleEndpointSliceDelete(es, tstore)
		},
	})
	if esInformerEHError != nil {
		slog.Error("Failing to attach event handler", "error", err)
		os.Exit(2)
	}

	// Lease handlers
	_, leaseInformerEHError := leaseInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc: func(obj interface{}) {
			lease := obj.(*coordinationv1.Lease)
			pstore.UpsertLease(lease)
		},
		UpdateFunc: func(_, newObj interface{}) {
			lease := newObj.(*coordinationv1.Lease)
			pstore.UpsertLease(lease)
		},
		DeleteFunc: func(obj interface{}) {
			lease := obj.(*coordinationv1.Lease)
			pstore.DeleteLease(lease.Name)
		},
	})
	if leaseInformerEHError != nil {
		slog.Error("Failing to attach event handler", "error", err)
		os.Exit(2)
	}

	// start informers
	stopCh := make(chan struct{})
	defer close(stopCh)
	go esFactory.Start(stopCh)
	go leaseFactory.Start(stopCh)

	// Wait for Lease informer caches to sync
	if !cache.WaitForCacheSync(stopCh, leaseInformer.HasSynced) {
		slog.Error("failed waiting for lease informer sync", "error", err)
		os.Exit(2)
	}

	// Wait for EndpointSlice informer caches to sync
	if !cache.WaitForCacheSync(stopCh, esInformer.HasSynced) {
		slog.Error("failed waiting for endpoint slice informer sync", "error", err)
		os.Exit(2)
	}

	//go func() {
	//	for {
	//		jitter := time.Duration(float64(leaseRenewInterval) * (0.8 + 0.4*randFloat()))
	//		select {
	//
	//		case <-time.After(jitter):
	//			if err := createOrRenewLease(ctx, clientset, namespace, selfID); err != nil {
	//				slog.Warn("lease renew error", "error", err)
	//			}
	//		case <-ctx.Done():
	//			return
	//		}
	//	}
	//}()

	// Lease renewal goroutine
	// Cleanup improvement. I wonder which one is more readable, due to the risk being low of sending "After" to a closed channel...
	go func(ctx context.Context, clientset *kubernetes.Clientset, namespace, selfID string) {
		for {
			jitter := time.Duration(float64(leaseRenewInterval) * (0.8 + 0.4*randFloat()))

			timer := time.NewTimer(jitter)
			select {
			case <-timer.C:
				if err := createOrRenewLease(ctx, clientset, namespace, selfID); err != nil {
					slog.Warn("lease renew error", "error", err)
				}
			case <-ctx.Done():
				if !timer.Stop() {
					// timer already fired; drain the channel to avoid a "send" on a drained channel
					select {
					case <-timer.C:
					default:
					}
				}
				return
			}
		}
	}(ctx, clientset, namespace, selfID)

	// Immediately create/renew lease once so peers see us quickly
	if err := createOrRenewLease(ctx, clientset, namespace, selfID); err != nil {
		slog.Warn("initial lease create/renew failed", "error", err)
	}

	// Immediately add garbage collection of leases
	go gcLeases(ctx, pstore, clientset, namespace, selfID)

	// HTTP client for scrapes
	httpClient := &http.Client{
		Timeout: scrapeTimeout,
		Transport: &http.Transport{
			IdleConnTimeout:     30 * time.Second,
			MaxIdleConnsPerHost: 20,
		},
	}

	// Scrape and update loop
	// Do I need to have jitter?
	// Do I need to split the scrape and the update of the node?
	// Need to change the scrape interval and react on endpoint slices changes instead of doing it every scrape.
	go func() {
		ticker := time.NewTicker(scrapeInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ticker.C:
				peers := pstore.List()
				targets := tstore.List()
				owned := selectOwnedTargets(peers, selfID, targets)
				slog.Info("scrape-loop", "scraper_pods", len(peers), "targets", len(targets), "owned", len(owned))
				var wg sync.WaitGroup
				sem := make(chan struct{}, concurrency)
				for _, t := range owned {
					// skip if target in the backoff window
					if t.ShouldSkip() {
						slog.Info(fmt.Sprintf("skipping target %s (backoff)", t.ID))
						continue
					}
					wg.Add(1)
					sem <- struct{}{}
					go func(targ *Target) {
						defer wg.Done()
						defer func() { <-sem }()
						scrapeOneTarget(httpClient, targ)
						if targ.ScrapeInfo.NodeName != "" {
							// Not using a pointer to ensure copy of struct to ensure no heap usage.
							currentCondition := v1.NodeCondition{
								Type:               conditions.StringToConditionType(rebootRequiredConditionType),
								Status:             conditions.BoolToConditionStatus(targ.ScrapeInfo.RebootRequired),
								Reason:             rebootRequiredConditionReason,
								Message:            fmt.Sprintf("%s is posting reboot-required as %t", nodeDetectorServiceName, targ.ScrapeInfo.RebootRequired),
								LastHeartbeatTime:  metav1.Now(),
								LastTransitionTime: metav1.Now(),
							}
							if err := conditions.UpdateNodeCondition(ctx, clientset, targ.ScrapeInfo.NodeName, currentCondition); err != nil {
								slog.Error("failed to update node condition", "error", err)
							}
							slog.Debug(fmt.Sprintf("node %s condition heartbeat done, reboot required condition is %s", targ.ScrapeInfo.NodeName, currentCondition.Status))
						}
					}(t)
				}
				wg.Wait()
			case <-ctx.Done():
				return
			}
		}
	}()

	// block forever
	select {}
}

// handleEndpointSliceUpsert iterates endpoints and upserts targets to store
func handleEndpointSliceUpsert(es *discoveryv1.EndpointSlice, store *TargetStore) {
	// Use the first port in EndpointSlice.Ports if available (Service ports should match)
	var port int32
	if len(es.Ports) > 0 && es.Ports[0].Port != nil {
		port = *es.Ports[0].Port
	}
	for _, ep := range es.Endpoints {
		// prefer ready endpoints only (nil means unknown; treat unknown as not ready)
		if ep.Conditions.Ready != nil && !*ep.Conditions.Ready {
			continue
		}
		if len(ep.Addresses) == 0 {
			continue
		}
		for _, addr := range ep.Addresses {
			id := ""
			if ep.TargetRef != nil {
				// use targetRef kind/name to be stable across IP changes if possible
				id = fmt.Sprintf("%s/%s", ep.TargetRef.Kind, ep.TargetRef.Name)
			} else {
				id = fmt.Sprintf("%s:%d", addr, port)
			}
			t := &Target{
				ID:   id,
				IP:   addr,
				Port: port,
			}
			store.Upsert(t)
		}
	}
}

// handleEndpointSliceDelete removes targets that belonged to that slice
func handleEndpointSliceDelete(es *discoveryv1.EndpointSlice, store *TargetStore) {
	var port int32
	if len(es.Ports) > 0 && es.Ports[0].Port != nil {
		port = *es.Ports[0].Port
	}
	for _, ep := range es.Endpoints {
		if len(ep.Addresses) == 0 {
			continue
		}
		for _, addr := range ep.Addresses {
			id := ""
			if ep.TargetRef != nil {
				id = fmt.Sprintf("%s/%s", ep.TargetRef.Kind, ep.TargetRef.Name)
			} else {
				id = fmt.Sprintf("%s:%d", addr, port)
			}
			store.Delete(id)
		}
	}
}

// createOrRenewLease creates or updates a Lease with holder identity = my pod name. It's not clean as deployment hash, but it will do (we run a garbage collection goroutine).
func createOrRenewLease(ctx context.Context, client *kubernetes.Clientset, namespace, selfID string) error {
	leases := client.CoordinationV1().Leases(namespace)
	now := metav1.NowMicro()

	leaseObj := &coordinationv1.Lease{
		ObjectMeta: metav1.ObjectMeta{
			Name: selfID,
			Labels: map[string]string{
				"app": nodeRebootReporterAppName,
			},
		},
		Spec: coordinationv1.LeaseSpec{
			HolderIdentity:       &selfID,
			AcquireTime:          &now,
			RenewTime:            &now,
			LeaseDurationSeconds: &leaseTTLSeconds,
		},
	}

	existing, err := leases.Get(ctx, selfID, metav1.GetOptions{})
	if err == nil {
		// Only update if nearing expiry (avoid unnecessary writes)
		if existing.Spec.RenewTime != nil &&
			time.Since(existing.Spec.RenewTime.Time) < time.Duration(leaseTTLSeconds/3)*time.Second {
			return nil
		}
		existing.Spec.RenewTime = &now
		existing.Spec.HolderIdentity = &selfID
		_, err = leases.Update(ctx, existing, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("lease update: %w", err)
		}
		return nil
	}

	// If the previous Get leases fails, just create a new lease.
	_, err = leases.Create(ctx, leaseObj, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("lease create: %w", err)
	}
	return nil
}

// selectOwnedTargets returns the subset of targets, owned by this instance, according to HRW
func selectOwnedTargets(peers []string, selfID string, targets []*Target) []*Target {
	if len(peers) == 0 {
		// if no peers, own everything
		return targets
	}
	owned := make([]*Target, 0)
	for _, t := range targets {
		owner := hrwChoose(t.ID, peers)
		if owner == selfID {
			owned = append(owned, t)
		}
	}
	return owned
}

// scrapeOneTarget performs a single HTTP GET /metrics; records success/failure on target
func scrapeOneTarget(client *http.Client, t *Target) {
	// construct host:port (handle IPv6)
	hostPort := net.JoinHostPort(t.IP, strconv.Itoa(int(t.Port)))
	url := fmt.Sprintf("http://%s/metrics", hostPort)
	req, _ := http.NewRequest("GET", url, nil)
	req.Header.Set("User-Agent", fmt.Sprintf("%s/1.0", nodeRebootReporterAppName))
	// simple context handled by client's timeout
	resp, err := client.Do(req)
	if err != nil {
		slog.Info("scrape failed", "error", err, "target", hostPort)
		t.RecordFail()
		return
	}
	defer func(Body io.ReadCloser) {
		err := Body.Close()
		if err != nil {
			slog.Info("could not close connection after scrape", "error", err, "target", hostPort)
		}
	}(resp.Body)
	// treat 2xx as success; otherwise failure
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		slog.Info("scrape fail", "target", hostPort, "status", resp.StatusCode)
		t.RecordFail()
		return
	}

	scrapeInfo, err := parseMetricValue(resp.Body)
	if err != nil {
		slog.Info("metric parse failed", "error", err, "target", hostPort)
		t.RecordFail()
		return
	}

	slog.Debug("scrape ok", "target", hostPort, "status", resp.StatusCode, "rebootRequired", scrapeInfo.RebootRequired, "nodeName", scrapeInfo.NodeName) //todo
	t.RecordSuccess(scrapeInfo)
}

func parseMetricValue(body io.Reader) (*RebootDetectorInfo, error) {
	rdInfo := &RebootDetectorInfo{}

	//need to use a pool of parsers to improve performance.
	//for that, I will need to use the following two lines here:
	//parser := parserPool.Get().(*expfmt.TextParser)
	//defer parserPool.Put(parser) // return it after use
	// and define my parser pool somewhere else:
	//var parserPool = sync.Pool{
	//	New: func() any {
	//		return &expfmt.TextParser{}
	//	},
	//}
	// it's an optimization I will keep for later.

	parser := expfmt.NewTextParser(model.LegacyValidation)

	families, err := parser.TextToMetricFamilies(body)
	if err != nil {
		slog.Info("metric parse failed", "error", err)
		return nil, err
	}

	mf, ok := families["kured_reboot_required"]
	if !ok {
		slog.Debug("metric kured_reboot_required not found")
		return nil, fmt.Errorf("metric kured_reboot_required not found (you are maybe targetting the wrong service)")
	}

	// Loop through metrics and print
	for _, m := range mf.Metric {
		for _, lp := range m.Label {
			if lp.GetName() == "node" {
				rdInfo.NodeName = lp.GetValue()
				rdInfo.RebootRequired = m.GetGauge().GetValue() == 1
			}
		}
	}
	if rdInfo.NodeName == "" {
		slog.Debug("no node found within metric kured_reboot_required")
		return nil, fmt.Errorf("kured_reboot_required without node label")
	}
	return rdInfo, nil
}

func gcLeases(ctx context.Context, pstore *PeerStore, clientset *kubernetes.Clientset, namespace, selfID string) {
	for {
		select {
		case <-time.After(leasesGCInterval):
			slog.Info("[gc] Starting  lease cleanup")
			peers := pstore.List()
			if len(peers) == 0 {
				continue
			}

			// pick a single "cleaner" peer deterministically (List is sorted)
			cleaner := peers[0]
			if selfID != cleaner {
				slog.Debug("[gc] will not clean up leases, not the cleaner", "cleaner", cleaner)
				continue
			}

			leasesClient := clientset.CoordinationV1().Leases(namespace)
			list, err := leasesClient.List(ctx, metav1.ListOptions{
				LabelSelector: fmt.Sprintf("app=%s", nodeRebootReporterAppName),
			})
			if err != nil {
				slog.Debug("[gc] list leases error", "error", err) // Ignoring this, will retry next time.
				continue
			}

			cutoff := time.Now().Add(-time.Duration(gcGracePeriod) * time.Second)
			deleted := 0
			for _, l := range list.Items {
				if l.Spec.RenewTime == nil {
					continue
				}
				if l.Spec.RenewTime.Time.Before(cutoff) {
					err := leasesClient.Delete(ctx, l.Name, metav1.DeleteOptions{})
					if err == nil {
						deleted++
					}
				}
			}

			if deleted > 0 {
				slog.Info("[gc] cleaned up leases", "deleted", deleted)
			}

		case <-ctx.Done():
			return
		}
	}
}
