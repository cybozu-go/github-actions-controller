package cmd

import (
	"fmt"
	"net"
	"strconv"

	actionsv1alpha1 "github.com/cybozu-go/github-actions-controller/api/v1alpha1"
	"github.com/cybozu-go/github-actions-controller/controllers"
	"github.com/cybozu-go/github-actions-controller/github"
	"github.com/cybozu-go/github-actions-controller/hooks"
	"github.com/cybozu-go/github-actions-controller/metrics"
	rc "github.com/cybozu-go/github-actions-controller/runner/client"
	"k8s.io/apimachinery/pkg/runtime"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/healthz"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	k8sMetrics "sigs.k8s.io/controller-runtime/pkg/metrics"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	// +kubebuilder:scaffold:imports
)

var (
	scheme   = runtime.NewScheme()
	setupLog = ctrl.Log.WithName("setup")
)

func init() {
	utilruntime.Must(clientgoscheme.AddToScheme(scheme))

	utilruntime.Must(actionsv1alpha1.AddToScheme(scheme))
	//+kubebuilder:scaffold:scheme

}

func run() error {
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&config.zapOpts)))

	host, p, err := net.SplitHostPort(config.webhookAddr)
	if err != nil {
		return fmt.Errorf("invalid webhook address: %s, %v", config.webhookAddr, err)
	}
	port, err := strconv.Atoi(p)
	if err != nil {
		return fmt.Errorf("invalid webhook address: %s, %v", config.webhookAddr, err)
	}

	mgr, err := ctrl.NewManager(ctrl.GetConfigOrDie(), ctrl.Options{
		Scheme:                 scheme,
		MetricsBindAddress:     config.metricsAddr,
		Host:                   host,
		Port:                   port,
		HealthProbeBindAddress: config.probeAddr,
		LeaderElection:         true,
		LeaderElectionID:       "6bee5a22.cybozu.com",
	})
	if err != nil {
		setupLog.Error(err, "unable to start manager")
		return err
	}
	metrics.InitControllerMetrics(k8sMetrics.Registry)

	githubClient, err := github.NewClient(
		config.appID,
		config.appInstallationID,
		config.appPrivateKeyPath,
		config.organizationName,
	)
	if err != nil {
		setupLog.Error(err, "unable to create github client", "controller", "RunnerPool")
		return err
	}

	dec, err := admission.NewDecoder(scheme)
	if err != nil {
		setupLog.Error(err, "unable to create decoder", "controller", "RunnerPool")
		return err
	}
	wh := mgr.GetWebhookServer()
	wh.Register("/pod/mutate", hooks.NewPodMutator(
		mgr.GetClient(),
		ctrl.Log.WithName("actions-token-pod-mutator"),
		dec,
		githubClient,
	))

	runnerManager := controllers.NewRunnerManager(
		ctrl.Log.WithName("RunnerManager"),
		config.runnerManagerInterval,
		mgr.GetClient(),
		githubClient,
		rc.NewClient())

	reconciler := controllers.NewRunnerPoolReconciler(
		mgr.GetClient(),
		ctrl.Log.WithName("controllers").WithName("RunnerPool"),
		mgr.GetScheme(),
		config.repositoryNames,
		config.organizationName,
		config.runnerImage,
		runnerManager,
	)
	if err = reconciler.SetupWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create controller", "controller", "runner-pool-reconciler")
		return err
	}

	if err = (&actionsv1alpha1.RunnerPool{}).SetupWebhookWithManager(mgr); err != nil {
		setupLog.Error(err, "unable to create webhook", "webhook", "RunnerPool")
		return err
	}

	// +kubebuilder:scaffold:builder

	if err := mgr.AddHealthzCheck("ping", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up health check")
		return err
	}
	if err := mgr.AddReadyzCheck("ping", healthz.Ping); err != nil {
		setupLog.Error(err, "unable to set up ready check")
		return err
	}

	setupLog.Info("starting manager")
	if err := mgr.Start(ctrl.SetupSignalHandler()); err != nil {
		setupLog.Error(err, "problem running manager")
		return err
	}
	return nil
}
