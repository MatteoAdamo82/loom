package cli

import (
	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
)

func cmdConfigShow(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Print the effective configuration.",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := bootstrap(*configPath)
			if err != nil {
				return err
			}
			defer rt.Store.Close()
			return toml.NewEncoder(cmd.OutOrStdout()).Encode(rt.Cfg)
		},
	}
}
