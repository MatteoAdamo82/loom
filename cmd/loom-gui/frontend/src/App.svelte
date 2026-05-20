<script lang="ts">
  import { onMount } from "svelte";
  import { Loom, type FileVM, type Status } from "./lib/loom";
  import NoteList from "./lib/NoteList.svelte";
  import ChatPanel from "./lib/ChatPanel.svelte";
  import Settings from "./lib/Settings.svelte";
  import FileViewer from "./lib/FileViewer.svelte";

  let status: Status | null = null;
  let files: FileVM[] = [];
  let selectedFile: FileVM | null = null;
  let showSettings = false;
  let scanning = false;
  let scanLabel = "";
  let toast: { kind: "info" | "error"; text: string } | null = null;

  Loom.onScanProgress((p) => {
    if (p.phase === "summarize") {
      scanLabel = `${p.index}/${p.total} ${p.rel_path}`;
    } else if (p.phase === "delete") {
      scanLabel = `removed ${p.rel_path}`;
    }
  });

  Loom.onScanDone((res) => {
    scanning = false;
    scanLabel = "";
    const parts: string[] = [];
    if (res.added) parts.push(`${res.added} aggiunti`);
    if (res.updated) parts.push(`${res.updated} aggiornati`);
    if (res.removed) parts.push(`${res.removed} rimossi`);
    if (res.skipped) parts.push(`${res.skipped} invariati`);
    if (res.failed) parts.push(`${res.failed} falliti`);
    flash(res.failed ? "error" : "info", parts.join(", ") || "scan completato");
    refresh();
  });

  onMount(async () => {
    await refresh();
  });

  async function refresh() {
    try {
      status = await Loom.status();
      if (status.ok) {
        files = await Loom.listFiles();
      }
    } catch (e: any) {
      flash("error", e?.message ?? String(e));
    }
  }

  async function rescan() {
    if (scanning) return;
    scanning = true;
    scanLabel = "scanning…";
    try {
      await Loom.rescan(false);
    } catch (e: any) {
      scanning = false;
      flash("error", e?.message ?? String(e));
    }
  }

  function selectFile(relPath: string) {
    const f = files.find((x) => x.rel_path === relPath);
    if (f) selectedFile = f;
  }

  function openFolder() {
    Loom.openNotesDir().catch((e) => flash("error", e?.message ?? String(e)));
  }

  function flash(kind: "info" | "error", text: string) {
    toast = { kind, text };
    setTimeout(() => {
      if (toast?.text === text) toast = null;
    }, 3500);
  }
</script>

{#if !status}
  <div class="boot">connecting…</div>
{:else if !status.ok && !showSettings}
  <div class="boot error">
    <h2>Loom non è pronto</h2>
    <p>{status.error || "errore di avvio"}</p>
    {#if status.config_path}
      <p class="dim">config: <code>{status.config_path}</code></p>
    {/if}
    <div class="boot-actions">
      <button class="primary" on:click={() => (showSettings = true)}>Apri impostazioni</button>
      <button on:click={refresh}>Riprova</button>
    </div>
  </div>
{:else}
  <main>
    <NoteList
      {files}
      selected={selectedFile?.rel_path ?? null}
      busy={scanning}
      {scanLabel}
      onSelect={selectFile}
      onRescan={rescan}
      onOpenFolder={openFolder}
      onSettings={() => (showSettings = true)}
    />
    <section class="content">
      <ChatPanel onOpenFile={selectFile} />
    </section>
  </main>
{/if}

{#if selectedFile}
  <FileViewer file={selectedFile} onClose={() => (selectedFile = null)} />
{/if}

{#if showSettings}
  <Settings
    onClose={() => (showSettings = false)}
    onSaved={async (s) => {
      status = s;
      await refresh();
    }}
  />
{/if}

{#if toast}
  <div class="toast {toast.kind}">{toast.text}</div>
{/if}

<style>
  :global(:root) {
    --text: #1f2330;
    --muted: #777b88;
    --panel: #f6f5f1;
    --panel-2: #fafaf8;
    --border: #e2e0db;
    --border-soft: #ececea;
    --accent: #5a6fa5;
    --accent-strong: #3f5494;
    --accent-soft: #eceffa;
    --mono: ui-monospace, SFMono-Regular, "JetBrains Mono", Menlo, Consolas, monospace;
  }
  :global(body) {
    margin: 0;
    color: var(--text);
    background: var(--panel-2);
    font-family: -apple-system, BlinkMacSystemFont, "SF Pro Text", system-ui, sans-serif;
    font-size: 14px;
    height: 100vh;
    overflow: hidden;
  }
  :global(html), :global(body), :global(#app) {
    height: 100%;
  }
  :global(*) { box-sizing: border-box; }
  :global(#app) {
    display: flex;
    flex-direction: column;
  }

  main {
    flex: 1;
    display: grid;
    grid-template-columns: 320px 1fr;
    height: 100%;
    overflow: hidden;
  }
  .content {
    overflow: hidden;
    background: var(--panel-2);
    height: 100%;
  }

  .boot {
    flex: 1;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    color: var(--muted);
    padding: 3rem;
    text-align: center;
  }
  .boot.error h2 { color: #b04a4a; margin-bottom: 0.4rem; }
  .boot.error p { max-width: 600px; }
  .boot code {
    font-family: var(--mono);
    background: var(--panel);
    padding: 1px 6px;
    border-radius: 4px;
  }
  .boot-actions {
    display: flex;
    gap: 0.6rem;
    margin-top: 1rem;
  }
  .boot button {
    padding: 0.55rem 1.2rem;
    border-radius: 6px;
    border: 1px solid var(--border);
    background: white;
    cursor: pointer;
    font: inherit;
  }
  .boot button.primary {
    background: var(--accent);
    color: white;
    border-color: var(--accent);
  }

  .toast {
    position: fixed;
    bottom: 1rem;
    right: 1rem;
    padding: 0.6rem 1rem;
    border-radius: 8px;
    background: var(--text);
    color: white;
    box-shadow: 0 4px 14px rgba(0,0,0,0.18);
    max-width: 480px;
    font-size: 0.86rem;
    z-index: 100;
  }
  .toast.error { background: #8a3a3a; }
</style>
