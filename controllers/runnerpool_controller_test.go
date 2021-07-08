package controllers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	constants "github.com/cybozu-go/github-actions-controller"
	actionsv1alpha1 "github.com/cybozu-go/github-actions-controller/api/v1alpha1"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("RunnerPool reconciler", func() {
	organizationName := "runnerpool-org"
	repositoryNames := []string{"runnerpool-repo-1", "runnerpool-repo-2"}
	namespace := "runnerpool-ns"
	runnerPoolName := "runnerpool-1"
	deploymentName := "runnerpool-1"
	defaultRunnerImage := "sample:latest"
	serviceAccountName := "customized-sa"
	wait := 10 * time.Second

	ctx := context.Background()
	var mgrCtx context.Context
	var mgrCancel context.CancelFunc

	BeforeEach(func() {
		mgr, err := ctrl.NewManager(cfg, ctrl.Options{
			Scheme:             scheme,
			LeaderElection:     false,
			MetricsBindAddress: "0",
		})
		Expect(err).ToNot(HaveOccurred())

		r := NewRunnerPoolReconciler(
			mgr.GetClient(),
			ctrl.Log.WithName("controllers").WithName("RunnerPool"),
			mgr.GetScheme(),
			repositoryNames,
			organizationName,
			defaultRunnerImage,
		)
		Expect(r.SetupWithManager(mgr)).To(Succeed())

		mgrCtx, mgrCancel = context.WithCancel(context.Background())
		go func() {
			err := mgr.Start(mgrCtx)
			if err != nil {
				panic(err)
			}
		}()
		time.Sleep(time.Second)
	})

	AfterEach(func() {
		mgrCancel()
		time.Sleep(500 * time.Millisecond)
	})

	It("should create Namespace", func() {
		createNamespaces(ctx, namespace)
	})

	It("should create Deployment from minimal RunnerPool", func() {
		By("deploying RunnerPool resource")
		rp := makeRunnerPoolTemplate(runnerPoolName, namespace, repositoryNames[0])
		Expect(k8sClient.Create(ctx, rp)).To(Succeed())

		By("wating the RunnerPool become Bound")
		Eventually(func() error {
			rp := new(actionsv1alpha1.RunnerPool)
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: runnerPoolName, Namespace: namespace}, rp); err != nil {
				return err
			}
			if !rp.Status.Bound {
				return errors.New(`status "bound" should be true`)
			}
			return nil
		}).Should(Succeed())
		time.Sleep(wait) // Wait for the reconciliation to run a few times. Please check the controller's log.

		By("getting the created Deployment")
		d := new(appsv1.Deployment)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, d)).To(Succeed())

		// DEBUG
		str, err := json.MarshalIndent(d, "[DEBUG]", "    ")
		Expect(err).ToNot(HaveOccurred())
		fmt.Println(string(str))

		By("confirming the Deployment's manifests")
		// labels
		Expect(d.Labels).To(MatchAllKeys(Keys{
			constants.AppNameLabelKey:      Equal(constants.AppName),
			constants.AppComponentLabelKey: Equal(constants.AppComponentRunner),
			constants.AppInstanceLabelKey:  Equal(runnerPoolName),
		}))
		Expect(d.Spec.Selector.MatchLabels).To(MatchAllKeys(Keys{
			constants.AppNameLabelKey:      Equal(constants.AppName),
			constants.AppComponentLabelKey: Equal(constants.AppComponentRunner),
			constants.AppInstanceLabelKey:  Equal(runnerPoolName),
		}))
		Expect(d.Spec.Template.Labels).To(MatchAllKeys(Keys{
			constants.AppNameLabelKey:      Equal(constants.AppName),
			constants.AppComponentLabelKey: Equal(constants.AppComponentRunner),
			constants.AppInstanceLabelKey:  Equal(runnerPoolName),
			constants.RunnerOrgLabelKey:    Equal(organizationName),
			constants.RunnerRepoLabelKey:   Equal(repositoryNames[0]),
		}))

		// deployment/pod spec
		Expect(d.Spec.Replicas).To(PointTo(BeNumerically("==", 1)))
		Expect(d.Spec.Template.Spec).To(MatchFields(IgnoreExtras, Fields{
			"ServiceAccountName": Equal("default"),
			"ImagePullSecrets":   BeEmpty(),
			"Volumes":            BeEmpty(),
		}))

		// runner container spec
		Expect(d.Spec.Template.Spec.Containers).To(HaveLen(1))
		Expect(d.Spec.Template.Spec.Containers[0]).To(MatchFields(IgnoreExtras, Fields{
			"Name":            Equal(constants.RunnerContainerName),
			"Image":           Equal(defaultRunnerImage),
			"ImagePullPolicy": Equal(corev1.PullAlways),
			"SecurityContext": BeNil(),
			"Env": MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": MatchFields(IgnoreExtras, Fields{
					"Name": Equal(constants.PodNameEnvName),
					"ValueFrom": PointTo(MatchFields(IgnoreExtras, Fields{
						"FieldRef": PointTo(MatchFields(IgnoreExtras, Fields{
							"FieldPath": Equal("metadata.name"),
						})),
					})),
				}),
				"1": MatchFields(IgnoreExtras, Fields{
					"Name": Equal(constants.PodNamespaceEnvName),
					"ValueFrom": PointTo(MatchFields(IgnoreExtras, Fields{
						"FieldRef": PointTo(MatchFields(IgnoreExtras, Fields{
							"FieldPath": Equal("metadata.namespace"),
						})),
					})),
				}),
				"2": MatchFields(IgnoreExtras, Fields{
					"Name":  Equal(constants.RunnerOrgEnvName),
					"Value": Equal(organizationName),
				}),
				"3": MatchFields(IgnoreExtras, Fields{
					"Name":  Equal(constants.RunnerRepoEnvName),
					"Value": Equal(repositoryNames[0]),
				}),
				"4": MatchFields(IgnoreExtras, Fields{
					"Name":  Equal(constants.RunnerPoolNameEnvName),
					"Value": Equal(runnerPoolName),
				}),
				"5": MatchFields(IgnoreExtras, Fields{
					"Name":  Equal(constants.RunnerOptionEnvName),
					"Value": Equal("{}"),
				}),
			}),
			"Resources": MatchAllFields(Fields{
				"Limits":   BeEmpty(),
				"Requests": BeEmpty(),
			}),
			"Ports": MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": MatchFields(IgnoreExtras, Fields{
					"Protocol":      Equal(corev1.ProtocolTCP),
					"Name":          Equal(constants.RunnerMetricsPortName),
					"ContainerPort": BeNumerically("==", constants.RunnerListenPort),
				}),
			}),
			"VolumeMounts": BeEmpty(),
		}))

		By("deleting the created RunnerPool")
		deleteRunnerPool(ctx, runnerPoolName, namespace)

		By("wating the Deployment is deleted")
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, &appsv1.Deployment{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())
	})

	It("should create Deployment from maximum RunnerPool", func() {
		By("deploying RunnerPool resource")
		rp := makeRunnerPoolTemplate(runnerPoolName, namespace, repositoryNames[1])
		rp.Spec.Replicas = 3
		rp.Spec.SetupCommand = []string{"command", "arg1", "args2"}
		rp.Spec.SlackAgent.ServiceName = "slack-agent"
		rp.Spec.SlackAgent.Channel = "#test"
		rp.Spec.Template.Image = "sample:devel"
		rp.Spec.Template.ImagePullPolicy = corev1.PullIfNotPresent
		rp.Spec.Template.ImagePullSecrets = []corev1.LocalObjectReference{
			{Name: "image-pull-secret1"},
		}
		rp.Spec.Template.SecurityContext = &corev1.SecurityContext{
			Privileged: pointer.BoolPtr(true),
		}
		rp.Spec.Template.Env = []corev1.EnvVar{
			{Name: "ENV_VAR", Value: "value"},
		}
		rp.Spec.Template.Resources = corev1.ResourceRequirements{
			Requests: corev1.ResourceList{
				"memory": resource.MustParse("100"),
			},
		}
		rp.Spec.Template.VolumeMounts = []corev1.VolumeMount{
			{Name: "volume1", MountPath: "/volume1"},
			{Name: "volume2", MountPath: "/volume2"},
		}
		rp.Spec.Template.Volumes = []corev1.Volume{
			{Name: "volume1", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
			{Name: "volume2", VolumeSource: corev1.VolumeSource{EmptyDir: &corev1.EmptyDirVolumeSource{}}},
		}
		rp.Spec.Template.ServiceAccountName = serviceAccountName
		Expect(k8sClient.Create(ctx, rp)).To(Succeed())

		By("wating the RunnerPool become Bound")
		Eventually(func() error {
			rp := new(actionsv1alpha1.RunnerPool)
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: runnerPoolName, Namespace: namespace}, rp); err != nil {
				return err
			}
			if !rp.Status.Bound {
				return errors.New(`status "bound" should be true`)
			}
			return nil
		}).Should(Succeed())
		time.Sleep(wait) // Wait for the reconciliation to run a few times. Please check the controller's log.

		By("getting the created Deployment")
		d := new(appsv1.Deployment)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, d)).To(Succeed())

		// DEBUG
		str, err := json.MarshalIndent(d, "[DEBUG]", "    ")
		Expect(err).ToNot(HaveOccurred())
		fmt.Println(string(str))

		By("confirming the Deployment's manifests")
		// labels (omit)

		// deployment/pod spec
		Expect(d.Spec.Replicas).To(PointTo(BeNumerically("==", 3)))
		Expect(d.Spec.Template.Spec).To(MatchFields(IgnoreExtras, Fields{
			"ServiceAccountName": Equal(serviceAccountName),
			"ImagePullSecrets": MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": MatchFields(IgnoreExtras, Fields{
					"Name": Equal("image-pull-secret1"),
				}),
			}),
			"Volumes": MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": MatchFields(IgnoreExtras, Fields{
					"Name": Equal("volume1"),
				}),
				"1": MatchFields(IgnoreExtras, Fields{
					"Name": Equal("volume2"),
				}),
			}),
		}))

		// runner container spec
		Expect(d.Spec.Template.Spec.Containers).To(HaveLen(1))
		Expect(d.Spec.Template.Spec.Containers[0]).To(MatchFields(IgnoreExtras, Fields{
			"Name":            Equal(constants.RunnerContainerName),
			"Image":           Equal("sample:devel"),
			"ImagePullPolicy": Equal(corev1.PullIfNotPresent),
			"SecurityContext": PointTo(MatchFields(IgnoreExtras, Fields{
				"Privileged": PointTo(BeTrue()),
			})),
			"Env": MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": MatchFields(IgnoreExtras, Fields{
					"Name": Equal(constants.PodNameEnvName),
					"ValueFrom": PointTo(MatchFields(IgnoreExtras, Fields{
						"FieldRef": PointTo(MatchFields(IgnoreExtras, Fields{
							"FieldPath": Equal("metadata.name"),
						})),
					})),
				}),
				"1": MatchFields(IgnoreExtras, Fields{
					"Name": Equal(constants.PodNamespaceEnvName),
					"ValueFrom": PointTo(MatchFields(IgnoreExtras, Fields{
						"FieldRef": PointTo(MatchFields(IgnoreExtras, Fields{
							"FieldPath": Equal("metadata.namespace"),
						})),
					})),
				}),
				"2": MatchFields(IgnoreExtras, Fields{
					"Name":  Equal(constants.RunnerOrgEnvName),
					"Value": Equal(organizationName),
				}),
				"3": MatchFields(IgnoreExtras, Fields{
					"Name":  Equal(constants.RunnerRepoEnvName),
					"Value": Equal(repositoryNames[1]),
				}),
				"4": MatchFields(IgnoreExtras, Fields{
					"Name":  Equal(constants.RunnerPoolNameEnvName),
					"Value": Equal(runnerPoolName),
				}),
				"5": MatchFields(IgnoreExtras, Fields{
					"Name":  Equal(constants.RunnerOptionEnvName),
					"Value": Equal("{\"setup_command\":[\"command\",\"arg1\",\"args2\"],\"slack_agent_service_name\":\"slack-agent\",\"slack_channel\":\"#test\"}"),
				}),
				"6": MatchFields(IgnoreExtras, Fields{
					"Name":  Equal("ENV_VAR"),
					"Value": Equal("value"),
				}),
			}),
			"Resources": MatchAllFields(Fields{
				"Limits": BeEmpty(),
				"Requests": MatchAllKeys(Keys{
					corev1.ResourceMemory: Equal(resource.MustParse("100")),
				}),
			}),
			"Ports": MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": MatchFields(IgnoreExtras, Fields{
					"Protocol":      Equal(corev1.ProtocolTCP),
					"Name":          Equal(constants.RunnerMetricsPortName),
					"ContainerPort": BeNumerically("==", constants.RunnerListenPort),
				}),
			}),
			"VolumeMounts": MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": MatchFields(IgnoreExtras, Fields{
					"Name":      Equal("volume1"),
					"MountPath": Equal("/volume1"),
				}),
				"1": MatchFields(IgnoreExtras, Fields{
					"Name":      Equal("volume2"),
					"MountPath": Equal("/volume2"),
				}),
			}),
		}))

		By("deleting the created RunnerPool")
		deleteRunnerPool(ctx, runnerPoolName, namespace)

		By("wating the Deployment is deleted")
		Eventually(func() bool {
			err := k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, &appsv1.Deployment{})
			return apierrors.IsNotFound(err)
		}).Should(BeTrue())
	})

	It("should not create Deployment with an invalid repository name", func() {
		By("deploying RunnerPool resource")
		rp := makeRunnerPoolTemplate(runnerPoolName, namespace, "bad-runnerpool-repo")
		Expect(k8sClient.Create(ctx, rp)).To(Succeed())

		By("confirming the Deployment is not created")
		Consistently(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, &appsv1.Deployment{})
		}).ShouldNot(Succeed())

		By("deleting the created RunnerPool")
		deleteRunnerPool(ctx, runnerPoolName, namespace)
	})
})

func makeRunnerPoolTemplate(name, namespace, repoName string) *actionsv1alpha1.RunnerPool {
	return &actionsv1alpha1.RunnerPool{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: namespace,
			// Add a finalizer manually, because a webhook is not working in this test.
			Finalizers: []string{constants.RunnerPoolFinalizer},
		},
		Spec: actionsv1alpha1.RunnerPoolSpec{
			RepositoryName: repoName,
		},
	}
}

func deleteRunnerPool(ctx context.Context, name, namespace string) {
	rp := &actionsv1alpha1.RunnerPool{}
	rp.Name = name
	rp.Namespace = namespace
	ExpectWithOffset(1, k8sClient.Delete(ctx, rp)).To(Succeed())
	EventuallyWithOffset(1, func() bool {
		err := k8sClient.Get(ctx, types.NamespacedName{Name: name, Namespace: namespace}, &actionsv1alpha1.RunnerPool{})
		return apierrors.IsNotFound(err)
	}).Should(BeTrue())
}
