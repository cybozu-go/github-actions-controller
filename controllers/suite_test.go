package controllers

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	constants "github.com/cybozu-go/meows"
	meowsv1alpha1 "github.com/cybozu-go/meows/api/v1alpha1"
	"github.com/cybozu-go/meows/metrics"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/envtest"
	logf "sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
	//+kubebuilder:scaffold:imports
)

// These tests use Ginkgo (BDD-style Go testing framework). Refer to
// http://onsi.github.io/ginkgo/ to learn more about Ginkgo.

var cfg *rest.Config
var k8sClient client.Client
var testEnv *envtest.Environment
var scheme = runtime.NewScheme()

func TestAPIs(t *testing.T) {
	RegisterFailHandler(Fail)

	SetDefaultEventuallyTimeout(10 * time.Second)
	SetDefaultEventuallyPollingInterval(time.Second)
	SetDefaultConsistentlyDuration(10 * time.Second)
	SetDefaultConsistentlyPollingInterval(time.Second)

	RunSpecs(t, "Controller Suite")
}

var _ = BeforeSuite(func() {
	logf.SetLogger(zap.New(zap.WriteTo(GinkgoWriter), zap.UseDevMode(true)))

	By("bootstrapping test environment")
	testEnv = &envtest.Environment{
		CRDDirectoryPaths:     []string{filepath.Join("..", "config", "crd", "bases")},
		ErrorIfCRDPathMissing: true,
	}

	var err error
	cfg, err = testEnv.Start()
	Expect(err).NotTo(HaveOccurred())
	Expect(cfg).NotTo(BeNil())

	err = clientgoscheme.AddToScheme(scheme)
	Expect(err).NotTo(HaveOccurred())
	err = meowsv1alpha1.AddToScheme(scheme)
	Expect(err).NotTo(HaveOccurred())

	//+kubebuilder:scaffold:scheme

	k8sClient, err = client.New(cfg, client.Options{Scheme: scheme})
	Expect(err).NotTo(HaveOccurred())
	Expect(k8sClient).NotTo(BeNil())

	registry := prometheus.DefaultRegisterer
	metrics.InitControllerMetrics(registry)
}, 60)

var _ = AfterSuite(func() {
	By("tearing down the test environment")
	err := testEnv.Stop()
	Expect(err).NotTo(HaveOccurred())
})

func createNamespaces(ctx context.Context, namespaces ...string) {
	for _, n := range namespaces {
		ns := &corev1.Namespace{}
		ns.Name = n
		err := k8sClient.Create(ctx, ns)
		ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
	}
}

func makeRunnerPool(name, namespace, repoName string) *meowsv1alpha1.RunnerPool {
	return &meowsv1alpha1.RunnerPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			// Add a finalizer manually, because a webhook is not working in this test.
			Finalizers: []string{constants.RunnerPoolFinalizer},
		},
		Spec: meowsv1alpha1.RunnerPoolSpec{
			RepositoryName: repoName,
		},
	}
}

func deleteRunnerPool(ctx context.Context, name, namespace string) {
	rp := &meowsv1alpha1.RunnerPool{}
	rp.Name = name
	rp.Namespace = namespace
	ExpectWithOffset(1, k8sClient.Delete(ctx, rp)).To(Succeed())
	EventuallyWithOffset(1, func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &meowsv1alpha1.RunnerPool{})
		return apierrors.IsNotFound(err)
	}).Should(BeTrue())

	d := &appsv1.Deployment{}
	d.Name = name
	d.Namespace = namespace
	k8sClient.Delete(ctx, d)
	EventuallyWithOffset(1, func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &appsv1.Deployment{})
		return apierrors.IsNotFound(err)
	}).Should(BeTrue())

	s := &corev1.Secret{}
	s.Name = rp.GetRunnerSecretName()
	s.Namespace = namespace
	k8sClient.Delete(ctx, s)
	EventuallyWithOffset(1, func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: rp.GetRunnerSecretName(), Namespace: namespace}, &corev1.Secret{})
		return apierrors.IsNotFound(err)
	}).Should(BeTrue())
}

func makePod(name, namespace, rpName string) *corev1.Pod {
	labels := map[string]string{
		constants.AppNameLabelKey:      constants.AppName,
		constants.AppComponentLabelKey: constants.AppComponentRunner,
		constants.AppInstanceLabelKey:  rpName,
	}

	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{
			Containers: []corev1.Container{
				{
					Name:  "sample",
					Image: "sample:latest",
				},
			},
		},
	}
}
