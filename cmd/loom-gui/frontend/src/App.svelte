<script lang="ts">
  import { onMount } from "svelte";
  import { Loom, type NoteSummary, type Status } from "./lib/loom";
  import StatusBar from "./lib/StatusBar.svelte";
  import NoteList from "./lib/NoteList.svelte";
  import NoteView from "./lib/NoteView.svelte";
  import ChatPanel from "./lib/ChatPanel.svelte";
  import LintPanel from "./lib/LintPanel.svelte";

  let status: Status | null = null;
  let notes: NoteSummary[] = [];
  let selectedSlug: string | null = null;
  let tab: "note" | "chat" | "lint" = "chat";
  let ingesting = false;
  let toast: { kind: "info" | "error"; text: string } | null = null;

  onMount(async () => {
    await refreshStatus();
    await refreshNotes();
  });

  async function refreshStatus() {
    try {
      status = await Loom.status();
    } catch (e: any) {
      status = { ok: false, error: e?.message ?? String(e) };
    }
  }

  async function refreshNotes() {
    if (!status?.ok) return;
    try {
      notes = await Loom.listNotes();
    } catch (e: any) {
      flash("error", e?.message ?? String(e));
    }
  }

  async function reload() {
    status = await Loom.reload();
    await refreshNotes();
    flash("info", "config reloaded");
  }

  async function ingest() {
    if (!status?.ok || ingesting) return;
    ingesting = true;
    try {
      const res = await Loom.pickAndIngest();
      if (res === null) return; // user cancelled
      if (res.deduplicated) {
        flash("info", `already ingested (id=${res.source_id}, "${res.title}")`);
      } else {
        flash(
          "info",
          `ingested "${res.title}" — ${res.notes_created.length} notes, ${res.entities_linked} links`,
        );
      }
      await refreshNotes();
    } catch (e: any) {
      flash("error", e?.message ?? String(e));
    } finally {
      ingesting = false;
    }
  }

  function selectNote(slug: string) {
    selectedSlug = slug;
    tab = "note";
  }

  function flash(kind: "info" | "error", text: string) {
    toast = { kind, text };
    setTimeout(() => {
      if (toast?.text === text) toast = null;
    }, 4000);
  }
</script>

<StatusBar
  {status}
  busy={ingesting}
  {tab}
  onTab={(t) => (tab = t)}
  onIngest={ingest}
  onReload={reload}
/>

{#if !status}
  <div class="boot">connecting…</div>
{:else if !status.ok}
  <div class="boot error">
    <h2>Loom failed to start</h2>
    <p>{status.error}</p>
    {#if status.config_path}
      <p class="dim">config: <code>{status.config_path}</code></p>
    {/if}
    <button on:click={reload}>retry</button>
  </div>
{:else}
  <main>
    <NoteList
      {notes}
      selectedSlug={selectedSlug}
      onSelect={selectNote}
    />
    <section class="panel">
      {#if tab === "note"}
        <NoteView slug={selectedSlug} onSelect={selectNote} />
      {:else if tab === "chat"}
        <ChatPanel onSelect={selectNote} />
      {:else if tab === "lint"}
        <LintPanel />
      {/if}
    </section>
  </main>
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
  :global(#app) {
    display: flex;
    flex-direction: column;
  }

  main {
    flex: 1;
    display: grid;
    grid-template-columns: 320px 1fr;
    overflow: hidden;
  }
  .panel {
    overflow-y: auto;
    border-left: 1px solid var(--border);
    background: var(--panel-2);
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
  .boot.error h2 { color: #b04a4a; }
  .boot.error p { max-width: 600px; }
  .boot code { font-family: var(--mono); background: var(--panel); padding: 1px 6px; border-radius: 4px; }
  .boot button {
    margin-top: 1rem;
    padding: 0.5rem 1.25rem;
    border-radius: 6px;
    border: 1px solid var(--border);
    background: white;
    cursor: pointer;
    font: inherit;
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
  }
  .toast.error { background: #8a3a3a; }
</style>
