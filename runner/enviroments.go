package runner

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	constants "github.com/cybozu-go/meows"
)

// Omittable options
type Option struct {
	SetupCommand          []string `json:"setup_command,omitempty"`
	SlackAgentServiceName string   `json:"slack_agent_service_name,omitempty"`
	SlackChannel          string   `json:"slack_channel,omitempty"`
}

type environments struct {
	// Environments
	podName        string
	podNamespace   string
	runnerOrg      string
	runnerRepo     string
	runnerPoolName string
	extendDuration string

	// Options
	option Option
}

func newRunnerEnvs() (*environments, error) {
	envs := &environments{
		podName:        os.Getenv(constants.PodNameEnvName),
		podNamespace:   os.Getenv(constants.PodNamespaceEnvName),
		runnerOrg:      os.Getenv(constants.RunnerOrgEnvName),
		runnerRepo:     os.Getenv(constants.RunnerRepoEnvName),
		runnerPoolName: os.Getenv(constants.RunnerPoolNameEnvName),
	}
	if err := envs.checkRequiredEnvs(); err != nil {
		return nil, err
	}

	str := os.Getenv(constants.ExtendDurationEnvName)
	if len(str) != 0 {
		_, err := time.ParseDuration(str)
		if err != nil {
			return nil, fmt.Errorf("failed to perse %s; %w", constants.ExtendDurationEnvName, err)
		}
		envs.extendDuration = str
	} else {
		envs.extendDuration = "20m"
	}

	optionRaw := os.Getenv(constants.RunnerOptionEnvName)
	if err := json.Unmarshal([]byte(optionRaw), &envs.option); err != nil {
		return nil, fmt.Errorf("failed to unmarshal %s; %w", constants.RunnerOptionEnvName, err)
	}

	return envs, nil
}

func (e *environments) checkRequiredEnvs() error {
	if len(e.podName) == 0 {
		return fmt.Errorf("%s must be set", constants.PodNameEnvName)
	}
	if len(e.podNamespace) == 0 {
		return fmt.Errorf("%s must be set", constants.PodNamespaceEnvName)
	}
	if len(e.runnerOrg) == 0 {
		return fmt.Errorf("%s must be set", constants.RunnerOrgEnvName)
	}
	if len(e.runnerRepo) == 0 {
		return fmt.Errorf("%s must be set", constants.RunnerRepoEnvName)
	}
	if len(e.runnerPoolName) == 0 {
		return fmt.Errorf("%s must be set", constants.RunnerPoolNameEnvName)
	}
	return nil
}
