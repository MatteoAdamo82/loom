package cli

import (
	"fmt"
	"os"
	"os/exec"

	"github.com/BurntSushi/toml"
	"github.com/spf13/cobra"
)

// cmdConfigShow keeps the original `loom config` behaviour for back-compat:
// dump the effective TOML to stdout. We promote it to a parent command with
// `show` (default) and `edit` subcommands.
func cmdConfigShow(configPath *string) *cobra.Command {
	parent := &cobra.Command{
		Use:   "config",
		Short: "Inspect or edit the Loom configuration file.",
		Long: `Inspect or edit ~/.loom/config.toml (or the file pointed at by --config / LOOM_CONFIG).

With no subcommand the effective configuration is printed to stdout, the same
behaviour as ` + "`loom config show`" + `.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigShow(cmd, *configPath)
		},
	}

	show := &cobra.Command{
		Use:   "show",
		Short: "Print the effective configuration as TOML.",
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigShow(cmd, *configPath)
		},
	}

	edit := &cobra.Command{
		Use:   "edit",
		Short: "Open the configuration file in $EDITOR (or $VISUAL).",
		Long: `Open the configuration file in the editor named by $VISUAL or $EDITOR
(falling back to vi). The file is created from defaults if it doesn't exist
yet, mirroring what ` + "`loom init`" + ` does.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runConfigEdit(cmd, *configPath)
		},
	}

	parent.AddCommand(show, edit)
	return parent
}

func runConfigShow(cmd *cobra.Command, path string) error {
	rt, err := bootstrap(path)
	if err != nil {
		return err
	}
	defer rt.Store.Close()
	return toml.NewEncoder(cmd.OutOrStdout()).Encode(rt.Cfg)
}

func runConfigEdit(cmd *cobra.Command, path string) error {
	// Make sure the file (and its parent dir) exist; bootstrap() handles
	// both — it loads defaults when the file is missing and creates the
	// db dir as a side effect.
	rt, err := bootstrap(path)
	if err != nil {
		return err
	}
	_ = rt.Store.Close()

	// If the file genuinely doesn't exist yet, write the defaults so the
	// user has something to edit instead of an empty buffer.
	if _, err := os.Stat(path); os.IsNotExist(err) {
		f, err := os.Create(path)
		if err != nil {
			return fmt.Errorf("create config: %w", err)
		}
		if err := toml.NewEncoder(f).Encode(rt.Cfg); err != nil {
			_ = f.Close()
			return fmt.Errorf("write defaults: %w", err)
		}
		_ = f.Close()
		fmt.Fprintf(cmd.ErrOrStderr(), "wrote default config at %s\n", path)
	}

	editor := os.Getenv("VISUAL")
	if editor == "" {
		editor = os.Getenv("EDITOR")
	}
	if editor == "" {
		editor = "vi"
	}

	c := exec.Command(editor, path)
	c.Stdin = os.Stdin
	c.Stdout = cmd.OutOrStdout()
	c.Stderr = cmd.ErrOrStderr()
	return c.Run()
}
