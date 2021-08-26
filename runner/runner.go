package runner

import (
	"context"
	"encoding/json"
	"fmt"
	"io/ioutil"
	"net/http"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"

	constants "github.com/cybozu-go/meows"
	"github.com/cybozu-go/meows/agent"
	"github.com/cybozu-go/meows/metrics"
	"github.com/cybozu-go/meows/runner/client"
	"github.com/cybozu-go/well"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Runner struct {
	envs       *environments
	listenAddr string
	listener   Listener

	// Directory/File Paths
	runnerDir         string
	workDir           string
	tokenPath         string
	startedFlagFile   string
	extendFlagFile    string
	failureFlagFile   string
	cancelledFlagFile string
	successFlagFile   string

	deletionTime atomic.Value
}

func NewRunner(listener Listener, listenAddr, runnerDir, workDir, varDir string) (*Runner, error) {
	envs, err := newRunnerEnvs()
	if err != nil {
		return nil, err
	}

	r := Runner{
		envs:              envs,
		listenAddr:        listenAddr,
		listener:          listener,
		runnerDir:         runnerDir,
		workDir:           workDir,
		tokenPath:         filepath.Join(varDir, "runnertoken"),
		startedFlagFile:   filepath.Join(varDir, "started"),
		extendFlagFile:    filepath.Join(varDir, "extend"),
		failureFlagFile:   filepath.Join(varDir, "failure"),
		cancelledFlagFile: filepath.Join(varDir, "cancelled"),
		successFlagFile:   filepath.Join(varDir, "success"),
	}

	r.deletionTime.Store(time.Time{})
	return &r, nil
}

func (r *Runner) Run(ctx context.Context) error {
	registry := prometheus.NewRegistry()
	metrics.InitRunnerPodMetrics(registry, r.envs.podNamespace+"/"+r.envs.runnerPoolName)

	env := well.NewEnvironment(ctx)
	env.Go(r.runListener)

	mux := http.NewServeMux()
	mux.Handle("/metrics", promhttp.InstrumentMetricHandler(registry, promhttp.HandlerFor(registry, promhttp.HandlerOpts{})))
	mux.Handle("/"+constants.DeletionTimeEndpoint, http.HandlerFunc(r.deletionTimeHandler))
	serv := &well.HTTPServer{
		Env: env,
		Server: &http.Server{
			Addr:    r.listenAddr,
			Handler: mux,
		},
	}
	if err := serv.ListenAndServe(); err != nil {
		return err
	}

	env.Stop()
	return env.Wait()
}

func (r *Runner) runListener(ctx context.Context) error {
	logger := log.FromContext(ctx)
	if isFileExists(r.startedFlagFile) {
		metrics.UpdateRunnerPodState(metrics.Stale)
		logger.Info("Pod is stale; waiting for deletion")
		r.deletionTime.Store(time.Now())
		<-ctx.Done()
		return nil
	}

	if _, err := os.Create(r.startedFlagFile); err != nil {
		return err
	}
	metrics.UpdateRunnerPodState(metrics.Initializing)
	if len(r.envs.option.SetupCommand) != 0 {
		if _, err := runCommand(ctx, r.runnerDir, r.envs.option.SetupCommand[0], r.envs.option.SetupCommand[1:]...); err != nil {
			return err
		}
	}

	b, err := ioutil.ReadFile(r.tokenPath)
	if err != nil {
		return fmt.Errorf("failed load %s; %w", r.tokenPath, err)
	}

	configArgs := []string{
		"--unattended",
		"--replace",
		"--name", r.envs.podName,
		"--labels", r.envs.podNamespace + "/" + r.envs.runnerPoolName,
		"--url", fmt.Sprintf("https://github.com/%s/%s", r.envs.runnerOrg, r.envs.runnerRepo),
		"--token", string(b),
		"--work", r.workDir,
	}
	if err := r.listener.configure(ctx, configArgs); err != nil {
		return err
	}

	metrics.UpdateRunnerPodState(metrics.Running)
	if err := r.listener.listen(ctx); err != nil {
		return err
	}

	metrics.UpdateRunnerPodState(metrics.Debugging)
	extend := isFileExists(r.extendFlagFile)
	err = r.updateDeletionTime(ctx, extend)
	if err != nil {
		return err
	}
	if err := r.notifyToSlack(ctx, extend); err != nil {
		return err
	}

	<-ctx.Done()
	return nil
}

func (r *Runner) updateDeletionTime(ctx context.Context, extend bool) error {
	logger := log.FromContext(ctx)
	if extend {
		dur, err := time.ParseDuration(r.envs.extendDuration)
		if err != nil {
			return err
		}
		logger.Info(fmt.Sprintf("Update pod's deletion time with the time %s later\n", r.envs.extendDuration))
		r.deletionTime.Store(time.Now().UTC().Add(dur))
	} else {
		logger.Info("Update pod's deletion time with current time")
		r.deletionTime.Store(time.Now().UTC())
	}
	return nil
}

func (r *Runner) notifyToSlack(ctx context.Context, extend bool) error {
	logger := log.FromContext(ctx)
	var jobResult string
	switch {
	case isFileExists(r.failureFlagFile):
		jobResult = agent.JobResultFailure
	case isFileExists(r.cancelledFlagFile):
		jobResult = agent.JobResultCancelled
	case isFileExists(r.successFlagFile):
		jobResult = agent.JobResultSuccess
	default:
		jobResult = agent.JobResultUnknown
	}
	if len(r.envs.option.SlackAgentServiceName) != 0 {
		logger.Info("Send an notification to slack", "jobResult", jobResult)
		c, err := agent.NewClient(fmt.Sprintf("http://%s", r.envs.option.SlackAgentServiceName))
		if err != nil {
			return err
		}
		jobInfo, err := agent.GetJobInfoFromFile(agent.DefaultJobInfoFile)
		if err != nil {
			return err
		}
		return c.PostResult(ctx, r.envs.option.SlackChannel, jobResult, extend, r.envs.podNamespace, r.envs.podName, jobInfo)
	} else {
		logger.Info("Skip sending an notification to slack because Slack agent service name is blank")
	}
	return nil
}

func (r *Runner) deletionTimeHandler(w http.ResponseWriter, req *http.Request) {
	switch req.Method {
	case http.MethodGet:
		tm, ok := r.deletionTime.Load().(time.Time)
		if !ok {
			http.Error(w, "Failed to load the deletion time", http.StatusInternalServerError)
			return
		}
		res, err := json.Marshal(client.DeletionTimePayload{
			DeletionTime: tm,
		})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(res)
		return
	case http.MethodPut:
		var dt client.DeletionTimePayload
		if req.Header.Get("Content-Type") != "application/json" {
			w.WriteHeader(http.StatusUnsupportedMediaType)
			return
		}
		err := json.NewDecoder(req.Body).Decode(&dt)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		r.deletionTime.Store(dt.DeletionTime)

		w.WriteHeader(http.StatusNoContent)
		return
	default:
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
}
