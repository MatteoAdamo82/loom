<script lang="ts">
  import type { FileVM } from "./loom";

  export let files: FileVM[] = [];
  export let selected: string | null = null;
  export let busy = false;
  export let scanLabel = "";
  export let onSelect: (relPath: string) => void;
  export let onRescan: () => void;
  export let onOpenFolder: () => void;
  export let onSettings: () => void;

  let filter = "";
  $: filtered = filter
    ? files.filter((f) => {
        const q = filter.toLowerCase();
        return (
          f.rel_path.toLowerCase().includes(q) ||
          f.title.toLowerCase().includes(q) ||
          f.keywords.some((k) => k.toLowerCase().includes(q))
        );
      })
    : files;

  function kindIcon(kind: string) {
    switch (kind) {
      case "md": case "markdown": return "M";
      case "pdf": return "P";
      case "html": case "htm": return "H";
      case "txt": case "text": return "T";
      default: return "·";
    }
  }
</script>

<aside>
  <header>
    <div class="title-row">
      <h2>Notes</h2>
      <button class="icon" title="Settings" on:click={onSettings} aria-label="Settings">⚙</button>
    </div>
    <input
      type="search"
      placeholder="filter…"
      bind:value={filter}
      class="filter"
    />
    <div class="actions">
      <button on:click={onRescan} disabled={busy} class="primary">
        {busy ? "scanning…" : "Rescan"}
      </button>
      <button on:click={onOpenFolder} title="Open notes folder">📁</button>
    </div>
    {#if scanLabel}
      <div class="scan-label">{scanLabel}</div>
    {/if}
  </header>

  <div class="list">
    {#if filtered.length === 0}
      <div class="empty">
        {#if files.length === 0}
          <p>Nessun file indicizzato.</p>
          <p class="dim">Trascina i tuoi file (md, pdf, html, txt) nella cartella notes e premi <strong>Rescan</strong>.</p>
        {:else}
          <p class="dim">Nessun risultato per «{filter}».</p>
        {/if}
      </div>
    {/if}
    {#each filtered as f (f.rel_path)}
      <button
        class="row"
        class:selected={selected === f.rel_path}
        on:click={() => onSelect(f.rel_path)}
      >
        <span class="kind">{kindIcon(f.kind)}</span>
        <span class="meta">
          <span class="t">{f.title || f.rel_path}</span>
          <span class="p">{f.rel_path}</span>
          {#if f.keywords?.length}
            <span class="kw">
              {#each f.keywords.slice(0, 4) as k}<em>{k}</em>{/each}
            </span>
          {/if}
        </span>
      </button>
    {/each}
  </div>

  <footer>
    <span>{files.length} {files.length === 1 ? "file" : "files"}</span>
  </footer>
</aside>

<style>
  aside {
    display: flex;
    flex-direction: column;
    height: 100%;
    border-right: 1px solid var(--border);
    background: var(--panel);
    min-width: 0;
  }
  header {
    padding: 0.75rem 0.85rem 0.6rem;
    border-bottom: 1px solid var(--border-soft);
    display: flex;
    flex-direction: column;
    gap: 0.5rem;
  }
  .title-row {
    display: flex;
    align-items: center;
    justify-content: space-between;
  }
  h2 {
    margin: 0;
    font-size: 0.78rem;
    text-transform: uppercase;
    letter-spacing: 0.08em;
    color: var(--muted);
    font-weight: 600;
  }
  .filter {
    width: 100%;
    padding: 0.4rem 0.6rem;
    border-radius: 6px;
    border: 1px solid var(--border);
    background: white;
    font: inherit;
    font-size: 0.86rem;
    box-sizing: border-box;
  }
  .filter:focus {
    outline: 2px solid var(--accent-soft);
    border-color: var(--accent);
  }
  .actions {
    display: flex;
    gap: 0.4rem;
  }
  .actions button {
    padding: 0.35rem 0.7rem;
    border-radius: 6px;
    border: 1px solid var(--border);
    background: white;
    cursor: pointer;
    font: inherit;
    font-size: 0.84rem;
  }
  .actions button:hover { border-color: var(--accent); }
  .actions button[disabled] { opacity: 0.55; cursor: progress; }
  .actions .primary {
    flex: 1;
    background: var(--accent);
    color: white;
    border-color: var(--accent);
    font-weight: 500;
  }
  .actions .primary:hover { background: var(--accent-strong); }
  .icon {
    background: transparent;
    border: none;
    cursor: pointer;
    color: var(--muted);
    font-size: 1rem;
    padding: 0.15rem 0.3rem;
    border-radius: 4px;
  }
  .icon:hover { background: var(--border-soft); color: var(--text); }
  .scan-label {
    font-size: 0.72rem;
    color: var(--muted);
    font-family: var(--mono);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }

  .list {
    flex: 1;
    overflow-y: auto;
    padding: 0.4rem 0.4rem;
  }
  .empty {
    padding: 2rem 1rem;
    text-align: center;
    color: var(--muted);
  }
  .empty p { margin: 0.4rem 0; font-size: 0.86rem; }

  .row {
    display: flex;
    width: 100%;
    align-items: flex-start;
    gap: 0.55rem;
    padding: 0.5rem 0.55rem;
    border: none;
    border-radius: 6px;
    background: transparent;
    text-align: left;
    cursor: pointer;
    font: inherit;
    color: inherit;
  }
  .row:hover { background: var(--panel-2); }
  .row.selected {
    background: var(--accent-soft);
  }
  .kind {
    flex-shrink: 0;
    width: 22px;
    height: 22px;
    border-radius: 4px;
    background: var(--border-soft);
    color: var(--muted);
    font-family: var(--mono);
    font-size: 0.7rem;
    display: grid;
    place-items: center;
    margin-top: 1px;
  }
  .row.selected .kind { background: var(--accent); color: white; }
  .meta {
    display: flex;
    flex-direction: column;
    min-width: 0;
    flex: 1;
  }
  .t {
    font-size: 0.88rem;
    font-weight: 500;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .p {
    font-size: 0.72rem;
    color: var(--muted);
    font-family: var(--mono);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .kw {
    margin-top: 0.25rem;
    display: flex;
    flex-wrap: wrap;
    gap: 0.25rem;
  }
  .kw em {
    font-style: normal;
    font-size: 0.68rem;
    padding: 1px 6px;
    border-radius: 999px;
    background: var(--panel-2);
    color: var(--muted);
    border: 1px solid var(--border-soft);
  }
  .row.selected .kw em { background: white; }

  footer {
    padding: 0.4rem 0.85rem;
    border-top: 1px solid var(--border-soft);
    font-size: 0.74rem;
    color: var(--muted);
    text-align: center;
  }
</style>
