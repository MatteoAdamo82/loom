package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"
)

func cmdNotes(configPath *string) *cobra.Command {
	var kind string
	var limit, offset int
	list := &cobra.Command{
		Use:   "notes",
		Short: "List curated notes.",
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := bootstrap(*configPath)
			if err != nil {
				return err
			}
			defer rt.Store.Close()

			notes, err := rt.Store.ListNotes(cliContext(cmd), kind, limit, offset)
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			if len(notes) == 0 {
				fmt.Fprintln(out, "no notes yet — try: loom ingest <file>")
				return nil
			}
			for _, n := range notes {
				kw := strings.Join(n.Keywords, ", ")
				fmt.Fprintf(out, "%-40s  %-10s  v%d  %s\n", n.Slug, n.Kind, n.Version, kw)
			}
			return nil
		},
	}
	list.Flags().StringVar(&kind, "kind", "", "filter by note kind")
	list.Flags().IntVar(&limit, "limit", 50, "max rows")
	list.Flags().IntVar(&offset, "offset", 0, "skip rows")
	return list
}

func cmdNoteShow(configPath *string) *cobra.Command {
	return &cobra.Command{
		Use:   "note <slug>",
		Short: "Show the full content of a note.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			rt, err := bootstrap(*configPath)
			if err != nil {
				return err
			}
			defer rt.Store.Close()

			n, err := rt.Store.GetNoteBySlug(cliContext(cmd), args[0])
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			fmt.Fprintf(out, "# %s\n", n.Title)
			fmt.Fprintf(out, "slug: %s\nkind: %s\nversion: %d\nupdated: %s\n",
				n.Slug, n.Kind, n.Version, n.UpdatedAt.Format("2006-01-02 15:04"))
			if len(n.Keywords) > 0 {
				fmt.Fprintf(out, "keywords: %s\n", strings.Join(n.Keywords, ", "))
			}
			fmt.Fprintln(out)
			fmt.Fprintln(out, n.Content)

			inbound, _ := rt.Store.LinksToNote(cliContext(cmd), n.ID)
			outbound, _ := rt.Store.LinksFromNote(cliContext(cmd), n.ID)
			if len(inbound)+len(outbound) > 0 {
				fmt.Fprintln(out, "\n---")
				if len(inbound) > 0 {
					fmt.Fprintf(out, "linked from: %d notes/sources\n", len(inbound))
				}
				if len(outbound) > 0 {
					fmt.Fprintf(out, "links to:    %d notes/sources\n", len(outbound))
				}
			}
			return nil
		},
	}
}
