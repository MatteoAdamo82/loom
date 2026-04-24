package cli

import (
	"fmt"

	"github.com/MatteoAdamo82/loom/internal/lint"
	"github.com/spf13/cobra"
)

func cmdLint(configPath *string) *cobra.Command {
	var minOverlap float64
	c := &cobra.Command{
		Use:   "lint",
		Short: "Inspect the knowledge base for orphans, near-duplicates, and gaps.",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := bootstrap(*configPath)
			if err != nil {
				return err
			}
			defer rt.Store.Close()

			report, err := lint.Run(cliContext(cmd), rt.Store, lint.Config{
				MinKeywordOverlap: minOverlap,
			})
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "stats: notes=%d sources=%d entities=%d  →  orphans=%d duplicates=%d gaps=%d\n\n",
				report.Stats.Notes, report.Stats.Sources, report.Stats.Entities,
				report.Stats.OrphanNotes, report.Stats.Duplicates, report.Stats.Gaps,
			)

			if len(report.Findings) == 0 {
				fmt.Fprintln(out, "no findings — your knowledge base is tidy.")
				return nil
			}

			lint.SortFindings(report.Findings)
			for _, f := range report.Findings {
				fmt.Fprintf(out, "[%s] %-9s  %s\n        %s\n",
					f.Severity, f.Kind, f.Subject, f.Message)
			}
			return nil
		},
	}
	c.Flags().Float64Var(&minOverlap, "min-overlap", 0.6,
		"minimum keyword Jaccard score (0..1) to flag two notes as duplicates")
	return c
}
