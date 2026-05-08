// Typed wrapper around the Wails-generated bindings on window.go.main.App.

export type Status = {
  ok: boolean;
  error?: string;
  notes_dir?: string;
  db_path?: string;
  llm_name?: string;
  config_path?: string;
  file_count: number;
};

export type FileVM = {
  rel_path: string;
  title: string;
  kind: string;
  summary: string;
  keywords: string[];
  indexed_at: string;
};

export type Citation = {
  rel_path: string;
  title: string;
};

export type AskResult = {
  answer: string;
  citations: Citation[];
  used: Citation[];
};

export type ScanResult = {
  added: number;
  updated: number;
  removed: number;
  skipped: number;
  failed: number;
  errors?: string[];
  duration_ms: number;
};

export type ScanProgress = {
  phase: "scan" | "summarize" | "delete" | "done";
  rel_path: string;
  index: number;
  total: number;
  error: string;
};

export type Settings = {
  NotesDir: string;
  DBPath: string;
  LLM: {
    Provider: string;
    Model: string;
    Endpoint: string;
    APIKeyEnv: string;
  };
};

type Bindings = {
  Status(): Promise<Status>;
  Settings(): Promise<Settings>;
  SaveSettings(cfg: Settings): Promise<Status>;
  ListFiles(): Promise<FileVM[]>;
  GetFileContent(relPath: string): Promise<string>;
  Rescan(force: boolean): Promise<ScanResult>;
  CancelScan(): Promise<void>;
  Ask(question: string): Promise<AskResult>;
  OpenNotesDir(): Promise<void>;
  ListOllamaModels(endpoint: string): Promise<string[]>;
};

type WailsRuntime = {
  EventsOn(name: string, cb: (...args: any[]) => void): () => void;
  EventsOff(name: string, ...rest: any[]): void;
};

function api(): Bindings {
  const w = window as any;
  if (!w?.go?.main?.App) {
    throw new Error("Wails bindings not available — open via `wails dev` or the built binary.");
  }
  return w.go.main.App as Bindings;
}

function rt(): WailsRuntime | null {
  const w = window as any;
  return (w?.runtime as WailsRuntime) ?? null;
}

export const Loom = {
  status: () => api().Status(),
  settings: () => api().Settings(),
  saveSettings: (cfg: Settings) => api().SaveSettings(cfg),
  listFiles: () => api().ListFiles(),
  getFileContent: (relPath: string) => api().GetFileContent(relPath),
  rescan: (force = false) => api().Rescan(force),
  cancelScan: () => api().CancelScan(),
  ask: (question: string) => api().Ask(question),
  openNotesDir: () => api().OpenNotesDir(),
  listOllamaModels: (endpoint = "") => api().ListOllamaModels(endpoint),

  onScanProgress: (cb: (p: ScanProgress) => void): (() => void) => {
    const r = rt();
    if (!r) return () => {};
    return r.EventsOn("scan:progress", (p: any) => cb(p as ScanProgress));
  },
  onScanDone: (cb: (r: ScanResult) => void): (() => void) => {
    const r = rt();
    if (!r) return () => {};
    return r.EventsOn("scan:done", (res: any) => cb(res as ScanResult));
  },
  onAskChunk: (cb: (chunk: string) => void): (() => void) => {
    const r = rt();
    if (!r) return () => {};
    return r.EventsOn("ask:chunk", (chunk: any) => cb(String(chunk)));
  },
};
