<script lang="ts">
  import type { Status } from "./loom";
  export let status: Status | null = null;
  export let busy = false;
  export let onIngest: () => void = () => {};
  export let onReload: () => void = () => {};

  type Tab = "note" | "chat" | "lint" | "settings";
  export let tab: Tab = "chat";
  export let onTab: (t: Tab) => void = () => {};
</script>

<header class="bar">
  <div class="brand">
    <span class="dot" class:ok={status?.ok} class:bad={status && !status.ok}></span>
    <strong>Loom</strong>
    {#if status?.llm_name}
      <span class="meta">{status.llm_name}</span>
    {/if}
  </div>

  <nav class="tabs">
    <button class:active={tab === "note"} on:click={() => onTab("note")}>Note</button>
    <button class:active={tab === "chat"} on:click={() => onTab("chat")}>Chat</button>
    <button class:active={tab === "lint"} on:click={() => onTab("lint")}>Lint</button>
  </nav>

  <div class="actions">
    <button on:click={onIngest} disabled={busy} title="ingest a file">
      {busy ? "ingesting…" : "+ ingest"}
    </button>
    <button on:click={onReload} title="reload config + reopen DB">reload</button>
    <button
      class="icon"
      class:active={tab === "settings"}
      on:click={() => onTab("settings")}
      title="settings"
      aria-label="settings">⚙</button>
  </div>
</header>

<style>
  .bar {
    display: flex;
    align-items: center;
    justify-content: space-between;
    padding: 0.5rem 1rem;
    background: var(--panel);
    border-bottom: 1px solid var(--border);
    -webkit-app-region: drag;
    user-select: none;
  }
  .bar button { -webkit-app-region: no-drag; }
  .brand {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    padding-left: 4.5rem; /* leave room for the macOS traffic lights */
  }
  .dot {
    width: 8px;
    height: 8px;
    border-radius: 50%;
    background: #888;
    align-self: center;
  }
  .dot.ok { background: #4a9a5a; }
  .dot.bad { background: #b04a4a; }
  .meta {
    color: var(--muted);
    font-size: 0.78rem;
    font-family: var(--mono);
  }

  .tabs {
    display: flex;
    gap: 0.25rem;
    background: var(--panel-2);
    border-radius: 8px;
    padding: 3px;
  }
  .tabs button {
    background: none;
    border: none;
    padding: 0.35rem 0.95rem;
    border-radius: 6px;
    cursor: pointer;
    font: inherit;
    color: var(--muted);
  }
  .tabs button.active {
    background: white;
    color: var(--text);
    box-shadow: 0 1px 3px rgba(0,0,0,0.08);
  }

  .actions {
    display: flex;
    gap: 0.4rem;
  }
  .actions button {
    padding: 0.35rem 0.9rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: white;
    cursor: pointer;
    font: inherit;
  }
  .actions button:hover { background: var(--accent-soft); }
  .actions button:disabled { opacity: 0.5; cursor: not-allowed; }
  .actions button.icon {
    padding: 0.35rem 0.55rem;
    font-size: 1rem;
    line-height: 1;
  }
  .actions button.icon.active {
    background: var(--accent);
    color: white;
    border-color: var(--accent);
  }
</style>
