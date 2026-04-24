<script lang="ts">
  import { Loom, type LintReport } from "./loom";

  let report: LintReport | null = null;
  let loading = false;
  let error = "";

  async function run() {
    loading = true;
    error = "";
    try {
      report = await Loom.lint();
    } catch (e: any) {
      error = e?.message ?? String(e);
    } finally {
      loading = false;
    }
  }
</script>

<section class="lint">
  <div class="header">
    <h2>Lint</h2>
    <button on:click={run} disabled={loading}>
      {loading ? "running…" : report ? "re-run" : "run lint"}
    </button>
  </div>

  {#if error}
    <p class="error">{error}</p>
  {/if}

  {#if report}
    <div class="stats">
      <div><strong>{report.stats.Notes}</strong> notes</div>
      <div><strong>{report.stats.Sources}</strong> sources</div>
      <div><strong>{report.stats.Entities}</strong> entities</div>
      <div class="warn"><strong>{report.stats.OrphanNotes}</strong> orphans</div>
      <div class="warn"><strong>{report.stats.Duplicates}</strong> duplicates</div>
      <div class="warn"><strong>{report.stats.Gaps}</strong> gaps</div>
    </div>

    {#if report.findings.length === 0}
      <p class="empty">no findings — your knowledge base is tidy.</p>
    {:else}
      <ul class="findings">
        {#each report.findings as f}
          <li class="severity-{f.Severity}">
            <span class="badge">{f.Kind}</span>
            <div class="body">
              <div class="subject">{f.Subject}</div>
              <div class="msg">{f.Message}</div>
            </div>
          </li>
        {/each}
      </ul>
    {/if}
  {:else if !loading && !error}
    <p class="empty">click <em>run lint</em> to inspect the knowledge base for orphans, near-duplicates, and source gaps.</p>
  {/if}
</section>

<style>
  .lint {
    padding: 1.5rem 2rem;
    max-width: 900px;
    margin: 0 auto;
  }
  .header {
    display: flex;
    align-items: baseline;
    justify-content: space-between;
  }
  .header h2 { margin: 0; }
  button {
    padding: 0.4rem 0.9rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: white;
    cursor: pointer;
    font: inherit;
  }
  button:hover { background: var(--accent-soft); }
  .error { color: #b04a4a; }
  .empty { color: var(--muted); }
  .stats {
    display: flex;
    flex-wrap: wrap;
    gap: 1rem 1.5rem;
    margin: 1.5rem 0;
    padding: 1rem 1.25rem;
    background: var(--panel-2);
    border: 1px solid var(--border);
    border-radius: 8px;
  }
  .stats div { font-size: 0.9rem; color: var(--muted); }
  .stats div strong {
    display: block;
    font-size: 1.4rem;
    color: var(--text);
  }
  .stats .warn strong { color: #b87a25; }

  ul.findings { list-style: none; margin: 0; padding: 0; display: flex; flex-direction: column; gap: 0.5rem; }
  ul.findings li {
    display: flex;
    align-items: flex-start;
    gap: 0.75rem;
    padding: 0.7rem 1rem;
    background: var(--panel-2);
    border: 1px solid var(--border);
    border-radius: 6px;
  }
  li.severity-warning { border-left: 3px solid #b87a25; }
  li.severity-info { border-left: 3px solid #5a7d9a; }
  .badge {
    background: #ddd;
    color: #555;
    padding: 1px 8px;
    border-radius: 99px;
    font-size: 0.72rem;
    text-transform: uppercase;
    letter-spacing: 0.04em;
    flex-shrink: 0;
  }
  .subject { font-family: var(--mono); font-size: 0.86rem; }
  .msg { color: var(--muted); font-size: 0.86rem; margin-top: 2px; }
</style>
