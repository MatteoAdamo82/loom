package cli

import (
	"fmt"

	"github.com/MatteoAdamo82/loom/internal/extract"
	"github.com/MatteoAdamo82/loom/internal/ingest"
	"github.com/spf13/cobra"
)

func cmdIngest(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "ingest <path> [<path>...]",
		Short: "Ingest one or more files into the knowledge base.",
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

			p := ingest.NewPipeline(rt.Store, client)
			p.Registry = extract.NewRegistryWithPDF(extract.PDF{
				OCRMode:      extract.OCRMode(rt.Cfg.Extract.PDF.OCR),
				OCRLanguages: rt.Cfg.Extract.PDF.OCRLanguages,
				CacheDir:     rt.Cfg.Extract.PDF.CacheDir,
				OCRDPI:       rt.Cfg.Extract.PDF.OCRDPI,
			})
			p.ChunkCfg = ingest.ChunkConfig{
				MaxTokens: rt.Cfg.Ingest.ChunkTokens,
				Overlap:   rt.Cfg.Ingest.ChunkOverlap,
			}
			p.MaxAnalyze = rt.Cfg.Ingest.MaxAnalyze

			ctx := cliContext(cmd)
			out := cmd.OutOrStdout()

			for _, path := range args {
				fmt.Fprintf(out, "→ %s\n", path)
				res, err := p.Ingest(ctx, path)
				if err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "  failed: %v\n", err)
					continue
				}
				if res.Deduplicated {
					fmt.Fprintf(out, "  skipped (already ingested, id=%d)\n", res.Source.ID)
					continue
				}
				fmt.Fprintf(out,
					"  source id=%d title=%q chunks=%d notes_created=%d entities_linked=%d\n",
					res.Source.ID, res.Source.Title, res.ChunksCreated,
					len(res.NotesCreated), res.EntitiesLinked,
				)
			}
			return nil
		},
	}
}
