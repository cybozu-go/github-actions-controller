package controllers

import (
	"context"
	"sync"
	"time"

	constants "github.com/cybozu-go/meows"
	"github.com/cybozu-go/meows/agent"
	meowsv1alpha1 "github.com/cybozu-go/meows/api/v1alpha1"
	"github.com/cybozu-go/meows/github"
	"github.com/cybozu-go/meows/metrics"
	"github.com/cybozu-go/meows/runner"
	"github.com/cybozu-go/well"
	"github.com/go-logr/logr"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

//+kubebuilder:rbac:groups="",resources=pods,verbs=get;list;watch;delete;update

type RunnerManager interface {
	StartOrUpdate(*meowsv1alpha1.RunnerPool) error
	Stop(context.Context, *meowsv1alpha1.RunnerPool) error
}

func namespacedName(namespace, name string) string {
	return namespace + "/" + name
}

type RunnerManagerImpl struct {
	log             logr.Logger
	interval        time.Duration
	k8sClient       client.Client
	githubClient    github.Client
	runnerPodClient runner.Client

	loops map[string]*managerLoop
}

func NewRunnerManager(log logr.Logger, interval time.Duration, k8sClient client.Client, githubClient github.Client, runnerPodClient runner.Client) RunnerManager {
	return &RunnerManagerImpl{
		log:             log.WithName("RunnerManager"),
		interval:        interval,
		k8sClient:       k8sClient,
		githubClient:    githubClient,
		runnerPodClient: runnerPodClient,
		loops:           map[string]*managerLoop{},
	}
}

func (m *RunnerManagerImpl) StartOrUpdate(rp *meowsv1alpha1.RunnerPool) error {
	rpNamespacedName := namespacedName(rp.Namespace, rp.Name)
	if _, ok := m.loops[rpNamespacedName]; !ok {
		loop, err := newManagerLoop(m.log.WithValues("runnerpool", rpNamespacedName), m.interval, m.k8sClient, m.githubClient, m.runnerPodClient, rp)
		if err != nil {
			return err
		}
		loop.start()
		m.loops[rpNamespacedName] = loop
		return nil
	}
	return m.loops[rpNamespacedName].update(rp)
}

func (m *RunnerManagerImpl) Stop(ctx context.Context, rp *meowsv1alpha1.RunnerPool) error {
	rpNamespacedName := namespacedName(rp.Namespace, rp.Name)
	if loop, ok := m.loops[rpNamespacedName]; ok {
		if err := loop.stop(ctx); err != nil {
			return err
		}
		delete(m.loops, rpNamespacedName)
	}

	runnerList, err := m.githubClient.ListRunners(ctx, rp.Spec.RepositoryName, []string{rpNamespacedName})
	if err != nil {
		m.log.Error(err, "failed to list runners")
		return err
	}
	for _, runner := range runnerList {
		err := m.githubClient.RemoveRunner(ctx, rp.Spec.RepositoryName, runner.ID)
		if err != nil {
			m.log.Error(err, "failed to remove runner", "runner", runner.Name, "runner_id", runner.ID)
			return err
		}
		m.log.Info("removed runner", "runner", runner.Name, "runner_id", runner.ID)
	}
	return nil
}

type managerLoop struct {
	// Given from outside. Not update internally.
	log                   logr.Logger
	interval              time.Duration
	k8sClient             client.Client
	githubClient          github.Client
	runnerPodClient       runner.Client
	slackAgentClient      *agent.Client
	rpNamespace           string
	rpName                string
	repository            string
	replicas              int32 // This field will be accessed from multiple goroutines. So use mutex to access.
	maxRunnerPods         int32 // This field will be accessed from multiple goroutines. So use mutex to access.
	slackChannel          string
	slackAgentServiceName string
	recreateDeadline      time.Duration

	// Update internally.
	lastCheckTime   time.Time
	env             *well.Environment
	cancel          context.CancelFunc
	prevRunnerNames []string
	mu              sync.Mutex
}

func newManagerLoop(log logr.Logger, interval time.Duration, k8sClient client.Client, githubClient github.Client, runnerPodClient runner.Client, rp *meowsv1alpha1.RunnerPool) (*managerLoop, error) {
	recreateDeadline, _ := time.ParseDuration(rp.Spec.RecreateDeadline)
	agentClient, err := agent.NewClient("http://" + rp.Spec.SlackAgent.ServiceName)
	if err != nil {
		return nil, err
	}
	loop := &managerLoop{
		log:                   log,
		interval:              interval,
		k8sClient:             k8sClient,
		githubClient:          githubClient,
		runnerPodClient:       runnerPodClient,
		rpNamespace:           rp.Namespace,
		rpName:                rp.Name,
		repository:            rp.Spec.RepositoryName,
		replicas:              rp.Spec.Replicas,
		maxRunnerPods:         rp.Spec.MaxRunnerPods,
		slackAgentClient:      agentClient,
		slackChannel:          rp.Spec.SlackAgent.Channel,
		slackAgentServiceName: rp.Spec.SlackAgent.ServiceName,
		recreateDeadline:      recreateDeadline,
		lastCheckTime:         time.Now().UTC(),
	}
	return loop, nil
}

func (m *managerLoop) update(rp *meowsv1alpha1.RunnerPool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.replicas = rp.Spec.Replicas
	m.maxRunnerPods = rp.Spec.MaxRunnerPods
	m.slackChannel = rp.Spec.SlackAgent.Channel
	if m.slackAgentServiceName != rp.Spec.SlackAgent.ServiceName {
		err := m.slackAgentClient.UpdateServerURL(rp.Spec.SlackAgent.ServiceName)
		if err != nil {
			return err
		}
		m.slackAgentServiceName = rp.Spec.SlackAgent.ServiceName
	}
	return nil
}

func (m *managerLoop) rpNamespacedName() string {
	return m.rpNamespace + "/" + m.rpName
}

// Start starts loop to manage Actions runner
func (m *managerLoop) start() {
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.env = well.NewEnvironment(ctx)

	m.env.Go(func(ctx context.Context) error {
		ticker := time.NewTicker(m.interval)
		defer ticker.Stop()
		m.log.Info("start a watching loop")
		for {
			select {
			case <-ctx.Done():
				m.log.Info("stop a watching loop")
				return nil
			case <-ticker.C:
				err := m.runOnce(ctx)
				if err != nil {
					m.log.Error(err, "failed to run a watching loop")
				}
			}
		}
	})
	m.env.Stop()
}

func (m *managerLoop) stop(ctx context.Context) error {
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
		if err := m.env.Wait(); err != nil {
			return err
		}
	}

	for _, runner := range m.prevRunnerNames {
		metrics.DeleteRunnerMetrics(m.rpNamespacedName(), runner)
	}
	metrics.DeleteRunnerPoolMetrics(m.rpNamespacedName())
	return nil
}

func (m *managerLoop) runOnce(ctx context.Context) error {
	podList, err := m.fetchRunnerPods(ctx)
	if err != nil {
		return err
	}
	runnerList, err := m.fetchRunners(ctx)
	if err != nil {
		return err
	}
	m.updateMetrics(podList, runnerList)

	err = m.maintainRunnerPods(ctx, runnerList, podList)
	if err != nil {
		return err
	}
	err = m.deleteOfflineRunners(ctx, runnerList, podList)
	if err != nil {
		return err
	}

	return nil
}

func (m *managerLoop) fetchRunnerPods(ctx context.Context) (*corev1.PodList, error) {
	selector, err := metav1.LabelSelectorAsSelector(
		&metav1.LabelSelector{
			MatchLabels: map[string]string{
				constants.AppNameLabelKey:      constants.AppName,
				constants.AppComponentLabelKey: constants.AppComponentRunner,
				constants.AppInstanceLabelKey:  m.rpName,
			},
		},
	)
	if err != nil {
		m.log.Error(err, "failed to make label selector")
		return nil, err
	}

	podList := new(corev1.PodList)
	err = m.k8sClient.List(ctx, podList, client.InNamespace(m.rpNamespace), client.MatchingLabelsSelector{
		Selector: selector,
	})
	if err != nil {
		m.log.Error(err, "failed to list pods")
		return nil, err
	}
	return podList, nil
}

func (m *managerLoop) fetchRunners(ctx context.Context) ([]*github.Runner, error) {
	runnerList, err := m.githubClient.ListRunners(ctx, m.repository, []string{m.rpNamespacedName()})
	if err != nil {
		m.log.Error(err, "failed to list runners")
		return nil, err
	}
	return runnerList, nil
}

func (m *managerLoop) updateMetrics(podList *corev1.PodList, runnerList []*github.Runner) {
	m.mu.Lock()
	metrics.UpdateRunnerPoolMetrics(m.rpNamespacedName(), int(m.replicas))
	m.mu.Unlock()

	var currentRunnerNames []string
	for _, runner := range runnerList {
		metrics.UpdateRunnerMetrics(m.rpNamespacedName(), runner.Name, runner.Online, runner.Busy)
		currentRunnerNames = append(currentRunnerNames, runner.Name)
	}

	// Sometimes, offline runners will be deleted from github automatically.
	// Therefore, compare the past runners with the current runners and remove the metrics for the deleted runners.
	for _, removedRunnerName := range difference(m.prevRunnerNames, currentRunnerNames) {
		metrics.DeleteRunnerMetrics(m.rpNamespacedName(), removedRunnerName)
	}
	m.prevRunnerNames = currentRunnerNames
}

func difference(prev, current []string) []string {
	set := map[string]bool{}
	for _, val := range current {
		set[val] = true
	}

	var ret []string
	for _, val := range prev {
		if !set[val] {
			ret = append(ret, val)
		}
	}
	return ret
}

func (m *managerLoop) notifyToSlack(ctx context.Context, po *corev1.Pod, status *runner.Status) error {
	slackChannel := status.SlackChannel
	if slackChannel == "" {
		slackChannel = m.slackChannel
	}
	return m.slackAgentClient.PostResult(ctx, slackChannel, status.Result, *status.Extend, po.Namespace, po.Name, status.JobInfo)
}

func (m *managerLoop) maintainRunnerPods(ctx context.Context, runnerList []*github.Runner, podList *corev1.PodList) error {
	now := time.Now().UTC()
	lastCheckTime := m.lastCheckTime
	m.lastCheckTime = now

	var numUnlabeledPods int32
	for i := range podList.Items {
		po := &podList.Items[i]
		if _, ok := po.Labels[appsv1.DefaultDeploymentUniqueLabelKey]; !ok {
			numUnlabeledPods++
		}
	}
	m.mu.Lock()
	numRemovablePods := m.maxRunnerPods - m.replicas - numUnlabeledPods
	m.mu.Unlock()
	if numRemovablePods < 0 {
		numRemovablePods = 0
	}

	for i := range podList.Items {
		po := &podList.Items[i]
		log := m.log.WithValues("pod", namespacedName(po.Namespace, po.Name))

		status, err := m.runnerPodClient.GetStatus(ctx, po.Status.PodIP)
		if err != nil {
			log.Error(err, "failed to get status, skipped maintaining runner pod")
			continue
		}

		if status.State == constants.RunnerPodStateStale {
			err = m.k8sClient.Delete(ctx, po)
			if err != nil && !apierrors.IsNotFound(err) {
				log.Error(err, "failed to delete stale runner pod")
			} else {
				log.Info("deleted stale runner pod")
			}
			continue
		}

		if status.State == constants.RunnerPodStateDebugging {
			if status.FinishedAt.After(lastCheckTime) && len(m.slackAgentServiceName) != 0 {
				err := m.notifyToSlack(ctx, po, status)
				if err != nil {
					log.Error(err, "failed to send a notification to slack-agent")
				} else {
					log.Info("sent a notification to slack-agent")
				}
			}

			if now.After(*status.DeletionTime) {
				err := m.k8sClient.Delete(ctx, po)
				if err != nil && !apierrors.IsNotFound(err) {
					log.Error(err, "failed to delete debugging runner pod")
				} else {
					log.Info("deleted debugging runner pod")
				}
				continue
			}
		}

		podRecreateTime := po.CreationTimestamp.Add(m.recreateDeadline)
		if podRecreateTime.Before(now) && !(runnerBusy(runnerList, po.Name) || status.State == constants.RunnerPodStateDebugging) {
			err = m.k8sClient.Delete(ctx, po)
			if err != nil && !apierrors.IsNotFound(err) {
				m.log.Error(err, "failed to delete runner pod that exceeded recreate deadline", "pod", namespacedName(po.Namespace, po.Name))
			} else {
				m.log.Info("deleted runner pod that exceeded recreate deadline", "pod", namespacedName(po.Namespace, po.Name))
			}
			continue
		}

		// When a job is assigned, the runner pod will be removed from replicaset control.
		if runnerBusy(runnerList, po.Name) || status.State == constants.RunnerPodStateDebugging {
			if numRemovablePods <= 0 {
				continue
			}
			if _, ok := po.Labels[appsv1.DefaultDeploymentUniqueLabelKey]; !ok {
				continue
			}
			delete(po.Labels, appsv1.DefaultDeploymentUniqueLabelKey)
			err = m.k8sClient.Update(ctx, po)
			if err != nil {
				log.Error(err, "failed to unlink (update) runner pod")
				continue
			}
			numRemovablePods--
			log.Info("unlinked (updated) runner pod")
		}
	}
	return nil
}

func runnerBusy(runnerList []*github.Runner, name string) bool {
	for _, runner := range runnerList {
		if runner.Name == name {
			return runner.Busy
		}
	}
	return false
}

func (m *managerLoop) deleteOfflineRunners(ctx context.Context, runnerList []*github.Runner, podList *corev1.PodList) error {
	for _, runner := range runnerList {
		if runner.Online || podExists(runner.Name, podList) {
			continue
		}
		err := m.githubClient.RemoveRunner(ctx, m.repository, runner.ID)
		if err != nil {
			m.log.Error(err, "failed to remove runner", "runner", runner.Name, "runner_id", runner.ID)
			return err
		}
		m.log.Info("removed runner", "runner", runner.Name, "runner_id", runner.ID)
	}
	return nil
}

func podExists(name string, podList *corev1.PodList) bool {
	for i := range podList.Items {
		if podList.Items[i].Name == name {
			return true
		}
	}
	return false
}
