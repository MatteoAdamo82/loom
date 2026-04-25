<script lang="ts">
  import { onMount, onDestroy } from "svelte";
  import { Loom, type Answer, type AnswerFormat } from "./loom";
  export let onSelect: (slug: string) => void = () => {};

  type Turn = {
    question: string;
    answer: string;     // accumulates streamed deltas while pending
    expanded?: string[];
    citations?: { entity_ref: string; title: string; slug?: string }[];
    error?: string;
    pending?: boolean;
    format: AnswerFormat;
  };

  let turns: Turn[] = [];
  let input = "";
  let busy = false;
  let format: AnswerFormat = "markdown";

  // Wired in onMount so the chunk callback always has a stable reference to
  // the in-flight turn. We rely on the fact that the UI gates Ask() with
  // `busy = true`, so only the last entry of `turns` is ever pending.
  let unsubChunk = () => {};
  let unsubEnd = () => {};

  onMount(() => {
    unsubChunk = Loom.onAnswerChunk((delta) => {
      const t = turns[turns.length - 1];
      if (!t || !t.pending) return;
      t.answer += delta;
      turns = [...turns];
    });
    unsubEnd = Loom.onAnswerEnd(() => {
      // No-op for now — the `Ask` Promise resolution finalises citations.
      // Kept as an explicit hook so a future "stop generating" button has
      // something to listen to.
    });
  });

  onDestroy(() => {
    unsubChunk();
    unsubEnd();
  });

  async function send() {
    const q = input.trim();
    if (!q || busy) return;
    busy = true;
    input = "";
    const turn: Turn = { question: q, answer: "", pending: true, format };
    turns = [...turns, turn];
    try {
      const ans: Answer = await Loom.ask(q, format);
      Object.assign(turn, {
        answer: ans.answer || turn.answer,
        expanded: ans.expanded,
        citations: ans.citations,
        pending: false,
      });
    } catch (e: any) {
      turn.error = e?.message ?? String(e);
      turn.pending = false;
    } finally {
      turns = [...turns];
      busy = false;
    }
  }

  function onKey(e: KeyboardEvent) {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      send();
    }
  }

  function formatLabel(f: AnswerFormat): string {
    switch (f) {
      case "marp": return "Marp slides";
      case "text": return "plain text";
      default:     return "markdown";
    }
  }
</script>

<section class="chat">
  <div class="log">
    {#if turns.length === 0}
      <div class="empty">
        <h2>Ask anything</h2>
        <p>Loom expands your question into BM25 keywords, ranks the matches, and grounds the answer in your notes. Press <kbd>⌘ Enter</kbd> to send.</p>
      </div>
    {/if}

    {#each turns as t, idx (idx)}
      <div class="turn">
        <div class="q">
          <span class="role">you</span>
          <p>{t.question}</p>
        </div>
        <div class="a" class:err={t.error}>
          <span class="role">loom</span>
          {#if t.error}
            <p>{t.error}</p>
          {:else}
            {#if t.pending && !t.answer}
              <p class="pending">thinking…</p>
            {:else}
              <p class="answer" class:streaming={t.pending}>{t.answer}</p>
            {/if}
            {#if !t.pending}
              {#if t.citations?.length}
                <div class="citations">
                  {#each t.citations as c}
                    {#if c.slug}
                      {@const slug = c.slug}
                      <button class="cite" on:click={() => onSelect(slug)}>
                        {c.title} <span class="ref">{c.entity_ref}</span>
                      </button>
                    {:else}
                      <span class="cite static">
                        {c.title} <span class="ref">{c.entity_ref}</span>
                      </span>
                    {/if}
                  {/each}
                </div>
              {/if}
              {#if t.expanded?.length || t.format !== "markdown"}
                <details class="expanded">
                  <summary>
                    {t.expanded?.length ?? 0} expanded queries
                    {#if t.format !== "markdown"}· format: {formatLabel(t.format)}{/if}
                  </summary>
                  {#if t.expanded?.length}
                    <ul>
                      {#each t.expanded as q}<li>{q}</li>{/each}
                    </ul>
                  {/if}
                </details>
              {/if}
            {/if}
          {/if}
        </div>
      </div>
    {/each}
  </div>

  <div class="composer">
    <textarea
      bind:value={input}
      on:keydown={onKey}
      placeholder="ask the knowledge base — ⌘ Enter to send"
      rows="3"
      disabled={busy}
    ></textarea>
    <div class="controls">
      <label class="format">
        <span>format</span>
        <select bind:value={format} disabled={busy}>
          <option value="markdown">markdown</option>
          <option value="marp">Marp slides</option>
          <option value="text">plain text</option>
        </select>
      </label>
      <button on:click={send} disabled={busy || !input.trim()}>
        {busy ? "asking…" : "send"}
      </button>
    </div>
  </div>
</section>

<style>
  .chat {
    display: flex;
    flex-direction: column;
    height: 100%;
  }
  .log {
    flex: 1;
    overflow-y: auto;
    padding: 1.5rem 2rem;
    display: flex;
    flex-direction: column;
    gap: 1.5rem;
  }
  .empty {
    margin: auto;
    text-align: center;
    color: var(--muted);
    max-width: 480px;
  }
  .empty h2 {
    font-weight: 500;
    margin-bottom: 0.5rem;
  }
  kbd {
    background: var(--panel-2);
    padding: 1px 6px;
    border-radius: 4px;
    border: 1px solid var(--border);
    font-family: var(--mono);
    font-size: 0.85em;
  }

  .turn {
    display: flex;
    flex-direction: column;
    gap: 0.6rem;
  }
  .q, .a {
    display: flex;
    gap: 0.75rem;
  }
  .role {
    flex-shrink: 0;
    width: 3rem;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--muted);
    padding-top: 0.2rem;
  }
  .a .role { color: var(--accent); }
  .q p, .a p {
    margin: 0;
    line-height: 1.55;
  }
  .answer {
    white-space: pre-wrap;
  }
  /* Caret pulse while tokens are still arriving — quick visual confirmation. */
  .answer.streaming::after {
    content: "▍";
    color: var(--accent);
    margin-left: 2px;
    animation: blink 1s steps(1) infinite;
  }
  @keyframes blink { 50% { opacity: 0; } }
  .pending {
    color: var(--muted);
    font-style: italic;
  }
  .err p { color: #b04a4a; }

  .citations {
    margin-top: 0.6rem;
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
  }
  .cite {
    background: var(--accent-soft);
    color: var(--accent-strong);
    border: none;
    padding: 3px 9px;
    border-radius: 99px;
    font-size: 0.78rem;
    cursor: pointer;
    font: inherit;
  }
  .cite.static { cursor: default; opacity: 0.7; }
  .cite:not(.static):hover { background: var(--accent); color: white; }
  .ref {
    font-family: var(--mono);
    font-size: 0.72em;
    opacity: 0.8;
    margin-left: 4px;
  }

  .expanded {
    margin-top: 0.5rem;
    font-size: 0.78rem;
    color: var(--muted);
  }
  .expanded summary { cursor: pointer; }
  .expanded ul { margin: 0.3rem 0 0 1rem; padding: 0; }

  .composer {
    border-top: 1px solid var(--border);
    padding: 0.75rem 1rem;
    display: flex;
    gap: 0.6rem;
    background: var(--panel-2);
    align-items: stretch;
  }
  textarea {
    flex: 1;
    font: inherit;
    border: 1px solid var(--border);
    border-radius: 6px;
    padding: 0.55rem 0.7rem;
    resize: none;
    background: white;
  }
  textarea:focus {
    outline: none;
    border-color: var(--accent);
  }
  .controls {
    display: flex;
    flex-direction: column;
    gap: 0.4rem;
    justify-content: flex-end;
  }
  .format {
    display: flex;
    flex-direction: column;
    font-size: 0.7rem;
    color: var(--muted);
    text-transform: uppercase;
    letter-spacing: 0.05em;
  }
  .format select {
    margin-top: 2px;
    padding: 0.25rem 0.4rem;
    font: inherit;
    border-radius: 4px;
    border: 1px solid var(--border);
    background: white;
  }
  button {
    padding: 0.5rem 1rem;
    border-radius: 6px;
    border: none;
    background: var(--accent);
    color: white;
    font: inherit;
    cursor: pointer;
  }
  button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
</style>
