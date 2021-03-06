package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/ryanuber/go-glob"
	"github.com/spf13/cobra"
	"github.com/vbauerster/mpb"
	"golang.org/x/sync/errgroup"

	"github.com/Shopify/themekit/kit"
)

type (
	commandArbiter struct {
		progress           *mpb.Progress
		verbose            bool
		force              bool
		configPath         string
		allenvs            bool
		environments       stringArgArray
		notifyFile         string
		flagConfig         kit.Configuration
		disableIgnore      bool
		ignoredFiles       stringArgArray
		ignores            stringArgArray
		activeThemeClients []kit.ThemeClient
		manifest           *fileManifest
	}
	cobraCmdE     func(*cobra.Command, []string) error
	arbitratedCmd func(kit.ThemeClient, []string) error
	assetAction   struct {
		asset kit.Asset
		event kit.EventType
	}
)

func newCommandArbiter() *commandArbiter {
	pwd, _ := os.Getwd()
	return &commandArbiter{
		progress:   mpb.New(nil),
		configPath: filepath.Join(pwd, "config.yml"),
		flagConfig: kit.Configuration{},
	}
}

func (arbiter *commandArbiter) generateManifest() error {
	var err error
	arbiter.manifest, err = newFileManifest(filepath.Dir(arbiter.configPath), arbiter.activeThemeClients)
	return err
}

func (arbiter *commandArbiter) generateThemeClients(cmd *cobra.Command, args []string) error {
	arbiter.activeThemeClients = []kit.ThemeClient{}
	configEnvs, err := kit.LoadEnvironments(arbiter.configPath)

	if err != nil && os.IsNotExist(err) {
		return fmt.Errorf("Could not find config file at %v", arbiter.configPath)
	} else if err != nil {
		return err
	}

	for env := range configEnvs {
		if !arbiter.shouldUseEnvironment(env) {
			continue
		}

		config, err := configEnvs.GetConfiguration(env)
		if err != nil {
			return fmt.Errorf(
				"[%s] %s",
				green(config.Environment),
				red(err.Error()),
			)
		}
		if arbiter.disableIgnore {
			config.IgnoredFiles = []string{}
			config.Ignores = []string{}
		}
		if config.Proxy != "" {
			stdOut.Printf(
				"[%s] Proxy URL detected from Configuration: %s SSL Certificate Validation will be disabled!",
				green(config.Environment),
				yellow(config.Proxy),
			)
		}

		client, err := kit.NewThemeClient(config)
		if err != nil {
			return fmt.Errorf(
				"[%s] Could not create a client: %s",
				green(config.Environment),
				red(err.Error()),
			)
		}

		arbiter.activeThemeClients = append(arbiter.activeThemeClients, client)
	}

	if len(arbiter.activeThemeClients) == 0 {
		return fmt.Errorf("Could not load any valid environments")
	}

	return arbiter.generateManifest()
}

func (arbiter *commandArbiter) shouldUseEnvironment(envName string) bool {
	flagEnvs := arbiter.environments.Value()
	if arbiter.allenvs || (len(flagEnvs) == 0 && envName == kit.DefaultEnvironment) {
		return true
	}
	for _, env := range flagEnvs {
		if env == envName || glob.Glob(env, envName) {
			return true
		}
	}
	return false
}

func (arbiter *commandArbiter) forEachClient(handler arbitratedCmd) cobraCmdE {
	return func(cmd *cobra.Command, args []string) error {
		var handlerGroup errgroup.Group
		for _, client := range arbiter.activeThemeClients {
			client := client
			handlerGroup.Go(func() error {
				return handler(client, args)
			})
		}
		return handlerGroup.Wait()
	}
}

func (arbiter *commandArbiter) forSingleClient(handler arbitratedCmd) cobraCmdE {
	return func(cmd *cobra.Command, args []string) error {
		if len(arbiter.activeThemeClients) > 1 {
			return fmt.Errorf("more than one environment specified for a single environment command")
		}

		return handler(arbiter.activeThemeClients[0], args)
	}
}

func (arbiter *commandArbiter) setFlagConfig() {
	if !arbiter.disableIgnore {
		arbiter.flagConfig.IgnoredFiles = arbiter.ignoredFiles.Value()
		arbiter.flagConfig.Ignores = arbiter.ignores.Value()
	}
	kit.SetFlagConfig(arbiter.flagConfig)
}

func (arbiter *commandArbiter) newProgressBar(count int, name string) *mpb.Bar {
	var bar *mpb.Bar
	if !arbiter.verbose && count > 0 {
		bar = arbiter.progress.AddBar(int64(count)).
			PrependName(fmt.Sprintf("[%s]: ", name), 0).
			AppendPercentage().
			PrependCounters(0, 0)
	}
	return bar
}

func (arbiter *commandArbiter) generateAssetActions(client kit.ThemeClient, filenames []string, destructive bool) (map[string]assetAction, error) {
	assetsActions := map[string]assetAction{}
	var err error
	var assets []kit.Asset
	if len(filenames) == 0 && destructive {
		if assets, err = client.AssetList(); err != nil {
			return nil, err
		}
		for _, asset := range assets {
			assetsActions[asset.Key] = assetAction{asset: asset, event: kit.Remove}
		}
	}

	if assets, err = client.LocalAssets(filenames...); err != nil {
		return nil, err
	}
	for _, asset := range assets {
		assetsActions[asset.Key] = assetAction{asset: asset, event: kit.Update}
	}

	return assetsActions, nil
}

func (arbiter *commandArbiter) preflightCheck(actions map[string]assetAction, destructive bool) error {
	if arbiter.force {
		return nil
	}

	for _, client := range arbiter.activeThemeClients {
		diff := arbiter.manifest.Diff(actions, client.Config.Environment)
		if diff.Any(destructive) {
			return diff
		}
	}

	return nil
}
