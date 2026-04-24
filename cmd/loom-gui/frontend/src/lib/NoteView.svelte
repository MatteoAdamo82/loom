<script lang="ts">
  import { Loom, type NoteDetail } from "./loom";
  export let slug: string | null;
  export let onSelect: (slug: string) => void = () => {};

  let note: NoteDetail | null = null;
  let error = "";
  let loading = false;

  $: if (slug) load(slug);
  $: if (!slug) {
    note = null;
    error = "";
  }

  async function load(s: string) {
    loading = true;
    error = "";
    try {
      note = await Loom.getNote(s);
    } catch (e: any) {
      error = e?.message ?? String(e);
      note = null;
    } finally {
      loading = false;
    }
  }
</script>

{#if !slug}
  <section class="empty-state">
    <h2>Pick a note from the sidebar</h2>
    <p>Or use the chat panel to ask a question across the whole knowledge base.</p>
  </section>
{:else if loading}
  <section class="empty-state"><p>loading…</p></section>
{:else if error}
  <section class="empty-state error"><p>{error}</p></section>
{:else if note}
  <article class="note">
    <header>
      <h1>{note.title}</h1>
      <div class="meta">
        <span class="badge kind-{note.kind}">{note.kind}</span>
        <span class="slug">{note.slug}</span>
        <span class="version">v{note.version}</span>
        <span class="updated">updated {note.updated}</span>
      </div>
      {#if note.keywords?.length}
        <div class="keywords">
          {#each note.keywords as k}<span class="kw">#{k}</span>{/each}
        </div>
      {/if}
      {#if note.summary}
        <p class="summary">{note.summary}</p>
      {/if}
    </header>

    <pre class="content">{note.content}</pre>

    {#if note.links_out?.length || note.links_in?.length}
      <section class="links">
        {#if note.links_out?.length}
          <div class="links-block">
            <h3>links out ({note.links_out.length})</h3>
            <ul>
              {#each note.links_out as l}
                <li>
                  <span class="link-kind">{l.kind}</span>
                  {#if l.other_kind === "note" && l.other_slug}
                    {@const otherSlug = l.other_slug}
                    <button class="link" on:click={() => onSelect(otherSlug)}>
                      {l.other_title || l.other_slug}
                    </button>
                  {:else}
                    <span class="link other">{l.other_title || l.other_slug}</span>
                    {#if l.other_kind}
                      <span class="other-kind">{l.other_kind}</span>
                    {/if}
                  {/if}
                </li>
              {/each}
            </ul>
          </div>
        {/if}
        {#if note.links_in?.length}
          <div class="links-block">
            <h3>linked from ({note.links_in.length})</h3>
            <ul>
              {#each note.links_in as l}
                <li>
                  <span class="link-kind">{l.kind}</span>
                  {#if l.other_kind === "note" && l.other_slug}
                    {@const otherSlug = l.other_slug}
                    <button class="link" on:click={() => onSelect(otherSlug)}>
                      {l.other_title || l.other_slug}
                    </button>
                  {:else}
                    <span class="link other">{l.other_title || l.other_slug}</span>
                  {/if}
                </li>
              {/each}
            </ul>
          </div>
        {/if}
      </section>
    {/if}
  </article>
{/if}

<style>
  .empty-state {
    height: 100%;
    display: flex;
    flex-direction: column;
    align-items: center;
    justify-content: center;
    color: var(--muted);
    padding: 2rem;
    text-align: center;
  }
  .empty-state h2 {
    font-weight: 500;
    margin: 0 0 0.5rem;
  }
  .empty-state.error { color: #b04a4a; }

  .note {
    padding: 2rem 2.5rem;
    max-width: 900px;
    margin: 0 auto;
  }
  header h1 {
    margin: 0 0 0.5rem;
    font-size: 1.6rem;
  }
  .meta {
    display: flex;
    flex-wrap: wrap;
    gap: 0.5rem 1rem;
    align-items: baseline;
    font-size: 0.82rem;
    color: var(--muted);
  }
  .slug {
    font-family: var(--mono);
  }
  .badge {
    color: white;
    padding: 1px 8px;
    border-radius: 99px;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    background: var(--muted);
  }
  .kind-entity { background: #5a7d9a; }
  .kind-concept { background: #7a5a9a; }
  .kind-summary { background: #9a7a5a; }
  .kind-synthesis { background: #5a9a7a; }
  .kind-log { background: #888; }

  .keywords {
    margin-top: 0.6rem;
    display: flex;
    flex-wrap: wrap;
    gap: 0.3rem;
  }
  .kw {
    background: var(--accent-soft);
    color: var(--accent-strong);
    padding: 1px 8px;
    border-radius: 99px;
    font-size: 0.74rem;
    font-family: var(--mono);
  }

  .summary {
    margin: 1rem 0 0;
    color: #4a4a4a;
    font-style: italic;
    line-height: 1.5;
  }

  .content {
    margin-top: 1.5rem;
    padding: 1rem 1.25rem;
    background: var(--panel-2);
    border: 1px solid var(--border);
    border-radius: 8px;
    font-family: var(--mono);
    font-size: 0.88rem;
    line-height: 1.55;
    white-space: pre-wrap;
    word-break: break-word;
  }

  .links {
    margin-top: 2rem;
    display: grid;
    grid-template-columns: 1fr 1fr;
    gap: 1.5rem;
  }
  .links-block h3 {
    margin: 0 0 0.6rem;
    font-size: 0.82rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--muted);
  }
  .links-block ul {
    list-style: none;
    margin: 0;
    padding: 0;
    display: flex;
    flex-direction: column;
    gap: 0.35rem;
  }
  .links-block li {
    display: flex;
    align-items: baseline;
    gap: 0.5rem;
    font-size: 0.86rem;
  }
  .link-kind {
    font-size: 0.68rem;
    padding: 1px 6px;
    border-radius: 99px;
    background: #ddd;
    color: #555;
    flex-shrink: 0;
  }
  button.link {
    background: none;
    border: none;
    padding: 0;
    color: var(--accent);
    cursor: pointer;
    text-decoration: underline;
    text-decoration-style: dotted;
    text-underline-offset: 2px;
    font: inherit;
  }
  button.link:hover { color: var(--accent-strong); }
  .link.other {
    color: var(--text);
  }
  .other-kind {
    font-size: 0.68rem;
    color: var(--muted);
  }
  @media (max-width: 800px) {
    .links { grid-template-columns: 1fr; }
  }
</style>
