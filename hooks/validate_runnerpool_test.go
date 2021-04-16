package hooks

import (
	actionsv1alpha1 "github.com/cybozu-go/github-actions-controller/api/v1alpha1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

var _ = Describe("validate RunnerPool webhook with ", func() {
	It("should deny runnerpool with invalid repository name", func() {
		name := "runnerpool-0"
		rp := actionsv1alpha1.RunnerPool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
			},
			Spec: actionsv1alpha1.RunnerPoolSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": name,
					},
				},
				RepositoryName: "invalid-repository",
				Template: actionsv1alpha1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "runner",
								Image: "sample:latest",
							},
						},
					},
				},
			},
		}
		err := k8sClient.Create(ctx, &rp)
		Expect(err).To(HaveOccurred())
	})

	It("should deny runnerpool with invalid container name", func() {
		name := "runnerpool-1"
		rp := actionsv1alpha1.RunnerPool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
			},
			Spec: actionsv1alpha1.RunnerPoolSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": name,
					},
				},
				RepositoryName: "test-repository2",
				Template: actionsv1alpha1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "sample",
								Image: "sample:latest",
							},
						},
					},
				},
			},
		}
		err := k8sClient.Create(ctx, &rp)
		Expect(err).To(HaveOccurred())
	})

	It("should deny runnerpool with reserved env name", func() {
		name := "runnerpool-2"
		rp := actionsv1alpha1.RunnerPool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
			},
			Spec: actionsv1alpha1.RunnerPoolSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": name,
					},
				},
				RepositoryName: "test-repository2",
				Template: actionsv1alpha1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "sample",
								Image: "sample:latest",
								Env: []corev1.EnvVar{
									{
										Name:  "POD_NAME",
										Value: "pod_name",
									},
								},
							},
						},
					},
				},
			},
		}
		err := k8sClient.Create(ctx, &rp)
		Expect(err).To(HaveOccurred())
	})

	It("should accept runnerpool", func() {
		name := "runnerpool-3"
		rp := actionsv1alpha1.RunnerPool{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: "default",
			},
			Spec: actionsv1alpha1.RunnerPoolSpec{
				Selector: &metav1.LabelSelector{
					MatchLabels: map[string]string{
						"app": name,
					},
				},
				RepositoryName: "test-repository2",
				Template: actionsv1alpha1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "runner",
								Image: "sample:latest",
							},
						},
					},
				},
			},
		}
		err := k8sClient.Create(ctx, &rp)
		Expect(err).NotTo(HaveOccurred())
	})
})
