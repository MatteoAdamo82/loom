<script lang="ts">
  import { tick } from "svelte";
  import { marked } from "marked";
  import DOMPurify from "dompurify";
  import { Loom, type Citation } from "./loom";

  export let onOpenFile: (relPath: string) => void;

  type Turn = {
    id: number;
    question: string;
    answer: string; // streaming buffer, becomes final on done
    citations: Citation[];
    used: Citation[];
    busy: boolean;
    error?: string;
  };

  let turns: Turn[] = [];
  let input = "";
  let textarea: HTMLTextAreaElement;
  let scrollEl: HTMLDivElement;
  let nextID = 1;

  // Listen for streaming token deltas. The Go side emits "ask:chunk" with the
  // raw delta string. We append it to the latest in-flight turn.
  Loom.onAskChunk((chunk) => {
    if (turns.length === 0) return;
    const last = turns[turns.length - 1];
    if (!last.busy) return;
    last.answer += chunk;
    turns = turns;
    queueScroll();
  });

  async function send() {
    const q = input.trim();
    if (!q || isAnyBusy()) return;
    input = "";
    autosize();

    const turn: Turn = {
      id: nextID++,
      question: q,
      answer: "",
      citations: [],
      used: [],
      busy: true,
    };
    turns = [...turns, turn];
    queueScroll();

    try {
      const res = await Loom.ask(q);
      // Trust the final assembled answer over the streamed buffer.
      turn.answer = res.answer;
      turn.citations = res.citations || [];
      turn.used = res.used || [];
    } catch (e: any) {
      turn.error = e?.message ?? String(e);
    } finally {
      turn.busy = false;
      turns = turns;
      queueScroll();
    }
  }

  function isAnyBusy() {
    return turns.some((t) => t.busy);
  }

  function renderMarkdown(src: string): string {
    if (!src) return "";
    const html = marked.parse(src, { gfm: true, breaks: true, async: false }) as string;
    return DOMPurify.sanitize(html, { USE_PROFILES: { html: true } });
  }

  function onKeydown(e: KeyboardEvent) {
    if (e.key === "Enter" && (e.metaKey || e.ctrlKey)) {
      e.preventDefault();
      send();
    } else if (e.key === "Enter" && !e.shiftKey) {
      e.preventDefault();
      send();
    }
  }

  function autosize() {
    if (!textarea) return;
    textarea.style.height = "auto";
    textarea.style.height = Math.min(textarea.scrollHeight, 200) + "px";
  }

  async function queueScroll() {
    await tick();
    if (scrollEl) scrollEl.scrollTop = scrollEl.scrollHeight;
  }

  function citationClick(c: Citation) {
    onOpenFile(c.rel_path);
  }

  // Intercept clicks on rendered citation tokens so [foo.md] in the markdown
  // body opens the file. The model writes citations as plain text, so we
  // post-process the HTML to wrap them in clickable buttons.
  function withCitationLinks(html: string): string {
    return html.replace(
      /\[([a-zA-Z0-9_\-./ ]+\.[a-zA-Z0-9]+)\]/g,
      (_, p) => `<button class="cite" data-rel="${escapeAttr(p)}">[${escapeHtml(p)}]</button>`,
    );
  }
  function escapeAttr(s: string) { return s.replace(/"/g, "&quot;"); }
  function escapeHtml(s: string) {
    return s.replace(/[&<>]/g, (c) => ({ "&": "&amp;", "<": "&lt;", ">": "&gt;" }[c]!));
  }

  function onAnswerClick(e: MouseEvent) {
    const target = e.target as HTMLElement;
    if (target?.tagName === "BUTTON" && target.classList.contains("cite")) {
      const rel = target.getAttribute("data-rel");
      if (rel) onOpenFile(rel);
    }
  }
</script>

<section class="chat">
  <div class="scroll" bind:this={scrollEl}>
    {#if turns.length === 0}
      <div class="welcome">
        <h2>Chiedi qualcosa alle tue note</h2>
        <p class="dim">Loom risponde solo usando i file che hai indicizzato.</p>
        <ul class="examples">
          <li>«riassumi quello che so su X»</li>
          <li>«cosa ho scritto sul progetto Y?»</li>
          <li>«trova le ricette con il guanciale»</li>
        </ul>
      </div>
    {/if}

    {#each turns as t (t.id)}
      <article class="turn">
        <div class="q">{t.question}</div>
        <div class="a" on:click={onAnswerClick} role="presentation">
          {#if t.error}
            <div class="err">errore: {t.error}</div>
          {:else if t.busy && !t.answer}
            <div class="thinking">
              <span></span><span></span><span></span>
            </div>
          {:else}
            {@html withCitationLinks(renderMarkdown(t.answer))}
          {/if}
        </div>
        {#if !t.busy && t.citations.length > 0}
          <div class="cites">
            <span class="cites-label">citazioni:</span>
            {#each t.citations as c}
              <button class="cite-pill" on:click={() => citationClick(c)}>
                {c.rel_path}
              </button>
            {/each}
          </div>
        {/if}
      </article>
    {/each}
  </div>

  <form class="composer" on:submit|preventDefault={send}>
    <textarea
      bind:this={textarea}
      bind:value={input}
      on:input={autosize}
      on:keydown={onKeydown}
      placeholder="Scrivi una domanda… (Invio per inviare, Shift+Invio per a capo)"
      rows="1"
    ></textarea>
    <button type="submit" class="send" disabled={isAnyBusy() || !input.trim()}>
      {isAnyBusy() ? "…" : "Invia"}
    </button>
  </form>
</section>

<style>
  .chat {
    display: flex;
    flex-direction: column;
    height: 100%;
  }
  .scroll {
    flex: 1;
    overflow-y: auto;
    padding: 1.5rem 2rem;
    scroll-behavior: smooth;
  }
  .welcome {
    max-width: 540px;
    margin: 4rem auto;
    text-align: center;
    color: var(--muted);
  }
  .welcome h2 {
    color: var(--text);
    font-size: 1.4rem;
    font-weight: 500;
    margin: 0 0 0.5rem;
  }
  .welcome .dim { font-size: 0.92rem; }
  .examples {
    list-style: none;
    padding: 0;
    margin: 1.5rem auto 0;
    text-align: left;
    max-width: 360px;
  }
  .examples li {
    padding: 0.55rem 0.8rem;
    margin: 0.4rem 0;
    background: var(--panel);
    border: 1px solid var(--border-soft);
    border-radius: 8px;
    font-size: 0.86rem;
  }

  .turn {
    max-width: 800px;
    margin: 0 auto 2rem;
  }
  .q {
    background: var(--accent-soft);
    border-radius: 12px 12px 4px 12px;
    padding: 0.7rem 1rem;
    margin-left: 25%;
    margin-bottom: 0.6rem;
    white-space: pre-wrap;
    color: var(--accent-strong);
    font-weight: 500;
  }
  .a {
    padding: 0.4rem 0;
    line-height: 1.6;
  }
  .a :global(p:first-child) { margin-top: 0; }
  .a :global(p:last-child) { margin-bottom: 0; }
  .a :global(pre) {
    background: var(--panel);
    padding: 0.7rem 1rem;
    border-radius: 8px;
    overflow-x: auto;
    font-family: var(--mono);
    font-size: 0.84rem;
  }
  .a :global(code) {
    font-family: var(--mono);
    font-size: 0.86em;
    background: var(--panel);
    padding: 1px 5px;
    border-radius: 3px;
  }
  .a :global(pre code) { background: transparent; padding: 0; }
  .a :global(.cite) {
    display: inline;
    border: none;
    background: var(--panel);
    color: var(--accent-strong);
    font: inherit;
    font-size: 0.86em;
    padding: 1px 5px;
    border-radius: 3px;
    cursor: pointer;
    font-family: var(--mono);
  }
  .a :global(.cite:hover) {
    background: var(--accent-soft);
  }
  .err {
    color: #b04a4a;
    font-size: 0.9rem;
  }
  .thinking {
    display: inline-flex;
    gap: 4px;
    padding: 0.5rem 0;
  }
  .thinking span {
    width: 6px;
    height: 6px;
    background: var(--muted);
    border-radius: 50%;
    animation: pulse 1.2s infinite;
  }
  .thinking span:nth-child(2) { animation-delay: 0.2s; }
  .thinking span:nth-child(3) { animation-delay: 0.4s; }
  @keyframes pulse {
    0%, 80%, 100% { opacity: 0.3; transform: scale(0.8); }
    40% { opacity: 1; transform: scale(1); }
  }

  .cites {
    display: flex;
    flex-wrap: wrap;
    gap: 0.4rem;
    align-items: center;
    margin-top: 0.8rem;
    padding-top: 0.5rem;
    border-top: 1px dashed var(--border-soft);
  }
  .cites-label {
    font-size: 0.74rem;
    color: var(--muted);
    text-transform: uppercase;
    letter-spacing: 0.06em;
  }
  .cite-pill {
    font-family: var(--mono);
    font-size: 0.78rem;
    padding: 3px 9px;
    border-radius: 999px;
    background: var(--panel);
    border: 1px solid var(--border-soft);
    cursor: pointer;
    color: var(--accent-strong);
  }
  .cite-pill:hover { background: var(--accent-soft); border-color: var(--accent); }

  .composer {
    border-top: 1px solid var(--border);
    padding: 0.85rem 1rem;
    display: flex;
    gap: 0.6rem;
    align-items: flex-end;
    background: var(--panel-2);
  }
  textarea {
    flex: 1;
    resize: none;
    border: 1px solid var(--border);
    border-radius: 10px;
    padding: 0.6rem 0.85rem;
    font: inherit;
    font-size: 0.92rem;
    line-height: 1.4;
    background: white;
    min-height: 40px;
    max-height: 200px;
  }
  textarea:focus {
    outline: 2px solid var(--accent-soft);
    border-color: var(--accent);
  }
  .send {
    padding: 0.6rem 1.2rem;
    background: var(--accent);
    color: white;
    border: 1px solid var(--accent);
    border-radius: 10px;
    cursor: pointer;
    font: inherit;
    font-weight: 500;
  }
  .send:hover:not([disabled]) { background: var(--accent-strong); }
  .send[disabled] { opacity: 0.4; cursor: not-allowed; }
</style>
