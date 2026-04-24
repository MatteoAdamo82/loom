<script lang="ts">
  import type { NoteSummary } from "./loom";
  export let notes: NoteSummary[] = [];
  export let selectedSlug: string | null = null;
  export let onSelect: (slug: string) => void = () => {};

  let kindFilter = "";
  let queryFilter = "";

  $: filtered = notes.filter((n) => {
    if (kindFilter && n.kind !== kindFilter) return false;
    if (queryFilter) {
      const q = queryFilter.toLowerCase();
      if (
        !n.title.toLowerCase().includes(q) &&
        !n.slug.toLowerCase().includes(q) &&
        !n.summary.toLowerCase().includes(q) &&
        !(n.keywords ?? []).some((k) => k.toLowerCase().includes(q))
      ) {
        return false;
      }
    }
    return true;
  });

  $: kinds = Array.from(new Set(notes.map((n) => n.kind))).sort();
</script>

<aside class="note-list">
  <div class="filters">
    <input
      type="search"
      bind:value={queryFilter}
      placeholder="filter by title, slug, keyword…"
    />
    <select bind:value={kindFilter} aria-label="filter by kind">
      <option value="">all kinds</option>
      {#each kinds as k}
        <option value={k}>{k}</option>
      {/each}
    </select>
  </div>

  <div class="counts">
    {filtered.length} / {notes.length} notes
  </div>

  <ul>
    {#each filtered as n (n.slug)}
      <li class:selected={n.slug === selectedSlug}>
        <button on:click={() => onSelect(n.slug)}>
          <div class="row1">
            <span class="title">{n.title}</span>
            <span class="kind kind-{n.kind}">{n.kind}</span>
          </div>
          <div class="row2">
            <span class="slug">{n.slug}</span>
            <span class="version">v{n.version}</span>
          </div>
        </button>
      </li>
    {/each}
    {#if filtered.length === 0}
      <li class="empty">no notes match the filter</li>
    {/if}
  </ul>
</aside>

<style>
  .note-list {
    display: flex;
    flex-direction: column;
    height: 100%;
    overflow: hidden;
  }
  .filters {
    display: flex;
    gap: 0.5rem;
    padding: 0.75rem;
    border-bottom: 1px solid var(--border);
    background: var(--panel-2);
  }
  .filters input,
  .filters select {
    font: inherit;
    padding: 0.4rem 0.55rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: white;
  }
  .filters input {
    flex: 1;
    min-width: 0;
  }
  .counts {
    padding: 0.4rem 0.85rem;
    font-size: 0.78rem;
    color: var(--muted);
    border-bottom: 1px solid var(--border);
    background: var(--panel-2);
  }
  ul {
    list-style: none;
    margin: 0;
    padding: 0;
    flex: 1;
    overflow-y: auto;
  }
  li.empty {
    padding: 1.5rem;
    text-align: center;
    color: var(--muted);
    font-size: 0.85rem;
  }
  li button {
    width: 100%;
    text-align: left;
    background: none;
    border: none;
    border-bottom: 1px solid var(--border-soft);
    padding: 0.65rem 0.85rem;
    cursor: pointer;
    font: inherit;
    color: inherit;
  }
  li button:hover {
    background: var(--accent-soft);
  }
  li.selected button {
    background: var(--accent-soft);
    border-left: 3px solid var(--accent);
    padding-left: calc(0.85rem - 3px);
  }
  .row1 {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
    gap: 0.5rem;
  }
  .title {
    font-weight: 600;
    font-size: 0.92rem;
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
  .kind {
    font-size: 0.7rem;
    padding: 1px 6px;
    border-radius: 99px;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    color: white;
    background: var(--muted);
    flex-shrink: 0;
  }
  .kind-entity { background: #5a7d9a; }
  .kind-concept { background: #7a5a9a; }
  .kind-summary { background: #9a7a5a; }
  .kind-synthesis { background: #5a9a7a; }
  .kind-log { background: #888; }
  .row2 {
    display: flex;
    justify-content: space-between;
    margin-top: 0.2rem;
    font-size: 0.74rem;
    color: var(--muted);
  }
  .slug {
    font-family: var(--mono);
    overflow: hidden;
    text-overflow: ellipsis;
    white-space: nowrap;
  }
</style>
