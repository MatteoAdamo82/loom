<script lang="ts">
  import { onMount } from "svelte";
  import { marked } from "marked";
  import DOMPurify from "dompurify";
  import { Loom, type FileVM } from "./loom";

  export let file: FileVM;
  export let onClose: () => void;

  let content = "";
  let loading = true;
  let error = "";

  onMount(async () => {
    try {
      content = await Loom.getFileContent(file.rel_path);
    } catch (e: any) {
      error = e?.message ?? String(e);
    } finally {
      loading = false;
    }
  });

  function renderMarkdown(src: string): string {
    if (!src) return "";
    const html = marked.parse(src, { gfm: true, breaks: true, async: false }) as string;
    return DOMPurify.sanitize(html, { USE_PROFILES: { html: true } });
  }
</script>

<div class="backdrop" on:click={onClose}>
  <div class="viewer" on:click|stopPropagation>
    <header>
      <div>
        <h2>{file.title || file.rel_path}</h2>
        <p class="path">{file.rel_path}</p>
      </div>
      <button class="x" on:click={onClose} aria-label="Close">×</button>
    </header>

    {#if file.summary}
      <aside class="summary">
        <strong>Riassunto:</strong> {file.summary}
        {#if file.keywords?.length}
          <div class="kw">
            {#each file.keywords as k}<em>{k}</em>{/each}
          </div>
        {/if}
      </aside>
    {/if}

    <div class="body">
      {#if loading}
        <p class="dim">caricamento…</p>
      {:else if error}
        <div class="err">{error}</div>
      {:else if file.kind === "md" || file.kind === "markdown"}
        <article class="md">{@html renderMarkdown(content)}</article>
      {:else}
        <pre>{content}</pre>
      {/if}
    </div>
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(20, 22, 30, 0.4);
    display: grid;
    place-items: center;
    z-index: 40;
    backdrop-filter: blur(2px);
  }
  .viewer {
    background: white;
    border-radius: 12px;
    width: min(880px, 92vw);
    max-height: 88vh;
    display: flex;
    flex-direction: column;
    box-shadow: 0 12px 40px rgba(0,0,0,0.25);
  }
  header {
    padding: 1rem 1.25rem;
    border-bottom: 1px solid var(--border);
    display: flex;
    justify-content: space-between;
    align-items: flex-start;
    gap: 1rem;
  }
  header h2 { margin: 0; font-size: 1.05rem; font-weight: 600; }
  .path {
    margin: 0.2rem 0 0;
    font-family: var(--mono);
    font-size: 0.78rem;
    color: var(--muted);
  }
  .x {
    background: transparent;
    border: none;
    font-size: 1.5rem;
    color: var(--muted);
    cursor: pointer;
    line-height: 1;
    padding: 0 0.3rem;
  }
  .x:hover { color: var(--text); }

  .summary {
    padding: 0.7rem 1.25rem;
    background: var(--panel);
    font-size: 0.86rem;
    border-bottom: 1px solid var(--border-soft);
  }
  .summary .kw {
    margin-top: 0.4rem;
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
  }
  .summary em {
    font-style: normal;
    font-size: 0.74rem;
    padding: 1px 7px;
    border-radius: 999px;
    background: white;
    color: var(--muted);
    border: 1px solid var(--border-soft);
  }

  .body {
    flex: 1;
    overflow-y: auto;
    padding: 1.25rem 1.5rem;
  }
  .md {
    line-height: 1.65;
    color: var(--text);
  }
  .md :global(h1) { font-size: 1.5rem; margin-top: 0; }
  .md :global(h2) { font-size: 1.2rem; margin-top: 1.5rem; }
  .md :global(h3) { font-size: 1.05rem; margin-top: 1.2rem; }
  .md :global(pre) {
    background: var(--panel);
    padding: 0.8rem 1rem;
    border-radius: 8px;
    overflow-x: auto;
    font-family: var(--mono);
    font-size: 0.84rem;
  }
  .md :global(code) {
    font-family: var(--mono);
    font-size: 0.86em;
    background: var(--panel);
    padding: 1px 5px;
    border-radius: 3px;
  }
  .md :global(pre code) { background: transparent; padding: 0; }
  .md :global(blockquote) {
    border-left: 3px solid var(--accent);
    padding-left: 1rem;
    margin-left: 0;
    color: var(--muted);
  }
  .md :global(table) {
    border-collapse: collapse;
    width: 100%;
  }
  .md :global(th), .md :global(td) {
    padding: 0.4rem 0.6rem;
    border: 1px solid var(--border-soft);
    text-align: left;
  }

  pre {
    font-family: var(--mono);
    font-size: 0.84rem;
    white-space: pre-wrap;
    word-wrap: break-word;
    margin: 0;
  }
  .err { color: #b04a4a; }
</style>
