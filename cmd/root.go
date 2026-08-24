package cmd

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var rootCmd = &cobra.Command{
	Use:   "homelabmon",
	Short: "HomeMonitor - homelab monitoring mesh",
	Long:  `A single-binary, zero-dependency homelab monitoring system with auto-discovery, CMDB, and optional LLM integration.`,
}

func Execute() error {
	return rootCmd.Execute()
}

func init() {
	cobra.OnInitialize(initConfig)

	rootCmd.PersistentFlags().String("config", "", "config file (default ~/.homelabmon/config.yaml)")
	rootCmd.PersistentFlags().String("data-dir", "", "data directory (default ~/.homelabmon)")
	rootCmd.PersistentFlags().String("log-level", "info", "log level: debug, info, warn, error")

	viper.BindPFlag("data-dir", rootCmd.PersistentFlags().Lookup("data-dir"))
	viper.BindPFlag("log-level", rootCmd.PersistentFlags().Lookup("log-level"))

	// Default command: if no subcommand given, run the agent
	rootCmd.RunE = func(cmd *cobra.Command, args []string) error {
		return runCmd.RunE(runCmd, args)
	}
}

func initConfig() {
	cfgFile, _ := rootCmd.Flags().GetString("config")
	if cfgFile != "" {
		viper.SetConfigFile(cfgFile)
	} else {
		home, _ := os.UserHomeDir()
		viper.AddConfigPath(filepath.Join(home, ".homelabmon"))
		viper.SetConfigName("config")
		viper.SetConfigType("yaml")
	}
	viper.SetEnvPrefix("HOMELABMON")
	viper.SetEnvKeyReplacer(strings.NewReplacer("-", "_"))
	viper.AutomaticEnv()
	viper.ReadInConfig()

	// Enable config file watching (callbacks registered in runAgent)
	viper.WatchConfig()

	// Setup logger
	level, _ := zerolog.ParseLevel(viper.GetString("log-level"))
	if level == zerolog.NoLevel {
		level = zerolog.InfoLevel
	}
	zerolog.SetGlobalLevel(level)
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
}

func dataDir() string {
	d := viper.GetString("data-dir")
	if d != "" {
		return d
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".homelabmon")
}
