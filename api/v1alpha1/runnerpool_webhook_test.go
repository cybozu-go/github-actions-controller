package v1alpha1

import (
	"context"

	constants "github.com/cybozu-go/meows"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

func makeRunnerPoolTemplate(name, namespace, repoName string) *RunnerPool {
	return &RunnerPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
		},
		Spec: RunnerPoolSpec{
			RepositoryName: repoName,
		},
	}
}

func deleteRunnerPool(ctx context.Context, name, namespace string) {
	nsn := types.NamespacedName{
		Name:      name,
		Namespace: namespace,
	}
	rp := &RunnerPool{}
	err := k8sClient.Get(ctx, nsn, rp)
	if apierrors.IsNotFound(err) {
		return
	}
	Expect(err).NotTo(HaveOccurred())

	// Remove finalizer
	rp.Finalizers = nil
	Expect(k8sClient.Update(ctx, rp)).To(Succeed())

	Expect(k8sClient.Delete(ctx, rp)).To(Succeed())
	Eventually(func() bool {
		err := k8sClient.Get(ctx, nsn, &RunnerPool{})
		return apierrors.IsNotFound(err)
	}).Should(BeTrue())
}

var _ = Describe("validate RunnerPool webhook with ", func() {
	name := "runnerpool-test"
	namespace := "default"
	ctx := context.Background()

	It("should accept creating RunnerPool", func() {
		rp := makeRunnerPoolTemplate(name, namespace, "test-repo")
		rp.Spec.Template.Env = []corev1.EnvVar{
			{
				Name:  "GOOD_ENV",
				Value: "GOOD!",
			},
		}
		Expect(k8sClient.Create(ctx, rp)).To(Succeed())

		By("checking default values")
		Expect(rp.ObjectMeta.Finalizers).To(HaveLen(1))
		Expect(rp.ObjectMeta.Finalizers[0]).To(Equal(constants.RunnerPoolFinalizer))
		Expect(rp.Spec.Replicas).To(BeNumerically("==", 1))
		Expect(rp.Spec.Template.ServiceAccountName).To(Equal("default"))

		By("deleting the created RunnerPool")
		deleteRunnerPool(ctx, name, namespace)
	})

	It("should deny creating RunnerPool with empty repository name", func() {
		rp := makeRunnerPoolTemplate(name, namespace, "")
		Expect(k8sClient.Create(ctx, rp)).NotTo(Succeed())
	})

	It("should deny updating RunnerPool if repository name is changed", func() {
		rp := makeRunnerPoolTemplate(name, namespace, "test-repo")
		Expect(k8sClient.Create(ctx, rp)).To(Succeed())

		rp.Spec.RepositoryName = "test-repo2"
		Expect(k8sClient.Update(ctx, rp)).NotTo(Succeed())

		By("deleting the created RunnerPool")
		deleteRunnerPool(ctx, name, namespace)
	})

	It("should deny creating or updating RunnerPool with reserved environment variables", func() {
		testCases := []string{
			constants.PodNameEnvName,
			constants.PodNamespaceEnvName,
			constants.RunnerOrgEnvName,
			constants.RunnerRepoEnvName,
			constants.RunnerPoolNameEnvName,
			constants.RunnerOptionEnvName,
		}

		for _, envName := range testCases {
			By("creating runner pool with reserved environment variables; " + envName)
			rp := makeRunnerPoolTemplate(name, namespace, "test-repo")
			rp.Spec.Template.Env = []corev1.EnvVar{
				{
					Name:  envName,
					Value: "creating",
				},
			}
			Expect(k8sClient.Create(ctx, rp)).NotTo(Succeed())

			By("updating runner pool with reserved environment variables; " + envName)
			rp = makeRunnerPoolTemplate(name, namespace, "test-repo")
			Expect(k8sClient.Create(ctx, rp)).To(Succeed())
			rp.Spec.Template.Env = []corev1.EnvVar{
				{
					Name:  envName,
					Value: "updating",
				},
			}
			Expect(k8sClient.Update(ctx, rp)).NotTo(Succeed())

			By("deleting the created RunnerPool; " + envName)
			deleteRunnerPool(ctx, name, namespace)
		}
	})
})
