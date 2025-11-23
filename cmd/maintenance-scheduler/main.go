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
	"github.com/kubereboot/kured/internal/controllers"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	metricsserver "sigs.k8s.io/controller-runtime/pkg/metrics/server"
)

var (
	version    = "unreleased"
	scheme     = runtime.NewScheme()
	binaryName = "maintenance-scheduler"
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))
	//prometheus.MustRegister(maintenances.ActiveMaintenanceWindowGauge)
}

func main() {
	var (
		// Please continue sorting alphabetically :)
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
	flag.StringVar(&cmNamespace, "cm-namespace", "kube-system", "Namespace where maintenance configmaps live")
	flag.StringVar(&cmPrefix, "config-prefix", "kured-maintenance-", "maintenance configmap prefix")
	flag.IntVar(&concurrency, "concurrency", 1, "Number of concurrent nodes actively in maintenance")
	flag.BoolVar(&debug, "debug", false, "Enable debug logging")
	//flag.StringVar(&kubeconfig, "kubeconfig", "", "optional kubeconfig")
	flag.StringVar(&logFormat, "log-format", "json", "use text or json log format")
	flag.StringVar(&metricsHost, "metrics-host", "", "host where metrics will listen")
	flag.IntVar(&metricsPort, "metrics-port", 8080, "port number where metrics will listen")
	flag.StringVar(&namespace, "leader-election-namespace", "kube-system", "Namespace for leader election lock")
	flag.DurationVar(&period, "period", time.Minute, "controller resync and maintenance assigner period")
	flag.StringVar(&probeBindAddress, "health-probe-bind-address", ":8081", "The address the probe endpoint binds to.")

	flag.Parse()

	// Load flags from environment variables. Remember the prefix KURED_!
	cli.LoadFromEnv()

	// set up signals so we handle the shutdown signal gracefully
	//ctx := ctrl.SetupSignalHandler()

	logger := cli.NewLogger(debug, logFormat)
	// For all the old calls using logger
	slog.SetDefault(logger)
	// Adapters for slog
	//cronLogger := &cli.CronSlogAdapter{Logger: logger}
	ctrl.SetLogger(logr.FromSlogHandler(logger.Handler()))
	setupLog := ctrl.Log.WithName(binaryName)

	//scheme := runtime.NewScheme()
	//_ = corev1.AddToScheme(scheme)

	metricsBindAddress := metricsHost + ":" + strconv.Itoa(metricsPort)
	setupLog.Info("Setting up manager")
	mgr, errNewManager := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
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

	setupLog.Info("Setting up controller")
	if err := (&controllers.MaintenanceSchedulerNodeReconciler{
		Client: mgr.GetClient(),
		Scheme: mgr.GetScheme(),
	}).SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", controllers.MaintenanceWindowControllerName)
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

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		os.Exit(1)
	}

	///////////////// OLD CONTROLLER LOGIC
	//
	//// Core initialisation, error 1 on failure.
	//client := cli.KubernetesClientSetOrDie("", kubeconfig)
	//windows, parsingError := maintenances.FetchConfigmaps(ctx, client, cmNamespace, cmPrefix)
	//if parsingError != nil {
	//	slog.Error("failed to parse a maintenance window", "error", parsingError.Error())
	//	os.Exit(1)
	//}
	//
	//slog.Info("Starting maintenance-scheduler",
	//	"version", version,
	//	"period", period,
	//	"metricsHost", metricsHost,
	//	"metricsPort", metricsPort,
	//	"debug", debug,
	//
	//	"cmPrefix", cmPrefix,
	//	"knownMaintenanceWindows", len(windows),
	//)
	//
	//// Todo, update the controller to dynamically reload windows without restart by watching cms
	//// delete of the cm would delete the attached cronjobs
	//// modification would delete and add the cronjobs
	//// addition would add the cronjobs
	//slog.Info("You must reload this deployment in case of new or updated maintenance window(s)")
	//
	//mw := maintenances.NewWindows(windows)
	//
	//// Maintenance manager setup
	//c := cronlib.New(cronlib.WithLogger(cronLogger), cronlib.WithChain(cronlib.SkipIfStillRunning(cronLogger)))
	//for _, window := range mw.AllWindows {
	//	logger.Debug("Loading maintenance window into cron", "window", window.Name, "schedule", window.Schedule)
	//	if _, err := c.AddFunc(window.Schedule, mw.Run(window.Name, logger)); err != nil {
	//		slog.Error("Failed to load maintenance windows into cron", "error", err.Error())
	//		os.Exit(1)
	//	}
	//}
	//c.Start()
	////
	////// Controller handling node events to guarantee the placement of nodes into maintenance if they belong to an active window
	////kubeInformerFactory := informers.NewSharedInformerFactory(client, period)
	////controller := NewController(logger, client,
	////	kubeInformerFactory.Core().V1().Nodes(),
	////	mw,
	////	concurrency,
	////	period,
	////)
	////
	////// The Start method is non-blocking and runs all registered informers in a dedicated goroutine.
	////kubeInformerFactory.Start(ctx.Done())
	////
	////go func() {
	////	if err := controller.Run(ctx, 2); err != nil {
	////		os.Exit(1)
	////	}
	////}()
	//
	//// Closes on Ctrl-C
	////http.Handle("/metrics", promhttp.Handler())
	////if err := http.ListenAndServe(fmt.Sprintf("%s:%d", metricsHost, metricsPort), nil); err != nil {
	////	slog.Error(fmt.Sprintf("unrecoverable error - failed to listen on metrics port: %v", err))
	////	os.Exit(1)
	////} // #nosec G114
}
