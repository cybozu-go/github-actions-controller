package controllers

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"time"

	constants "github.com/cybozu-go/meows"
	meowsv1alpha1 "github.com/cybozu-go/meows/api/v1alpha1"
	"github.com/cybozu-go/meows/github"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/pointer"
	ctrl "sigs.k8s.io/controller-runtime"
)

type runnerManagerMock struct {
	started map[string]bool
}

func newRunnerManagerMock() *runnerManagerMock {
	return &runnerManagerMock{
		started: map[string]bool{},
	}
}

func (m *runnerManagerMock) StartOrUpdate(rp *meowsv1alpha1.RunnerPool) {
	rpNamespacedName := rp.Namespace + "/" + rp.Name
	m.started[rpNamespacedName] = true
}

func (m *runnerManagerMock) Stop(_ context.Context, rp *meowsv1alpha1.RunnerPool) error {
	rpNamespacedName := rp.Namespace + "/" + rp.Name
	delete(m.started, rpNamespacedName)
	return nil
}

var _ = Describe("RunnerPool reconciler", func() {
	organizationName := "runnerpool-org"
	repositoryNames := []string{"runnerpool-repo-1", "runnerpool-repo-2"}
	namespace := "runnerpool-ns"
	runnerPoolName := "runnerpool-1"
	secretName := fmt.Sprintf("runner-token-%s", runnerPoolName)
	deploymentName := "runnerpool-1"
	defaultRunnerImage := "sample:latest"
	serviceAccountName := "customized-sa"
	wait := 10 * time.Second
	mockManager := newRunnerManagerMock()
	var githubFakeClient *github.FakeClient

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

		githubFakeClient = github.NewFakeClient(organizationName)
		r := NewRunnerPoolReconciler(
			mgr.GetClient(),
			ctrl.Log.WithName("controllers").WithName("RunnerPool"),
			mgr.GetScheme(),
			repositoryNames,
			organizationName,
			defaultRunnerImage,
			RunnerManager(mockManager),
		)

		secretWatcher := NewSecretWatcher(
			mgr.GetClient(),
			1*time.Second,
			githubFakeClient,
		)
		Expect(mgr.Add(secretWatcher)).To(Succeed())

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
		rp := makeRunnerPool(runnerPoolName, namespace, repositoryNames[0])
		Expect(k8sClient.Create(ctx, rp)).To(Succeed())

		By("wating the RunnerPool become Bound")
		Eventually(func() error {
			rp := new(meowsv1alpha1.RunnerPool)
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: runnerPoolName, Namespace: namespace}, rp); err != nil {
				return err
			}
			if !rp.Status.Bound {
				return errors.New(`status "bound" should be true`)
			}
			return nil
		}).Should(Succeed())
		time.Sleep(wait) // Wait for the reconciliation to run a few times. Please check the controller's log.

		By("getting the created Secret")
		s := new(corev1.Secret)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, s)).To(Succeed())

		By("confirming the Secret's expires in")
		expiresAt, err := time.Parse(time.RFC3339, s.Annotations[constants.RunnerSecretExpiresAtAnnotationKey])
		Expect(err).NotTo(HaveOccurred())
		Expect(expiresAt).Should(BeTemporally("~", time.Now().Add(1*time.Hour), 5*time.Minute))

		By("checking that a runner pool is the owner of a secret")
		Expect(s.OwnerReferences).To(HaveLen(1))
		Expect(s.OwnerReferences[0]).To(MatchFields(IgnoreExtras, Fields{
			"Kind": Equal("RunnerPool"),
			"Name": Equal(runnerPoolName),
		}))

		By("getting the created Deployment")
		d := new(appsv1.Deployment)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, d)).To(Succeed())

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
			"Volumes": MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": MatchFields(IgnoreExtras, Fields{
					"Name": Equal("var-dir"),
				}),
				"1": MatchFields(IgnoreExtras, Fields{
					"Name": Equal(secretName),
				}),
			}),
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
			"VolumeMounts": MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": MatchFields(IgnoreExtras, Fields{
					"Name":      Equal("var-dir"),
					"MountPath": Equal(constants.RunnerVarDirPath),
				}),
				"1": MatchFields(IgnoreExtras, Fields{
					"Name":      Equal(secretName),
					"MountPath": Equal(filepath.Join(constants.RunnerVarDirPath, "runnertoken")),
				}),
			}),
		}))

		By("checking that a runner pool is the owner of a deployment")
		Expect(d.OwnerReferences).To(HaveLen(1))
		Expect(d.OwnerReferences[0]).To(MatchFields(IgnoreExtras, Fields{
			"Kind": Equal("RunnerPool"),
			"Name": Equal(runnerPoolName),
		}))

		By("checking a manager is started")
		Expect(mockManager.started).To(HaveKey(namespace + "/" + runnerPoolName))

		By("deleting the created RunnerPool")
		deleteRunnerPool(ctx, runnerPoolName, namespace)

		By("checking a manager is stopped")
		Expect(mockManager.started).NotTo(HaveKey(namespace + "/" + runnerPoolName))
	})

	It("should create Deployment from maximum RunnerPool", func() {
		By("deploying RunnerPool resource")
		rp := makeRunnerPool(runnerPoolName, namespace, repositoryNames[1])
		rp.Spec.Replicas = 3
		rp.Spec.SetupCommand = []string{"command", "arg1", "args2"}
		rp.Spec.SlackAgent.ServiceName = "slack-agent"
		rp.Spec.SlackAgent.Channel = "#test"
		rp.Spec.Template.ObjectMeta.Labels = map[string]string{
			"test-label":                "test",
			constants.RunnerOrgLabelKey: "should-not-be-updated",
		}
		rp.Spec.Template.ObjectMeta.Annotations = map[string]string{
			"test-annotation": "test",
		}
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
			rp := new(meowsv1alpha1.RunnerPool)
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: runnerPoolName, Namespace: namespace}, rp); err != nil {
				return err
			}
			if !rp.Status.Bound {
				return errors.New(`status "bound" should be true`)
			}
			return nil
		}).Should(Succeed())
		time.Sleep(wait) // Wait for the reconciliation to run a few times. Please check the controller's log.

		By("getting the created Secret")
		s := new(corev1.Secret)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, s)).To(Succeed())

		By("confirming the Secret's expires in")
		expiresAt, err := time.Parse(time.RFC3339, s.Annotations[constants.RunnerSecretExpiresAtAnnotationKey])
		Expect(err).NotTo(HaveOccurred())
		Expect(expiresAt).Should(BeTemporally("~", time.Now().Add(1*time.Hour), 5*time.Minute))

		By("checking that a runner pool is the owner of a secret")
		Expect(s.OwnerReferences).To(HaveLen(1))
		Expect(s.OwnerReferences[0]).To(MatchFields(IgnoreExtras, Fields{
			"Kind": Equal("RunnerPool"),
			"Name": Equal(runnerPoolName),
		}))

		By("getting the created Deployment")
		d := new(appsv1.Deployment)
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, d)).To(Succeed())

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
				"2": MatchFields(IgnoreExtras, Fields{
					"Name": Equal("var-dir"),
				}),
				"3": MatchFields(IgnoreExtras, Fields{
					"Name": Equal(secretName),
				}),
			}),
		}))

		// runner container spec
		Expect(d.Spec.Template).To(MatchFields(IgnoreExtras, Fields{
			"ObjectMeta": MatchFields(IgnoreExtras, Fields{
				"Labels": MatchAllKeys(Keys{
					constants.AppNameLabelKey:      Equal(constants.AppName),
					constants.AppComponentLabelKey: Equal(constants.AppComponentRunner),
					constants.AppInstanceLabelKey:  Equal(rp.Name),
					constants.RunnerOrgLabelKey:    Equal(organizationName),
					constants.RunnerRepoLabelKey:   Equal(rp.Spec.RepositoryName),
					"test-label":                   Equal("test"),
				}),
				"Annotations": MatchAllKeys(Keys{
					"test-annotation": Equal("test"),
				}),
			}),
		}))
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
				"2": MatchFields(IgnoreExtras, Fields{
					"Name":      Equal("var-dir"),
					"MountPath": Equal(constants.RunnerVarDirPath),
				}),
				"3": MatchFields(IgnoreExtras, Fields{
					"Name":      Equal(secretName),
					"MountPath": Equal(filepath.Join(constants.RunnerVarDirPath, "runnertoken")),
				}),
			}),
		}))

		By("checking that a runner pool is the owner of a deployment")
		Expect(d.OwnerReferences).To(HaveLen(1))
		Expect(d.OwnerReferences[0]).To(MatchFields(IgnoreExtras, Fields{
			"Kind": Equal("RunnerPool"),
			"Name": Equal(runnerPoolName),
		}))

		By("checking a manager is started")
		Expect(mockManager.started).To(HaveKey(namespace + "/" + runnerPoolName))

		By("deleting the created RunnerPool")
		deleteRunnerPool(ctx, runnerPoolName, namespace)

		By("checking a manager is stopped")
		Expect(mockManager.started).NotTo(HaveKey(namespace + "/" + runnerPoolName))
	})

	It("should not create Deployment with an invalid repository name", func() {
		By("deploying RunnerPool resource")
		rp := makeRunnerPool(runnerPoolName, namespace, "bad-runnerpool-repo")
		Expect(k8sClient.Create(ctx, rp)).To(Succeed())

		By("confirming the Deployment is not created")
		Consistently(func() error {
			return k8sClient.Get(ctx, types.NamespacedName{Name: deploymentName, Namespace: namespace}, &appsv1.Deployment{})
		}).ShouldNot(Succeed())

		By("checking a manager is not started")
		Expect(mockManager.started).NotTo(HaveKey(namespace + "/" + runnerPoolName))

		By("deleting the created RunnerPool")
		deleteRunnerPool(ctx, runnerPoolName, namespace)
	})

	It("should or not update secret by a SecretWatcher", func() {
		By("deploying RunnerPool resource")
		rp := makeRunnerPool(runnerPoolName, namespace, repositoryNames[0])
		Expect(k8sClient.Create(ctx, rp)).To(Succeed())

		By("wating the RunnerPool become Bound")
		Eventually(func() error {
			rp := new(meowsv1alpha1.RunnerPool)
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: runnerPoolName, Namespace: namespace}, rp); err != nil {
				return err
			}
			if !rp.Status.Bound {
				return errors.New(`status "bound" should be true`)
			}
			return nil
		}).Should(Succeed())
		time.Sleep(wait) // Wait for the reconciliation to run a few times. Please check the controller's log.

		testCase := []struct {
			expiresAtDuration time.Duration
			shouldUpdate      bool
		}{
			{
				-10 * time.Minute,
				true,
			},
			{
				0 * time.Minute,
				true,
			},
			{
				5 * time.Minute,
				true,
			},
			{
				20 * time.Minute,
				false,
			},
			{
				1 * time.Hour,
				false,
			},
		}
		for _, tc := range testCase {
			By("getting the created Secret")
			fmt.Printf("testcase is {expiresAtDuration: %s, should update: %v}\n", tc.expiresAtDuration.String(), tc.shouldUpdate)
			s := new(corev1.Secret)
			Expect(k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, s)).To(Succeed())

			By("set annotation")
			baseTime := time.Now().Add(tc.expiresAtDuration).Format(time.RFC3339)
			s.Annotations[constants.RunnerSecretExpiresAtAnnotationKey] = baseTime
			Expect(k8sClient.Update(ctx, s)).To(Succeed())
			time.Sleep(wait)

			if tc.shouldUpdate {
				By("checking to update secret")
				s = new(corev1.Secret)
				err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, s)
				Expect(err).ToNot(HaveOccurred())
				tmStr := s.Annotations[constants.RunnerSecretExpiresAtAnnotationKey]
				tm, err := time.Parse(time.RFC3339, tmStr)
				Expect(err).ToNot(HaveOccurred())

				Expect(tmStr).ShouldNot(Equal(baseTime))
				Expect(tm).Should(BeTemporally("~", time.Now().Add(1*time.Hour), 20*time.Second))
				continue
			}

			By("checking to not update secret")
			s = new(corev1.Secret)
			err := k8sClient.Get(ctx, types.NamespacedName{Name: secretName, Namespace: namespace}, s)
			Expect(err).ToNot(HaveOccurred())
			tm := s.Annotations[constants.RunnerSecretExpiresAtAnnotationKey]
			Expect(err).ToNot(HaveOccurred())
			Expect(tm).Should(Equal(baseTime))
		}

		By("deleting the created RunnerPool")
		deleteRunnerPool(ctx, runnerPoolName, namespace)
	})
})
