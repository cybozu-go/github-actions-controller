package controllers

import (
	"context"
	"encoding/json"
	"fmt"

	constants "github.com/cybozu-go/meows"
	meowsv1alpha1 "github.com/cybozu-go/meows/api/v1alpha1"
	"github.com/cybozu-go/meows/runner"
	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"
)

// RunnerPoolReconciler reconciles a RunnerPool object
type RunnerPoolReconciler struct {
	client.Client
	log              logr.Logger
	scheme           *runtime.Scheme
	repositoryNames  []string
	organizationName string
	runnerImage      string
	runnerManager    RunnerManager
}

// NewRunnerPoolReconciler creates RunnerPoolReconciler
func NewRunnerPoolReconciler(client client.Client, log logr.Logger, scheme *runtime.Scheme, repositoryNames []string, organizationName, runnerImage string, runnerManager RunnerManager) *RunnerPoolReconciler {
	return &RunnerPoolReconciler{
		Client:           client,
		log:              log,
		scheme:           scheme,
		repositoryNames:  repositoryNames,
		organizationName: organizationName,
		runnerImage:      runnerImage,
		runnerManager:    runnerManager,
	}
}

//+kubebuilder:rbac:groups=meows.cybozu.com,resources=runnerpools,verbs=get;list;watch;create;update;patch;delete
//+kubebuilder:rbac:groups=meows.cybozu.com,resources=runnerpools/status,verbs=get;update;patch
//+kubebuilder:rbac:groups=apps,resources=deployments,verbs=get;list;watch;create;update;patch;delete

// Reconcile is part of the main kubernetes reconciliation loop which aims to
// move the current state of the cluster closer to the desired state.
func (r *RunnerPoolReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := r.log.WithValues("runnerpool", req.NamespacedName)
	log.Info("start reconciliation loop")

	rp := &meowsv1alpha1.RunnerPool{}
	if err := r.Get(ctx, req.NamespacedName, rp); err != nil {
		if apierrors.IsNotFound(err) {
			log.Info("runnerpool is not found")
			return ctrl.Result{}, nil
		}
		log.Error(err, "unable to get RunnerPool")
		return ctrl.Result{}, err
	}

	if rp.ObjectMeta.DeletionTimestamp != nil {
		if !controllerutil.ContainsFinalizer(rp, constants.RunnerPoolFinalizer) {
			return ctrl.Result{}, nil
		}

		log.Info("start finalizing RunnerPool")

		if err := r.finalize(ctx, log, rp); err != nil {
			log.Error(err, "failed to finalize")
			return ctrl.Result{}, err
		}

		if err := r.runnerManager.Stop(ctx, rp); err != nil {
			log.Error(err, "failed to stop runner manager")
			return ctrl.Result{}, err
		}

		controllerutil.RemoveFinalizer(rp, constants.RunnerPoolFinalizer)
		if err := r.Update(ctx, rp); err != nil {
			log.Error(err, "failed to remove finalizer")
			return ctrl.Result{}, err
		}

		log.Info("finalizing RunnerPool is completed")
		return ctrl.Result{}, nil
	}

	if err := r.validateRepositoryName(rp); err != nil {
		log.Error(err, "failed to validate repository name")
		return ctrl.Result{}, err
	}

	if err := r.reconcileDeployment(ctx, log, rp); err != nil {
		log.Error(err, "failed to reconcile deployment")
		return ctrl.Result{}, err
	}

	r.runnerManager.StartOrUpdate(rp)

	rp.Status.Bound = true
	if err := r.Status().Update(ctx, rp); err != nil {
		log.Error(err, "failed to update status")
		return ctrl.Result{}, err
	}
	return ctrl.Result{}, nil
}

// SetupWithManager sets up the controller with the Manager.
func (r *RunnerPoolReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&meowsv1alpha1.RunnerPool{}).
		Owns(&appsv1.Deployment{}).
		Complete(r)
}

func (r *RunnerPoolReconciler) validateRepositoryName(rp *meowsv1alpha1.RunnerPool) error {
	for _, n := range r.repositoryNames {
		if n == rp.Spec.RepositoryName {
			return nil
		}
	}
	return fmt.Errorf("found the invalid repository name %v. Valid repository names are %v", rp.Spec.RepositoryName, r.repositoryNames)
}

func (r *RunnerPoolReconciler) finalize(ctx context.Context, log logr.Logger, rp *meowsv1alpha1.RunnerPool) error {
	d := &appsv1.Deployment{}
	d.SetNamespace(rp.GetNamespace())
	d.SetName(rp.GetRunnerDeploymentName())
	if err := r.Delete(ctx, d); err != nil {
		if !apierrors.IsNotFound(err) {
			log.Error(err, "failed to delete deployment")
			return err
		}
	}
	return nil
}

func labelSet(rp *meowsv1alpha1.RunnerPool, component string) map[string]string {
	labels := map[string]string{
		constants.AppNameLabelKey:      constants.AppName,
		constants.AppComponentLabelKey: component,
		constants.AppInstanceLabelKey:  rp.Name,
	}
	return labels
}

func labelSetForRunnerPod(rp *meowsv1alpha1.RunnerPool, organizationName string) map[string]string {
	labels := labelSet(rp, constants.AppComponentRunner)
	labels[constants.RunnerOrgLabelKey] = organizationName
	labels[constants.RunnerRepoLabelKey] = rp.Spec.RepositoryName
	return labels
}

func mergeMap(m1, m2 map[string]string) map[string]string {
	m := make(map[string]string)
	for k, v := range m1 {
		m[k] = v
	}
	for k, v := range m2 {
		m[k] = v
	}
	if len(m) == 0 {
		return nil
	}
	return m
}

func (r *RunnerPoolReconciler) reconcileDeployment(ctx context.Context, log logr.Logger, rp *meowsv1alpha1.RunnerPool) error {
	d := &appsv1.Deployment{}
	d.SetNamespace(rp.GetNamespace())
	d.SetName(rp.GetRunnerDeploymentName())

	var orig, updated *appsv1.DeploymentSpec
	op, err := ctrl.CreateOrUpdate(ctx, r.Client, d, func() error {
		orig = d.Spec.DeepCopy()

		d.Labels = mergeMap(d.GetLabels(), labelSet(rp, constants.AppComponentRunner))
		d.Spec.Selector = &metav1.LabelSelector{
			MatchLabels: labelSet(rp, constants.AppComponentRunner),
		}

		d.Spec.Template.Labels = mergeMap(d.Spec.Template.GetLabels(), rp.Spec.Template.ObjectMeta.Labels)
		d.Spec.Template.Labels = mergeMap(d.Spec.Template.GetLabels(), labelSetForRunnerPod(rp, r.organizationName))
		d.Spec.Template.Annotations = mergeMap(d.Spec.Template.GetAnnotations(), rp.Spec.Template.ObjectMeta.Annotations)

		d.Spec.Replicas = pointer.Int32Ptr(rp.Spec.Replicas)
		d.Spec.Template.Spec.ServiceAccountName = rp.Spec.Template.ServiceAccountName
		d.Spec.Template.Spec.ImagePullSecrets = rp.Spec.Template.ImagePullSecrets

		varDir := "var-dir"
		workDir := "work-dir"
		volumes := append(rp.Spec.Template.Volumes, corev1.Volume{
			Name: varDir,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		}, corev1.Volume{
			Name: workDir,
			VolumeSource: corev1.VolumeSource{
				EmptyDir: &corev1.EmptyDirVolumeSource{},
			},
		})
		d.Spec.Template.Spec.Volumes = volumes

		r.addRunnerContainerIfNotExists(d)
		runnerContainer := r.findRunnerContainer(d)

		// Update the runner container.
		if rp.Spec.Template.Image != "" {
			runnerContainer.Image = rp.Spec.Template.Image
		} else {
			runnerContainer.Image = r.runnerImage
		}
		if rp.Spec.Template.ImagePullPolicy != "" {
			runnerContainer.ImagePullPolicy = rp.Spec.Template.ImagePullPolicy
		}
		runnerContainer.SecurityContext = rp.Spec.Template.SecurityContext
		runnerContainer.Resources = rp.Spec.Template.Resources
		runnerContainer.Ports = r.makeRunnerContainerPorts()

		volumeMounts := append(rp.Spec.Template.VolumeMounts, corev1.VolumeMount{
			Name:      varDir,
			MountPath: constants.RunnerVarDirPath,
		}, corev1.VolumeMount{
			Name:      workDir,
			MountPath: constants.RunnerWorkDirPath,
		})
		runnerContainer.VolumeMounts = volumeMounts

		env, err := r.makeRunnerContainerEnv(rp)
		if err != nil {
			return err
		}
		runnerContainer.Env = env

		updated = d.Spec.DeepCopy()
		return ctrl.SetControllerReference(rp, d, r.scheme)
	})

	if err != nil {
		log.Error(err, "failed to reconcile deployment")
		return err
	}
	switch op {
	case controllerutil.OperationResultCreated:
		log.Info("reconciled deployment", "operation", string(op))
	case controllerutil.OperationResultUpdated:
		// The deployment update should occur only when the users update their RunnerPool CR.
		// If this log shows frequently, users need to review their RunnerPool CR.
		log.Info("reconciled deployment", "operation", string(op), "diff", cmp.Diff(orig, updated))
	}
	return nil
}

func (r *RunnerPoolReconciler) findRunnerContainer(d *appsv1.Deployment) *corev1.Container {
	for i := range d.Spec.Template.Spec.Containers {
		c := &d.Spec.Template.Spec.Containers[i]
		if c.Name == constants.RunnerContainerName {
			return c
		}
	}
	return nil
}

func (r *RunnerPoolReconciler) addRunnerContainerIfNotExists(d *appsv1.Deployment) {
	if c := r.findRunnerContainer(d); c != nil {
		// When the runner container already exists, nothing to do.
		return
	}

	// Create the runner container.
	c := corev1.Container{
		Name: constants.RunnerContainerName,
	}
	d.Spec.Template.Spec.Containers = append(d.Spec.Template.Spec.Containers, c)
}

func (r *RunnerPoolReconciler) makeRunnerContainerEnv(rp *meowsv1alpha1.RunnerPool) ([]corev1.EnvVar, error) {
	option := runner.Option{
		SetupCommand:          rp.Spec.SetupCommand,
		SlackAgentServiceName: rp.Spec.SlackAgent.ServiceName,
		SlackChannel:          rp.Spec.SlackAgent.Channel,
	}
	optionJson, err := json.Marshal(&option)
	if err != nil {
		return nil, err
	}

	envs := []corev1.EnvVar{
		{
			Name: constants.PodNameEnvName,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "metadata.name",
				},
			},
		},
		{
			Name: constants.PodNamespaceEnvName,
			ValueFrom: &corev1.EnvVarSource{
				FieldRef: &corev1.ObjectFieldSelector{
					APIVersion: "v1",
					FieldPath:  "metadata.namespace",
				},
			},
		},
		{
			Name:  constants.RunnerOrgEnvName,
			Value: r.organizationName,
		},
		{
			Name:  constants.RunnerRepoEnvName,
			Value: rp.Spec.RepositoryName,
		},
		{
			Name:  constants.RunnerPoolNameEnvName,
			Value: rp.ObjectMeta.Name,
		},
		{
			Name:  constants.RunnerOptionEnvName,
			Value: string(optionJson),
		},
	}

	// NOTE:
	// We need not ignore the reserved environment variables here.
	// Since the reserved environment variables are checked in the validating webhook.
	envs = append(envs, rp.Spec.Template.Env...)

	return envs, nil
}

func (r *RunnerPoolReconciler) makeRunnerContainerPorts() []corev1.ContainerPort {
	return []corev1.ContainerPort{
		{
			Protocol:      corev1.ProtocolTCP,
			Name:          constants.RunnerMetricsPortName,
			ContainerPort: constants.RunnerListenPort,
		},
	}
}
