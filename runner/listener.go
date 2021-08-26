package runner

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/cybozu-go/meows/metrics"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

type Listener interface {
	configure(ctx context.Context, configArgs []string) error
	listen(ctx context.Context) error
}

type listenerImpl struct {
	runnerDir       string
	configCommand   string
	listenerCommand string
}

func NewListener(runnerDir string) Listener {
	return &listenerImpl{
		runnerDir:       runnerDir,
		configCommand:   filepath.Join(runnerDir, "config.sh"),
		listenerCommand: filepath.Join(runnerDir, "bin", "Runner.Listener"),
	}
}

func (l *listenerImpl) configure(ctx context.Context, configArgs []string) error {
	_, err := runCommand(ctx, l.runnerDir, l.configCommand, configArgs...)
	return err
}

func (l *listenerImpl) listen(ctx context.Context) error {
	logger := log.FromContext(ctx)
	for {
		code, err := runCommand(ctx, l.runnerDir, l.listenerCommand, "run", "--startuptype", "service", "--once")
		if _, ok := err.(*exec.ExitError); !ok {
			return err
		}

		// This logic is based on the following code.
		// ref: https://github.com/actions/runner/blob/v2.278.0/src/Misc/layoutbin/RunnerService.js
		logger.Info("Runner listener exited with error", "code", code)
		switch code {
		case 0:
			logger.Info("Runner listener exit with 0 return code, stop the service, no retry needed.")
			return nil
		case 1:
			logger.Info("Runner listener exit with terminated error, stop the service, no retry needed.")
			return fmt.Errorf("runner listener exit with terminated error: %v", err)
		case 2:
			logger.Info("Runner listener exit with retryable error, re-launch runner in 10 seconds.")
			metrics.IncrementListenerExitState(metrics.RetryableError)
		case 3:
			logger.Info("Runner listener exit because of updating, re-launch runner in 10 seconds.")
			metrics.IncrementListenerExitState(metrics.Updating)
		default:
			logger.Info("Runner listener exit with undefined return code, re-launch runner in 10 seconds.")
			metrics.IncrementListenerExitState(metrics.Undefined)
		}

		// Sleep 10 seconds to wait for the update process finish.
		time.Sleep(10 * time.Second)
	}
}
