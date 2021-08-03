package controllers

import (
	"context"
	"net/http"
	"sort"
	"time"

	meowsv1alpha1 "github.com/cybozu-go/meows/api/v1alpha1"
	"github.com/cybozu-go/meows/github"
	"github.com/cybozu-go/meows/metrics"
	rc "github.com/cybozu-go/meows/runner/client"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	gomegatypes "github.com/onsi/gomega/types"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
)

var _ = Describe("RunnerManager", func() {
	ctx := context.Background()
	metricsPort := ":12345"
	metricsURL := "http://localhost" + metricsPort

	It("should create namespace", func() {
		createNamespaces(ctx, "test-ns1", "test-ns2")
	})

	It("should manage pods and runners", func() {
		type inputPod struct {
			spec         *corev1.Pod
			ip           string
			deletionTime time.Time
		}
		testCases := []struct {
			name                string
			inputRunnerPools    []*meowsv1alpha1.RunnerPool
			inputPods           []*inputPod
			inputRunners        map[string][]*github.Runner // key: "<Repository name>"
			expectedPodNames    []string                    // slice of "<Namespace>/<Pod name>"
			expectedRunnerNames []string                    // slice of "<Repository name>/<Runner name>"
		}{
			{
				name: "delete pods",
				inputRunnerPools: []*meowsv1alpha1.RunnerPool{
					makeRunnerPool("rp1", "test-ns1", "repo1"),
					makeRunnerPool("rp2", "test-ns1", "repo2"),
				},
				inputPods: []*inputPod{
					{spec: makePod("pod1", "test-ns1", "rp1"), ip: "10.0.0.1", deletionTime: time.Now().UTC()},
					{spec: makePod("pod2", "test-ns1", "rp1"), ip: "10.0.0.2", deletionTime: time.Now().UTC()},
					{spec: makePod("pod3", "test-ns1", "rp2"), ip: "10.0.0.3", deletionTime: time.Now().UTC()},
				},
				expectedPodNames: nil,
			},
			{
				name: "should not delete pods",
				inputRunnerPools: []*meowsv1alpha1.RunnerPool{
					makeRunnerPool("rp1", "test-ns1", "repo1"),
					makeRunnerPool("rp2", "test-ns1", "repo2"),
				},
				inputPods: []*inputPod{
					{spec: makePod("pod1", "test-ns1", "rp1"), ip: "10.0.0.1"},
					{spec: makePod("pod2", "test-ns1", "rp2"), ip: "10.0.0.2", deletionTime: time.Now().Add(24 * time.Hour).UTC()},
					{spec: makePod("pod3", "test-ns1", "rp3"), ip: "10.0.0.3", deletionTime: time.Now().UTC()}, // RunnerPool (test-ns1/rp3) is not exists.
					{spec: makePod("pod1", "test-ns2", "rp1"), ip: "10.0.1.1", deletionTime: time.Now().UTC()}, // RunnerPool (test-ns2/rp1) is not exists.
				},
				expectedPodNames: []string{
					"test-ns1/pod1",
					"test-ns1/pod2",
					"test-ns1/pod3",
					"test-ns2/pod1",
				},
			},
			{
				name: "delete runners",
				inputRunnerPools: []*meowsv1alpha1.RunnerPool{
					makeRunnerPool("rp1", "test-ns1", "repo1"),
					makeRunnerPool("rp2", "test-ns1", "repo2"),
				},
				inputRunners: map[string][]*github.Runner{
					"repo1": {
						{Name: "pod1", ID: 1, Online: false, Busy: false, Labels: []string{"test-ns1/rp1"}}, // pod does not exist, offline
						{Name: "pod2", ID: 2, Online: false, Busy: false, Labels: []string{"test-ns1/rp1"}}, // pod does not exist, offline
					},
					"repo2": {
						{Name: "pod3", ID: 3, Online: false, Busy: false, Labels: []string{"test-ns1/rp2"}}, // pod does not exist, offline
					},
				},
				expectedRunnerNames: nil,
			},
			{
				name: "should not delete runners",
				inputRunnerPools: []*meowsv1alpha1.RunnerPool{
					makeRunnerPool("rp1", "test-ns1", "repo1"),
					makeRunnerPool("rp2", "test-ns1", "repo2"),
				},
				inputPods: []*inputPod{
					{spec: makePod("pod1", "test-ns1", "rp1"), ip: "10.0.0.1"},
					{spec: makePod("pod2", "test-ns1", "rp1"), ip: "10.0.0.2"},
				},
				inputRunners: map[string][]*github.Runner{
					"repo1": {
						{Name: "pod1", ID: 1, Online: false, Busy: false, Labels: []string{"test-ns1/rp1"}}, // pod exists
						{Name: "pod2", ID: 2, Online: true, Busy: true, Labels: []string{"test-ns1/rp1"}},   // pod exists
						{Name: "pod3", ID: 3, Online: true, Busy: false, Labels: []string{"test-ns1/rp1"}},  // pod does not exist, but online
					},
					"repo2": {
						{Name: "pod1", ID: 4, Online: false, Busy: false, Labels: []string{"test-ns1/rp1"}},
						{Name: "pod2", ID: 5, Online: false, Busy: false, Labels: []string{"test-ns1/rp3"}},
						{Name: "pod3", ID: 6, Online: false, Busy: false, Labels: []string{}},
					},
				},
				expectedPodNames: []string{
					"test-ns1/pod1",
					"test-ns1/pod2",
				},
				expectedRunnerNames: []string{
					"repo1/pod1",
					"repo1/pod2",
					"repo1/pod3",
					"repo2/pod1",
					"repo2/pod2",
					"repo2/pod3",
				},
			},
		}

		for _, tt := range testCases {
			By("preparing fake clients; " + tt.name)
			runnerPodClient := rc.NewFakeClient()
			githubClient := github.NewFakeClient("runnermanager-org")
			runnerManager := NewRunnerManager(ctrl.Log, time.Second, k8sClient, githubClient, runnerPodClient)

			By("preparing pods and runners")
			for _, inputPod := range tt.inputPods {
				Expect(k8sClient.Create(ctx, inputPod.spec)).To(Succeed())
				created := &corev1.Pod{}
				Expect(k8sClient.Get(ctx, types.NamespacedName{Name: inputPod.spec.Name, Namespace: inputPod.spec.Namespace}, created)).To(Succeed())
				created.Status.PodIP = inputPod.ip
				Expect(k8sClient.Status().Update(ctx, created)).To(Succeed())
				runnerPodClient.SetDeletionTimes(created.Status.PodIP, inputPod.deletionTime)
			}
			githubClient.SetRunners(tt.inputRunners)

			By("starting runnerpool manager")
			for _, rp := range tt.inputRunnerPools {
				runnerManager.StartOrUpdate(rp)
			}
			time.Sleep(3 * time.Second)

			By("checking pods")
			var actualPodNames []string
			podList := new(corev1.PodList)
			Expect(k8sClient.List(ctx, podList)).To(Succeed())
			for i := range podList.Items {
				po := &podList.Items[i]
				actualPodNames = append(actualPodNames, po.Namespace+"/"+po.Name)
			}
			sort.Strings(actualPodNames)
			sort.Strings(tt.expectedPodNames)
			Expect(actualPodNames).To(Equal(tt.expectedPodNames))

			By("checking runners")
			var actualRunnerNames []string
			for repo := range tt.inputRunners {
				runnerList, _ := githubClient.ListRunners(ctx, repo) // github.FakeClient does not return an error.
				for _, runner := range runnerList {
					actualRunnerNames = append(actualRunnerNames, repo+"/"+runner.Name)
				}
			}
			sort.Strings(actualRunnerNames)
			sort.Strings(tt.expectedRunnerNames)
			Expect(actualRunnerNames).To(Equal(tt.expectedRunnerNames))

			By("tearing down; " + tt.name)
			for _, rp := range tt.inputRunnerPools {
				runnerManager.Stop(rp.Namespace + "/" + rp.Name)
			}
			for _, inputPod := range tt.inputPods {
				k8sClient.Delete(ctx, inputPod.spec)
			}
			time.Sleep(500 * time.Millisecond)
		}
	})

	It("should expose metrics about runnerpools", func() {
		By("preparing fake clients")
		runnerPodClient := rc.NewFakeClient()
		githubClient := github.NewFakeClient("runnermanager-org")
		runnerManager := NewRunnerManager(ctrl.Log, time.Second, k8sClient, githubClient, runnerPodClient)

		By("starting metrics server")
		server := &http.Server{Addr: metricsPort, Handler: promhttp.Handler()}
		go func() {
			server.ListenAndServe()
		}()
		defer server.Shutdown(context.Background())
		time.Sleep(1 * time.Second)

		By("checking metrics are not exposed")
		MetricsShouldNotExist(metricsURL, "meows_runnerpool_replicas")
		MetricsShouldNotExist(metricsURL, "meows_runner_online")
		MetricsShouldNotExist(metricsURL, "meows_runner_busy")

		By("creating rp1")
		rp1 := makeRunnerPool("rp1", "test-ns1", "repo1")
		rp1.Spec.Replicas = 1
		runnerManager.StartOrUpdate(rp1)
		time.Sleep(2 * time.Second)
		MetricsShouldHaveValue(metricsURL, "meows_runnerpool_replicas",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1")}),
					"Value": BeNumerically("==", 1.0),
				})),
			}),
		)

		By("updating rp1")
		rp1.Spec.Replicas = 2
		runnerManager.StartOrUpdate(rp1)
		time.Sleep(2 * time.Second)
		MetricsShouldHaveValue(metricsURL, "meows_runnerpool_replicas",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1")}),
					"Value": BeNumerically("==", 2.0),
				})),
			}),
		)

		By("creating rp2")
		rp2 := makeRunnerPool("rp2", "test-ns2", "repo1")
		rp2.Spec.Replicas = 1
		runnerManager.StartOrUpdate(rp2)
		time.Sleep(2 * time.Second)
		MetricsShouldHaveValue(metricsURL, "meows_runnerpool_replicas",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1")}),
					"Value": BeNumerically("==", 2.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns2/rp2")}),
					"Value": BeNumerically("==", 1.0),
				})),
			}),
		)

		By("deleting rp1")
		runnerManager.Stop("test-ns1/rp1")
		time.Sleep(2 * time.Second)
		MetricsShouldHaveValue(metricsURL, "meows_runnerpool_replicas",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns2/rp2")}),
					"Value": BeNumerically("==", 1.0),
				})),
			}),
		)

		By("deleting rp2")
		runnerManager.Stop("test-ns2/rp2")
		time.Sleep(2 * time.Second)
		MetricsShouldNotExist(metricsURL, "meows_runnerpool_replicas")
	})

	It("should expose metrics about runners (single runnerpool)", func() {
		By("preparing fake clients")
		runnerPodClient := rc.NewFakeClient()
		githubClient := github.NewFakeClient("runnermanager-org")
		runnerManager := NewRunnerManager(ctrl.Log, time.Second, k8sClient, githubClient, runnerPodClient)

		By("starting metrics server")
		server := &http.Server{Addr: metricsPort, Handler: promhttp.Handler()}
		go func() {
			server.ListenAndServe()
		}()
		defer server.Shutdown(context.Background())
		time.Sleep(1 * time.Second)

		By("creating a runnerpool")
		runnerManager.StartOrUpdate(makeRunnerPool("rp1", "test-ns1", "repo1"))
		time.Sleep(2 * time.Second)
		MetricsShouldHaveValue(metricsURL, "meows_runnerpool_replicas",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchFields(IgnoreExtras, Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1")}),
				})),
			}),
		)
		MetricsShouldNotExist(metricsURL, "meows_runner_online")
		MetricsShouldNotExist(metricsURL, "meows_runner_busy")

		By("creating runner pods")
		dummyPods := []*corev1.Pod{
			makePod("pod1", "test-ns1", "rp1"),
			makePod("pod2", "test-ns1", "rp1"),
		}
		for _, po := range dummyPods {
			Expect(k8sClient.Create(ctx, po)).To(Succeed())
		}

		By("creating runners")
		runenrs := map[string][]*github.Runner{
			"repo1": {
				{Name: "pod1", ID: 1, Online: true, Busy: true, Labels: []string{"test-ns1/rp1"}},
				{Name: "pod2", ID: 2, Online: true, Busy: true, Labels: []string{"test-ns1/rp1"}},
				{Name: "pod3", ID: 3, Online: true, Busy: false, Labels: []string{"test-ns1/rp1"}},
			},
		}
		githubClient.SetRunners(runenrs)
		time.Sleep(3 * time.Second)
		MetricsShouldHaveValue(metricsURL, "meows_runner_online",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod1")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod2")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"2": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod3")}),
					"Value": BeNumerically("==", 1.0),
				})),
			}),
		)
		MetricsShouldHaveValue(metricsURL, "meows_runner_busy",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod1")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod2")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"2": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod3")}),
					"Value": BeNumerically("==", 0.0),
				})),
			}),
		)

		By("updating runners")
		runenrs = map[string][]*github.Runner{
			"repo1": {
				{Name: "pod1", ID: 1, Online: true, Busy: false, Labels: []string{"test-ns1/rp1"}},
				{Name: "pod2", ID: 2, Online: false, Busy: false, Labels: []string{"test-ns1/rp1"}},
				{Name: "pod3", ID: 3, Online: false, Busy: false, Labels: []string{"test-ns1/rp1"}}, // metrics should not exist. "Offline" AND "Runner pod is not exist".
			},
		}
		githubClient.SetRunners(runenrs)
		time.Sleep(3 * time.Second)
		MetricsShouldHaveValue(metricsURL, "meows_runner_online",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod1")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod2")}),
					"Value": BeNumerically("==", 0.0),
				})),
			}),
		)
		MetricsShouldHaveValue(metricsURL, "meows_runner_busy",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod1")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod2")}),
					"Value": BeNumerically("==", 0.0),
				})),
			}),
		)

		By("deleting runnerpool")
		runnerManager.Stop("test-ns1/rp1")
		time.Sleep(2 * time.Second)
		MetricsShouldNotExist(metricsURL, "meows_runnerpool_replicas")
		MetricsShouldNotExist(metricsURL, "meows_runner_online")
		MetricsShouldNotExist(metricsURL, "meows_runner_busy")

		By("tearing down")
		for _, po := range dummyPods {
			Expect(k8sClient.Delete(ctx, po)).To(Succeed())
		}
	})

	It("should expose metrics about runners (some runnerpools)", func() {
		By("preparing fake clients")
		runnerPodClient := rc.NewFakeClient()
		githubClient := github.NewFakeClient("runnermanager-org")
		runnerManager := NewRunnerManager(ctrl.Log, time.Second, k8sClient, githubClient, runnerPodClient)

		By("starting metrics server")
		server := &http.Server{Addr: metricsPort, Handler: promhttp.Handler()}
		go func() {
			server.ListenAndServe()
		}()
		defer server.Shutdown(context.Background())
		time.Sleep(1 * time.Second)

		By("creating runnerpools")
		runnerManager.StartOrUpdate(makeRunnerPool("rp1", "test-ns1", "repo1"))
		runnerManager.StartOrUpdate(makeRunnerPool("rp2", "test-ns1", "repo1"))
		runnerManager.StartOrUpdate(makeRunnerPool("rp3", "test-ns2", "repo2"))
		time.Sleep(2 * time.Second)
		MetricsShouldHaveValue(metricsURL, "meows_runnerpool_replicas",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchFields(IgnoreExtras, Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1")}),
				})),
				"1": PointTo(MatchFields(IgnoreExtras, Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp2")}),
				})),
				"2": PointTo(MatchFields(IgnoreExtras, Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns2/rp3")}),
				})),
			}),
		)
		MetricsShouldNotExist(metricsURL, "meows_runner_online")
		MetricsShouldNotExist(metricsURL, "meows_runner_busy")

		By("creating runners")
		runenrs := map[string][]*github.Runner{
			"repo1": {
				{Name: "pod1", ID: 1, Online: true, Busy: true, Labels: []string{"test-ns1/rp1"}},
				{Name: "pod2", ID: 2, Online: true, Busy: false, Labels: []string{"test-ns1/rp1"}},
				{Name: "pod3", ID: 3, Online: true, Busy: false, Labels: []string{"test-ns1/rp2"}},
			},
			"repo2": {
				{Name: "pod4", ID: 4, Online: true, Busy: true, Labels: []string{"test-ns2/rp3"}},
				{Name: "pod5", ID: 5, Online: true, Busy: true, Labels: []string{"test-ns1/rp1"}}, // metrics should not exist.
				{Name: "pod6", ID: 6, Online: true, Busy: true, Labels: []string{}},               // metrics should not exist.
			},
		}
		githubClient.SetRunners(runenrs)
		time.Sleep(3 * time.Second)
		MetricsShouldHaveValue(metricsURL, "meows_runner_online",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod1")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod2")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"2": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp2"), "runner": Equal("pod3")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"3": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns2/rp3"), "runner": Equal("pod4")}),
					"Value": BeNumerically("==", 1.0),
				})),
			}),
		)
		MetricsShouldHaveValue(metricsURL, "meows_runner_busy",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod1")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod2")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"2": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp2"), "runner": Equal("pod3")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"3": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns2/rp3"), "runner": Equal("pod4")}),
					"Value": BeNumerically("==", 1.0),
				})),
			}),
		)

		By("updating runners")
		runenrs = map[string][]*github.Runner{
			"repo1": {
				{Name: "pod1", ID: 1, Online: true, Busy: false, Labels: []string{"test-ns1/rp1"}},
				{Name: "pod2", ID: 2, Online: true, Busy: true, Labels: []string{"test-ns1/rp1"}},
				{Name: "pod3", ID: 3, Online: true, Busy: true, Labels: []string{"test-ns1/rp2"}},
			},
		}
		githubClient.SetRunners(runenrs)
		time.Sleep(3 * time.Second)
		MetricsShouldHaveValue(metricsURL, "meows_runner_online",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod1")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod2")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"2": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp2"), "runner": Equal("pod3")}),
					"Value": BeNumerically("==", 1.0),
				})),
			}),
		)
		MetricsShouldHaveValue(metricsURL, "meows_runner_busy",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod1")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp1"), "runner": Equal("pod2")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"2": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp2"), "runner": Equal("pod3")}),
					"Value": BeNumerically("==", 1.0),
				})),
			}),
		)

		By("deleting runnerpool (1)")
		runnerManager.Stop("test-ns1/rp1")
		time.Sleep(3 * time.Second)
		MetricsShouldHaveValue(metricsURL, "meows_runnerpool_replicas",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchFields(IgnoreExtras, Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp2")}),
				})),
				"1": PointTo(MatchFields(IgnoreExtras, Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns2/rp3")}),
				})),
			}),
		)
		MetricsShouldHaveValue(metricsURL, "meows_runner_online",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp2"), "runner": Equal("pod3")}),
					"Value": BeNumerically("==", 1.0),
				})),
			}),
		)
		MetricsShouldHaveValue(metricsURL, "meows_runner_busy",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("test-ns1/rp2"), "runner": Equal("pod3")}),
					"Value": BeNumerically("==", 1.0),
				})),
			}),
		)
	})
})

func MetricsShouldNotExist(url, name string) {
	_, err := metrics.FetchGauge(context.Background(), url, name)
	ExpectWithOffset(1, err).Should(MatchError(metrics.ErrNotExist))
}

func MetricsShouldHaveValue(url, name string, matcher gomegatypes.GomegaMatcher) {
	m, err := metrics.FetchGauge(context.Background(), url, name)
	ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
	ExpectWithOffset(1, m).To(matcher)
}
