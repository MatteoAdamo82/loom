// Typed wrapper around the Wails-generated bindings on window.go.main.App.
// Wails regenerates frontend/wailsjs/go/main/App.{js,d.ts} on every build,
// but writing against window.go directly keeps this file independent of the
// generation step (and keeps types stable in source control).

export type Status = {
  ok: boolean;
  error?: string;
  db_path?: string;
  llm_name?: string;
  config_path?: string;
};

export type NoteSummary = {
  slug: string;
  title: string;
  kind: string;
  summary: string;
  keywords: string[];
  version: number;
  updated: string;
};

export type Link = {
  kind: string;
  other_slug?: string;
  other_title?: string;
  other_kind?: string; // "note" | "source"
  context?: string;
};

export type NoteDetail = NoteSummary & {
  content: string;
  links_out: Link[];
  links_in: Link[];
};

export type Citation = {
  entity_ref: string;
  title: string;
  slug?: string;
};

export type Answer = {
  question: string;
  answer: string;
  expanded: string[];
  citations: Citation[];
};

export type Ingested = {
  source_id: number;
  title: string;
  deduplicated: boolean;
  chunks_created: number;
  notes_created: string[];
  entities_linked: number;
};

export type LintFinding = {
  Kind: string;
  Severity: string;
  Subject: string;
  Message: string;
  Refs: string[];
};

export type LintReport = {
  stats: {
    Notes: number;
    Sources: number;
    Entities: number;
    OrphanNotes: number;
    Duplicates: number;
    Gaps: number;
  };
  findings: LintFinding[];
};

// At runtime Wails injects window.go.main.App with our methods.
type Bindings = {
  Status(): Promise<Status>;
  Reload(): Promise<Status>;
  ListNotes(kind: string, limit: number): Promise<NoteSummary[]>;
  GetNote(slug: string): Promise<NoteDetail>;
  Ask(question: string): Promise<Answer>;
  PickAndIngest(): Promise<Ingested | null>;
  IngestPath(path: string): Promise<Ingested>;
  Lint(): Promise<LintReport>;
};

function api(): Bindings {
  const w = window as any;
  if (!w?.go?.main?.App) {
    throw new Error("Wails bindings not available — open this UI through `wails dev` or the built binary.");
  }
  return w.go.main.App as Bindings;
}

export const Loom = {
  status: () => api().Status(),
  reload: () => api().Reload(),
  listNotes: (kind = "", limit = 200) => api().ListNotes(kind, limit),
  getNote: (slug: string) => api().GetNote(slug),
  ask: (question: string) => api().Ask(question),
  pickAndIngest: () => api().PickAndIngest(),
  ingestPath: (path: string) => api().IngestPath(path),
  lint: () => api().Lint(),
};
