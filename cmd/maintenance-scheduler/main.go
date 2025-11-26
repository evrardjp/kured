package main

import (
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"time"

	"github.com/go-logr/logr"
	"github.com/kubereboot/kured/internal/cli"
	"github.com/kubereboot/kured/internal/conditions"
	"github.com/kubereboot/kured/internal/maintenances"
	"github.com/robfig/cron/v3"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/client-go/kubernetes"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/cache"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	version    = "unreleased"
	scheme     = runtime.NewScheme()
	binaryName = "maintenance-scheduler"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	metrics.Registry.Register(maintenances.ActiveWindowsGauge)
	metrics.Registry.Register(maintenances.NodesInProgressGauge)
}

func main() {
	var (
		// Please continue sorting alphabetically :)
		cmLabelKey  string
		cmNamespace string
		cmPrefix    string
		concurrency int
		debug       bool
		//kubeconfig  string
		logFormat        string
		metricsHost      string
		metricsPort      int
		namespace        string
		period           time.Duration
		probeBindAddress string
	)
	flag.StringVar(&cmLabelKey, "cm-label-key", "kured.dev/maintenance", "Label key to identify maintenance configmaps")
	flag.StringVar(&cmNamespace, "cm-namespace", "kube-system", "Namespace where maintenance configmaps live")
	flag.StringVar(&cmPrefix, "config-prefix", "kured-maintenance-", "maintenance configmap prefix")
	flag.IntVar(&concurrency, "concurrency", 1, "Number of concurrent nodes actively in maintenance")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")
	//flag.StringVar(&kubeconfig, "kubeconfig", "", "optional kubeconfig")
	flag.StringVar(&logFormat, "log-format", "json", "use text or json log format")
	flag.StringVar(&metricsHost, "metrics-host", "", "host where metrics will listen")
	flag.IntVar(&metricsPort, "metrics-port", 8080, "port number where metrics will listen")
	flag.StringVar(&namespace, "leader-election-namespace", "kube-system", "Namespace for leader election lock")
	flag.DurationVar(&period, "period", time.Minute, "controller resync and maintenance assigner heartbeat period")
	flag.StringVar(&probeBindAddress, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")

	flag.Parse()

	// Load flags from environment variables. Remember the prefix KURED_!
	cli.LoadFromEnv()

	// set up signals so we handle the shutdown signal gracefully
	ctx := ctrl.SetupSignalHandler()

	// Setup all loggers
	logger := cli.NewLogger(debug, logFormat)

	slog.SetDefault(logger)                                // for generic log.logger
	cronLogger := &cli.CronSlogAdapter{Logger: logger}     // for robfig cron's logger
	ctrl.SetLogger(logr.FromSlogHandler(logger.Handler())) // for controller-runtime's logger
	setupLog := ctrl.Log.WithName(binaryName)              // for controller-runtime logger
	klog.SetLogger(logr.FromSlogHandler(logger.Handler())) // for client-go's tool loggers (appears in leader election)

	// At all times all the pods of this controller need to know the maintenance windows,
	// so that they can take over gracefully in case of leader election.
	config := ctrl.GetConfigOrDie()
	client := kubernetes.NewForConfigOrDie(config)

	windows, parsingError := maintenances.FetchConfigmaps(ctx, client, cmNamespace, cmPrefix)
	if parsingError != nil {
		slog.Error("failed to parse a maintenance window", "error", parsingError.Error())
		os.Exit(1)
	}

	slog.Info("Starting maintenance-scheduler",
		"version", version,
		"period", period,
		"metricsHost", metricsHost,
		"metricsPort", metricsPort,
		"debug", debug,

		"cmPrefix", cmPrefix,
		"knownMaintenanceWindows", len(windows),
	)

	// we take a stance here: if you reload your cm, don't touch conditions, leave them as is, until we have completely reloaded
	// This will be easier than having to deal with:
	// - race conditions between n controllers and exchange all data that needs deleting/adding
	// - stopping already running jobs
	slog.Info("Any change in a watched maintenance window (configmap) WILL result in a restart of this pod, regardless of maintenance progress")

	mw := maintenances.NewWindows(windows...)
	// Maintenance window schedule is ALWAYS active, regardless or not the manager is the leader or the follower
	// It makes it easy for a leader election of the node controller to have all the information about the maintenances.
	// This is why we reboot the whole controller on a cm change.
	c := cron.New(cron.WithLogger(cronLogger), cron.WithChain(cron.SkipIfStillRunning(cronLogger)))
	for _, window := range mw.AllWindows {
		logger.Debug("Loading maintenance window into cron", "window", window.Name, "schedule", window.Schedule)
		if _, err := c.AddFunc(window.Schedule, mw.Run(window.Name, logger)); err != nil {
			slog.Error("Failed to load maintenance windows into cron", "error", err.Error())
			os.Exit(1)
		}
	}
	c.Start()
	// Only to look good in linting, as we crash stuff with os.Exit in controllers :)
	defer c.Stop()

	// Need a separate manager, because we only want to watch on namespace
	cmMgr, errNewCMManager := ctrl.NewManager(config, ctrl.Options{
		Scheme: scheme,
		Cache: cache.Options{
			DefaultNamespaces: map[string]cache.Config{
				cmNamespace: {},
			},
		},
		LeaderElection: false,
		Metrics: metricsserver.Options{
			BindAddress: "0",
		},
	})
	if errNewCMManager != nil {
		setupLog.Error(errNewCMManager, "unable to start manager")
		os.Exit(1)
	}

	setupLog.Info("Setting up cm controller")
	if err := (&maintenances.MaintenanceSchedulerCMReconciler{
		Client:              cmMgr.GetClient(),
		Scheme:              cmMgr.GetScheme(),
		Logger:              logger,
		ConfigMapNamespaces: cmNamespace,
		ConfigMapPrefix:     cmPrefix,
		ConfigMapLabelKey:   cmLabelKey,
		MaintenanceWindows:  mw,
	}).SetupWithManager(cmMgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", maintenances.MaintenanceWindowCMControllerName)
		os.Exit(1)
	}

	go func() {
		setupLog.Info("starting cm manager")
		if err := cmMgr.Start(ctx); err != nil {
			setupLog.Error(err, "problem running manager")
			os.Exit(1)
		}
	}()

	metricsBindAddress := metricsHost + ":" + strconv.Itoa(metricsPort)
	setupLog.Info("Setting up manager")
	mgr, errNewManager := ctrl.NewManager(config, ctrl.Options{
		Scheme: scheme,
		Metrics: metricsserver.Options{
			BindAddress:   metricsBindAddress,
			SecureServing: false,
		},
		HealthProbeBindAddress:  probeBindAddress,
		LeaderElectionNamespace: namespace,
		LeaderElection:          true,
		LeaderElectionID:        fmt.Sprintf("%s-leader-election.kured.dev", binaryName),
	})
	if errNewManager != nil {
		setupLog.Error(errNewManager, "unable to start manager")
		os.Exit(1)
	}

	setupLog.Info("Setting up node controller")
	if err := (&maintenances.MaintenanceSchedulerNodeReconciler{
		Client:                    mgr.GetClient(),
		Scheme:                    mgr.GetScheme(),
		MaintenanceWindows:        mw,
		ConditionHeartbeatPeriod:  period,
		Logger:                    logger,
		RequiredConditionTypes:    []string{conditions.RebootRequiredConditionType, conditions.UnderMaintenanceConditionType},
		ForbiddenConditionTypes:   []string{conditions.InhibitedRebootConditionType},
		MaximumNodesInMaintenance: concurrency,
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", maintenances.MaintenanceWindowNodeControllerName)
		os.Exit(1)
	}

	if err := mgr.AddHealthzCheck("healthz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		os.Exit(1)
	}

	// Eventually move to a builtin healthcheck from metrics
	// https://github.com/kubernetes-sigs/controller-runtime/issues/3238
	if err := mgr.AddReadyzCheck("readyz", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		os.Exit(1)
	}

	setupLog.Info("starting node manager")
	if err := mgr.Start(ctx); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}

	//// Closes on Ctrl-C
	////http.Handle("/metrics", promhttp.Handler())
	////if err := http.ListenAndServe(fmt.Sprintf("%s:%d", metricsHost, metricsPort), nil); err != nil {
	////	slog.Error(fmt.Sprintf("unrecoverable error - failed to listen on metrics port: %v", err))
	////	os.Exit(1)
	////} // #nosec G114
}
