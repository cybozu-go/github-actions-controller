package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"testing"
	"time"

	constants "github.com/cybozu-go/meows"
	"github.com/cybozu-go/meows/metrics"
	"github.com/cybozu-go/meows/runner/client"
	. "github.com/onsi/ginkgo"
	. "github.com/onsi/gomega"
	. "github.com/onsi/gomega/gstruct"
	gomegatypes "github.com/onsi/gomega/types"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var (
	testRunnerDir = filepath.Join("..", "tmp", "runner")
	testWorkDir   = filepath.Join("..", "tmp", "runner", "_work")
	testVarDir    = filepath.Join("..", "tmp", "var", "meows")
)

var _ = Describe("Runner", func() {
	BeforeEach(func() {
		Expect(os.MkdirAll(testRunnerDir, 0755)).To(Succeed())
		Expect(os.MkdirAll(testWorkDir, 0755)).To(Succeed())
		Expect(os.MkdirAll(testVarDir, 0755)).To(Succeed())
		createFakeTokenFile()

		os.Setenv(constants.PodNameEnvName, "fake-pod-name")
		os.Setenv(constants.PodNamespaceEnvName, "fake-pod-ns")
		os.Setenv(constants.RunnerOrgEnvName, "fake-org")
		os.Setenv(constants.RunnerRepoEnvName, "fake-repo")
		os.Setenv(constants.RunnerPoolNameEnvName, "fake-runnerpool")
		os.Setenv(constants.RunnerOptionEnvName, "{}")
	})

	AfterEach(func() {
		Expect(os.RemoveAll(testRunnerDir)).To(Succeed())
		Expect(os.RemoveAll(testWorkDir)).To(Succeed())
		Expect(os.RemoveAll(testVarDir)).To(Succeed())
		time.Sleep(time.Second)
	})

	It("should change states", func() {
		By("starting runner")
		listener := newListenerMock()
		cancel := startRunner(listener)
		defer cancel()

		By("checking initializing state")
		flagFileShouldExist("started")
		deletionTimeShouldBeZero()
		metricsShouldHaveValue("meows_runner_pod_state",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("debugging")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("initializing")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"2": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("running")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"3": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("stale")}),
					"Value": BeNumerically("==", 0.0),
				})),
			}),
		)
		metricsShouldNotExist("meows_runner_listener_exit_state")

		By("checking running state")
		listener.configureCh <- nil
		time.Sleep(time.Second)

		flagFileShouldExist("started")
		deletionTimeShouldBeZero()
		metricsShouldHaveValue("meows_runner_pod_state",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("debugging")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("initializing")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"2": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("running")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"3": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("stale")}),
					"Value": BeNumerically("==", 0.0),
				})),
			}),
		)
		metricsShouldNotExist("meows_runner_listener_exit_state")

		By("checking debugging state")
		listener.listenCh <- nil
		finishedAt := time.Now()
		time.Sleep(time.Second)

		flagFileShouldExist("started")
		flagFileShouldNotExist("extend")
		flagFileShouldNotExist("failure")
		flagFileShouldNotExist("cancelled")
		flagFileShouldNotExist("success")
		deletionTimeShouldHaveValue("~", finishedAt, 500*time.Millisecond)
		metricsShouldHaveValue("meows_runner_pod_state",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("debugging")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("initializing")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"2": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("running")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"3": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("stale")}),
					"Value": BeNumerically("==", 0.0),
				})),
			}),
		)
		metricsShouldNotExist("meows_runner_listener_exit_state")
	})

	It("should extend default duration when extend file exists", func() {
		By("starting runner")
		listener := newListenerMock("extend")
		cancel := startRunner(listener)
		defer cancel()
		listener.configureCh <- nil
		listener.listenCh <- nil
		finishedAt := time.Now()
		time.Sleep(time.Second)

		By("checking outputs")
		d, err := time.ParseDuration("20m")
		Expect(err).ToNot(HaveOccurred())
		deletionTimeShouldHaveValue("~", finishedAt.Add(d), 500*time.Millisecond)
		metricsShouldHaveValue("meows_runner_pod_state",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("debugging")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("initializing")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"2": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("running")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"3": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("stale")}),
					"Value": BeNumerically("==", 0.0),
				})),
			}),
		)
		metricsShouldNotExist("meows_runner_listener_exit_state")
	})

	It("should extend specified duration", func() {
		By("starting runner with extend duration")
		os.Setenv(constants.ExtendDurationEnvName, "1h")
		listener := newListenerMock("extend")
		cancel := startRunner(listener)
		defer cancel()
		listener.configureCh <- nil
		listener.listenCh <- nil
		finishedAt := time.Now()
		time.Sleep(time.Second)

		By("checking outputs")
		d, err := time.ParseDuration("1h")
		Expect(err).ToNot(HaveOccurred())
		deletionTimeShouldHaveValue("~", finishedAt.Add(d), 500*time.Millisecond)
		metricsShouldHaveValue("meows_runner_pod_state",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("debugging")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("initializing")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"2": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("running")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"3": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("stale")}),
					"Value": BeNumerically("==", 0.0),
				})),
			}),
		)
		metricsShouldNotExist("meows_runner_listener_exit_state")
	})

	It("should become stale state when started file exists", func() {
		By("starting runner with started file")
		createFlagFile("started")
		startedAt := time.Now()
		listener := newListenerMock()
		cancel := startRunner(listener)
		defer cancel()

		By("checking outputs")
		deletionTimeShouldHaveValue("~", startedAt, 500*time.Millisecond)
		metricsShouldHaveValue("meows_runner_pod_state",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("debugging")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("initializing")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"2": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("running")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"3": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("stale")}),
					"Value": BeNumerically("==", 1.0),
				})),
			}),
		)
		metricsShouldNotExist("meows_runner_listener_exit_state")
	})

	It("should run setup command", func() {
		By("starting runner with setup command")
		opt, err := json.Marshal(&Option{
			SetupCommand: []string{"bash", "-c", "touch ./dummy"},
		})
		Expect(err).NotTo(HaveOccurred())
		os.Setenv(constants.RunnerOptionEnvName, string(opt))

		listener := newListenerMock()
		cancel := startRunner(listener)
		defer cancel()

		By("checking outputs")
		_, err = os.Stat(filepath.Join(testRunnerDir, "dummy")) // setup command is run at runner root dir.
		Expect(err).ToNot(HaveOccurred())

		flagFileShouldExist("started")
		deletionTimeShouldBeZero()
		metricsShouldHaveValue("meows_runner_pod_state",
			MatchAllElementsWithIndex(IndexIdentity, Elements{
				"0": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("debugging")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"1": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("initializing")}),
					"Value": BeNumerically("==", 1.0),
				})),
				"2": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("running")}),
					"Value": BeNumerically("==", 0.0),
				})),
				"3": PointTo(MatchAllFields(Fields{
					"Label": MatchAllKeys(Keys{"runnerpool": Equal("fake-pod-ns/fake-runnerpool"), "state": Equal("stale")}),
					"Value": BeNumerically("==", 0.0),
				})),
			}),
		)
		metricsShouldNotExist("meows_runner_listener_exit_state")
	})
})

func TestRunner(t *testing.T) {
	RegisterFailHandler(Fail)

	SetDefaultEventuallyTimeout(10 * time.Second)
	SetDefaultEventuallyPollingInterval(time.Second)
	SetDefaultConsistentlyDuration(10 * time.Second)
	SetDefaultConsistentlyPollingInterval(time.Second)

	RunSpecs(t, "Runner Suite")
}

type listenerMock struct {
	flagFiles   []string
	configureCh chan error
	listenCh    chan error
}

func newListenerMock(flagFiles ...string) *listenerMock {
	return &listenerMock{
		flagFiles:   flagFiles,
		configureCh: make(chan error),
		listenCh:    make(chan error),
	}
}

func (l *listenerMock) configure(ctx context.Context, configArgs []string) error {
	return <-l.configureCh
}

func (l *listenerMock) listen(ctx context.Context) error {
	ret := <-l.listenCh
	for _, file := range l.flagFiles {
		createFlagFile(file)
	}
	return ret
}

func startRunner(listener Listener) context.CancelFunc {
	r, err := NewRunner(listener, fmt.Sprintf(":%d", constants.RunnerListenPort), testRunnerDir, testWorkDir, testVarDir)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	ctx, cancel := context.WithCancel(context.Background())
	logger := zap.New()
	ctx = log.IntoContext(ctx, logger)
	go func() {
		defer GinkgoRecover()
		Expect(r.Run(ctx)).To(Succeed())
	}()
	time.Sleep(2 * time.Second) // delay
	return cancel
}

func createFakeTokenFile() {
	Expect(os.MkdirAll(filepath.Join(testVarDir), 0755)).To(Succeed())
	err := ioutil.WriteFile(filepath.Join(testVarDir, "runnertoken"), []byte("faketoken"), os.ModePerm)
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
}

func createFlagFile(filename string) {
	_, err := os.Create(filepath.Join(testVarDir, filename))
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
}

func flagFileShouldExist(filename string) {
	_, err := os.Stat(filepath.Join(testVarDir, filename))
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
}

func flagFileShouldNotExist(filename string) {
	_, err := os.Stat(filepath.Join(testVarDir, filename))
	ExpectWithOffset(1, err).To(HaveOccurred())
}

func deletionTimeShouldBeZero() {
	runnerClient := client.NewClient()
	tm, err := runnerClient.GetDeletionTime(context.Background(), "localhost")
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	ExpectWithOffset(1, tm).To(BeZero())
}

func deletionTimeShouldHaveValue(comparator string, compareTo time.Time, threshold ...time.Duration) {
	runnerClient := client.NewClient()
	tm, err := runnerClient.GetDeletionTime(context.Background(), "localhost")
	ExpectWithOffset(1, err).ToNot(HaveOccurred())
	ExpectWithOffset(1, tm).To(BeTemporally(comparator, compareTo, threshold...))
}

func metricsShouldNotExist(name string) {
	_, err := metrics.FetchGauge(context.Background(), "http://localhost:8080/metrics", name)
	ExpectWithOffset(1, err).Should(MatchError(metrics.ErrNotExist))
}

func metricsShouldHaveValue(name string, matcher gomegatypes.GomegaMatcher) {
	m, err := metrics.FetchGauge(context.Background(), "http://localhost:8080/metrics", name)
	ExpectWithOffset(1, err).ShouldNot(HaveOccurred())
	ExpectWithOffset(1, m).To(matcher)
}
