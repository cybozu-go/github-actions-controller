package cmd

import (
	"fmt"
	"os"

	constants "github.com/cybozu-go/meows"
	"github.com/cybozu-go/meows/runner"
	"github.com/cybozu-go/well"
	"github.com/spf13/cobra"
)

var config struct {
	listenAddr string
}

var rootCmd = &cobra.Command{
	Use:   "entrypoint",
	Short: "GitHub Actions runner Entrypoint",
	Long:  "GitHub Actions runner Entrypoint",
	RunE: func(cmd *cobra.Command, args []string) error {
		listener := runner.NewListener(constants.RunnerRootDirPath)
		r, err := runner.NewRunner(listener, config.listenAddr, constants.RunnerRootDirPath, constants.RunnerWorkDirPath, constants.RunnerVarDirPath)
		if err != nil {
			return err
		}

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
}
