package cmd

import (
	"flag"
	"fmt"
	"os"

	constants "github.com/cybozu-go/meows"
	"github.com/cybozu-go/meows/runner"
	"github.com/cybozu-go/well"
	"github.com/spf13/cobra"
	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"
)

var config struct {
	zapOpts    zap.Options
	listenAddr string
}

var rootCmd = &cobra.Command{
	Use:   "entrypoint",
	Short: "GitHub Actions runner Entrypoint",
	Long:  "GitHub Actions runner Entrypoint",
	RunE: func(cmd *cobra.Command, args []string) error {
		// FIXME: Use emptyDir.
		if err := os.MkdirAll(constants.RunnerWorkDirPath, 0755); err != nil {
			return err
		}
		listener := runner.NewListener(constants.RunnerRootDirPath)
		r, err := runner.NewRunner(listener, config.listenAddr, constants.RunnerRootDirPath, constants.RunnerWorkDirPath, constants.RunnerVarDirPath)
		if err != nil {
			return err
		}
		podName := os.Getenv(constants.PodNameEnvName)
		logger := zap.New(zap.UseFlagOptions(&config.zapOpts)).WithName("runner").WithValues("pod", podName)
		log.SetLogger(logger)
		well.Go(r.Run)

		well.Stop()
		return well.Wait()
	},
}

// Execute adds all child commands to the root command and sets flags appropriately.
// This is called by main.main(). It only needs to happen once to the rootCmd.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func init() {
	fs := rootCmd.Flags()
	fs.StringVar(&config.listenAddr, "listen-address", fmt.Sprintf(":%d", constants.RunnerListenPort), "Listening address and port.")

	goflags := flag.NewFlagSet("klog", flag.ExitOnError)
	config.zapOpts.BindFlags(goflags)
	fs.AddGoFlagSet(goflags)
}
