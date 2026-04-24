package cli

import (
	"fmt"
	"os"

	"github.com/BurntSushi/toml"
	"github.com/MatteoAdamo82/loom/internal/config"
	"github.com/spf13/cobra"
)

func cmdInit(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "init",
		Short: "Create the Loom config file and an empty SQLite database.",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfg := config.Default()
			if err := ensureDir(*configPath); err != nil {
				return err
			}
			if _, err := os.Stat(*configPath); err == nil {
				fmt.Fprintf(cmd.OutOrStdout(), "config already exists at %s\n", *configPath)
			} else {
				f, err := os.Create(*configPath)
				if err != nil {
					return fmt.Errorf("create config: %w", err)
				}
				enc := toml.NewEncoder(f)
				if err := enc.Encode(cfg); err != nil {
					_ = f.Close()
					return fmt.Errorf("write config: %w", err)
				}
				_ = f.Close()
				fmt.Fprintf(cmd.OutOrStdout(), "wrote %s\n", *configPath)
			}

			rt, err := bootstrap(*configPath)
			if err != nil {
				return err
			}
			defer rt.Store.Close()

			fmt.Fprintf(cmd.OutOrStdout(), "opened db at %s\n", rt.Cfg.Storage.DBPath)
			fmt.Fprintf(cmd.OutOrStdout(), "llm provider: %s (%s)\n",
				rt.Cfg.LLM.Provider, rt.Cfg.LLM.Model)
			return nil
		},
	}
}
