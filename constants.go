package constants

// Container names
const (
	// RunnerContainerName is a container name which runs GitHub Actions runner.
	RunnerContainerName = "runner"
)

// Metadata keys
const (
	// RunnerOrgLabelKey is a label key for organization name.
	RunnerOrgLabelKey = "meows.cybozu.com/organization"

	// RunnerRepoLabelKey is a label key for repository name.
	RunnerRepoLabelKey = "meows.cybozu.com/repository"

	RunnerSecretExpiresAtAnnotationKey = "meows.cybozu.com/expires-at"

	// RunnerPoolFinalizer is a finalizer for runnerpool resource.
	RunnerPoolFinalizer = "meows.cybozu.com/runnerpool"

	// AppNameLabelKey is a label key for application name.
	AppNameLabelKey = "app.kubernetes.io/name"

	// AppComponentLabelKey is a label key for the component.
	AppComponentLabelKey = "app.kubernetes.io/component"

	// AppInstanceLabelKey is a label key for the instance name.
	AppInstanceLabelKey = "app.kubernetes.io/instance"
)

const (
	// AppName is the application name.
	AppName = "meows"

	// AppComponentRunner is the component name for runner.
	AppComponentRunner = "runner"
)

// Container ports
const (
	// RunnerListenPort is the port number for runner container.
	RunnerListenPort = 8080

	// RunnerMetricsPortName is the port name for runner container.
	RunnerMetricsPortName = "metrics"
)

// Container endpoints
const (
	// DeletionTimeEndpoint is the endpoint to get deletion time for runner container.
	DeletionTimeEndpoint = "deletion_time"
)

// Directory path for runner pods.
const (
	// RunnerRootDirPath is a directory path where GitHub Actions Runner will be installed.
	RunnerRootDirPath = "/runner"

	// RunnerWorkDirPath is a working directory path for job execution.
	RunnerWorkDirPath = "/runner/_work"

	// RunnerVarDirPath is a directory path for storing variable files.
	RunnerVarDirPath = "/var/meows"
)

// Environment variables
const (
	// PodNameEnvName is a env field key for POD_NAME.
	PodNameEnvName = "POD_NAME"

	// PodNamespaceEnvName is a env field key for POD_NAME.
	PodNamespaceEnvName = "POD_NAMESPACE"

	// RunnerOrgEnvName is a env field key for RUNNER_ORG.
	RunnerOrgEnvName = "RUNNER_ORG"

	// RunnerRepoEnvName is a env field key for RUNNER_REPO.
	RunnerRepoEnvName = "RUNNER_REPO"

	// RunnerPoolNameEnvName is a env field key for RUNNER_POOL_NAME.
	RunnerPoolNameEnvName = "RUNNER_POOL_NAME"

	// RunnerOptionEnvName is a env field key for RUNNER_OPTION
	RunnerOptionEnvName = "RUNNER_OPTION"

	// ExtendDurationEnvName is a env field key for EXTEND_DURATION
	ExtendDurationEnvName = "EXTEND_DURATION"
)
