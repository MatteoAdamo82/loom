package cli

import (
	"fmt"
	"strings"

	"github.com/MatteoAdamo82/loom/internal/query"
	"github.com/spf13/cobra"
)

func cmdQuery(configPath *string) *cobra.Command {
	var showDebug bool
	var format string
	var noStream bool
	c := &cobra.Command{
		Use:   "query <question>",
		Short: "Ask a question against the knowledge base.",
		Args:  cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := bootstrap(*configPath)
			if err != nil {
				return err
			}
			defer rt.Store.Close()

			client, err := makeLLM(rt.Cfg.LLM)
			if err != nil {
				return err
			}

			eng := query.NewEngine(rt.Store, client)
			eng.Cfg = query.Config{
				BM25TopK:       rt.Cfg.Query.BM25TopK,
				GraphExpandHop: rt.Cfg.Query.GraphExpandHop,
				RerankTopK:     rt.Cfg.Query.RerankTopK,
				Format:         query.ParseFormat(format),
			}

			out := cmd.OutOrStdout()
			streaming := !noStream
			var streamed bool
			if streaming {
				eng.OnSynthesisChunk = func(s string) {
					streamed = true
					_, _ = fmt.Fprint(out, s)
					if f, ok := out.(interface{ Sync() error }); ok {
						_ = f.Sync()
					}
				}
			}

			question := strings.Join(args, " ")
			ans, err := eng.Run(cliContext(cmd), question)
			if err != nil {
				if streamed {
					fmt.Fprintln(out)
				}
				return err
			}

			if streamed {
				// Streamed output already wrote the content to stdout — just
				// terminate the line.
				fmt.Fprintln(out)
			} else {
				fmt.Fprintln(out, ans.Content)
			}

			if showDebug {
				fmt.Fprintln(out, "\n---")
				fmt.Fprintf(out, "expanded: %s\n", strings.Join(ans.Expanded, " · "))
				fmt.Fprintln(out, "candidates:")
				for _, c := range ans.Candidates {
					fmt.Fprintf(out, "  %-10s  %s  (%s)\n", c.EntityRef, c.Title, c.Kind)
				}
			}
			return nil
		},
	}
	c.Flags().BoolVar(&showDebug, "debug", false, "print expansion and reranked candidates")
	c.Flags().StringVar(&format, "format", "markdown",
		"answer format: markdown (default) | marp | text")
	c.Flags().BoolVar(&noStream, "no-stream", false,
		"buffer the full answer before printing instead of streaming tokens live")
	return c
}
