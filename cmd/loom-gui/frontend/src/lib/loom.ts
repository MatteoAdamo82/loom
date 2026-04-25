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

export type Settings = {
  config_path: string;
  provider: string;
  model: string;
  endpoint: string;
  api_key_env: string;
  error?: string;
};

export type OllamaModel = {
  name: string;
  size: number;
};

export type AnswerFormat = "markdown" | "marp" | "text";

// At runtime Wails injects window.go.main.App with our methods.
type Bindings = {
  Status(): Promise<Status>;
  Reload(): Promise<Status>;
  ListNotes(kind: string, limit: number): Promise<NoteSummary[]>;
  GetNote(slug: string): Promise<NoteDetail>;
  Ask(question: string, format: AnswerFormat): Promise<Answer>;
  PickAndIngest(): Promise<Ingested | null>;
  IngestPath(path: string): Promise<Ingested>;
  Lint(): Promise<LintReport>;
  Settings(): Promise<Settings>;
  SaveSettings(provider: string, model: string, endpoint: string, apiKeyEnv: string): Promise<Status>;
  ListOllamaModels(endpoint: string): Promise<OllamaModel[] | null>;
};

// Wails also injects window.runtime with EventsOn / EventsOff helpers used to
// receive streaming token deltas while Ask() is in flight.
type WailsRuntime = {
  EventsOn(name: string, cb: (...args: any[]) => void): () => void;
  EventsOff(name: string, ...rest: any[]): void;
};

function api(): Bindings {
  const w = window as any;
  if (!w?.go?.main?.App) {
    throw new Error("Wails bindings not available — open this UI through `wails dev` or the built binary.");
  }
  return w.go.main.App as Bindings;
}

function rt(): WailsRuntime | null {
  const w = window as any;
  return (w?.runtime as WailsRuntime) ?? null;
}

// Event names mirror the constants on the Go side.
export const AnswerEvents = {
  Start: "loom:answer:start",
  Chunk: "loom:answer:chunk",
  End: "loom:answer:end",
} as const;

export const Loom = {
  status: () => api().Status(),
  reload: () => api().Reload(),
  listNotes: (kind = "", limit = 200) => api().ListNotes(kind, limit),
  getNote: (slug: string) => api().GetNote(slug),
  ask: (question: string, format: AnswerFormat = "markdown") => api().Ask(question, format),
  pickAndIngest: () => api().PickAndIngest(),
  ingestPath: (path: string) => api().IngestPath(path),
  lint: () => api().Lint(),
  settings: () => api().Settings(),
  saveSettings: (provider: string, model: string, endpoint: string, apiKeyEnv: string) =>
    api().SaveSettings(provider, model, endpoint, apiKeyEnv),
  listOllamaModels: (endpoint = "") => api().ListOllamaModels(endpoint),
  // onAnswerChunk subscribes a callback to incoming token deltas. Returns an
  // unsubscribe function. Safe to call before or after Ask() — Wails buffers
  // events until a listener is registered.
  onAnswerChunk: (cb: (delta: string) => void): (() => void) => {
    const r = rt();
    if (!r) return () => {};
    return r.EventsOn(AnswerEvents.Chunk, (delta: string) => cb(delta));
  },
  onAnswerEnd: (cb: () => void): (() => void) => {
    const r = rt();
    if (!r) return () => {};
    return r.EventsOn(AnswerEvents.End, () => cb());
  },
};
