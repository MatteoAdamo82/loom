<script lang="ts">
  import { onMount } from "svelte";
  import { Loom, type Settings, type Status } from "./loom";

  export let onClose: () => void;
  export let onSaved: (s: Status) => void;

  let cfg: Settings | null = null;
  let saving = false;
  let error = "";
  let ollamaModels: string[] = [];

  onMount(async () => {
    try {
      cfg = await Loom.settings();
      await refreshOllamaModels();
    } catch (e: any) {
      error = e?.message ?? String(e);
    }
  });

  async function refreshOllamaModels() {
    if (!cfg || cfg.LLM.Provider !== "ollama") return;
    try {
      ollamaModels = await Loom.listOllamaModels(cfg.LLM.Endpoint);
    } catch {
      ollamaModels = [];
    }
  }

  async function save() {
    if (!cfg) return;
    saving = true;
    error = "";
    try {
      const status = await Loom.saveSettings(cfg);
      if (status.error) {
        error = status.error;
        return;
      }
      onSaved(status);
      onClose();
    } catch (e: any) {
      error = e?.message ?? String(e);
    } finally {
      saving = false;
    }
  }
</script>

<div class="backdrop" on:click={onClose}>
  <div class="modal" on:click|stopPropagation>
    <header>
      <h2>Settings</h2>
      <button class="x" on:click={onClose} aria-label="Close">×</button>
    </header>

    {#if !cfg}
      <p class="dim">caricamento…</p>
    {:else}
      <div class="form">
        <label>
          <span>Notes folder</span>
          <input type="text" bind:value={cfg.NotesDir} />
          <small>Cartella dove Loom legge i tuoi file. Trascinaci dentro md/pdf/html/txt.</small>
        </label>

        <label>
          <span>Index database</span>
          <input type="text" bind:value={cfg.DBPath} />
          <small>SQLite. Cancellabile: si rigenera al prossimo scan.</small>
        </label>

        <fieldset>
          <legend>LLM</legend>
          <label>
            <span>Provider</span>
            <select bind:value={cfg.LLM.Provider} on:change={refreshOllamaModels}>
              <option value="ollama">Ollama (locale)</option>
              <option value="openai">OpenAI</option>
              <option value="anthropic">Anthropic</option>
            </select>
          </label>

          {#if cfg.LLM.Provider === "ollama"}
            <label>
              <span>Endpoint</span>
              <input type="text" bind:value={cfg.LLM.Endpoint} on:blur={refreshOllamaModels} />
            </label>
            <label>
              <span>Model</span>
              {#if ollamaModels.length > 0}
                <select bind:value={cfg.LLM.Model}>
                  {#each ollamaModels as m}<option value={m}>{m}</option>{/each}
                </select>
              {:else}
                <input type="text" bind:value={cfg.LLM.Model} placeholder="llama3.1:8b" />
                <small>Ollama non raggiungibile a {cfg.LLM.Endpoint}. Imposta manualmente il modello.</small>
              {/if}
            </label>
          {:else}
            <label>
              <span>Model</span>
              <input type="text" bind:value={cfg.LLM.Model} />
            </label>
            <label>
              <span>Endpoint (opzionale)</span>
              <input type="text" bind:value={cfg.LLM.Endpoint} placeholder="lascia vuoto per il default" />
            </label>
            <label>
              <span>API key env var</span>
              <input type="text" bind:value={cfg.LLM.APIKeyEnv} placeholder={cfg.LLM.Provider === "openai" ? "OPENAI_API_KEY" : "ANTHROPIC_API_KEY"} />
              <small>Nome della variabile d'ambiente che contiene la chiave. Loom non salva mai la chiave.</small>
            </label>
          {/if}
        </fieldset>
      </div>

      {#if error}
        <div class="error">{error}</div>
      {/if}

      <footer>
        <button on:click={onClose} disabled={saving}>Annulla</button>
        <button class="primary" on:click={save} disabled={saving}>
          {saving ? "salvataggio…" : "Salva"}
        </button>
      </footer>
    {/if}
  </div>
</div>

<style>
  .backdrop {
    position: fixed;
    inset: 0;
    background: rgba(20, 22, 30, 0.4);
    display: grid;
    place-items: center;
    z-index: 50;
    backdrop-filter: blur(2px);
  }
  .modal {
    background: white;
    border-radius: 12px;
    width: min(540px, 92vw);
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
    align-items: center;
  }
  header h2 { margin: 0; font-size: 1.05rem; font-weight: 600; }
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

  .form {
    padding: 1.25rem;
    overflow-y: auto;
  }
  label {
    display: block;
    margin-bottom: 1rem;
  }
  label > span {
    display: block;
    font-size: 0.8rem;
    color: var(--muted);
    margin-bottom: 0.3rem;
    font-weight: 500;
  }
  input[type="text"], select {
    width: 100%;
    padding: 0.5rem 0.7rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    font: inherit;
    font-size: 0.9rem;
    background: white;
    box-sizing: border-box;
  }
  input[type="text"]:focus, select:focus {
    outline: 2px solid var(--accent-soft);
    border-color: var(--accent);
  }
  small {
    display: block;
    font-size: 0.74rem;
    color: var(--muted);
    margin-top: 0.25rem;
  }
  fieldset {
    border: 1px solid var(--border-soft);
    border-radius: 8px;
    padding: 1rem 1rem 0.4rem;
    margin: 1rem 0 0;
  }
  legend {
    font-size: 0.78rem;
    color: var(--muted);
    text-transform: uppercase;
    letter-spacing: 0.06em;
    padding: 0 0.4rem;
  }

  .error {
    margin: 0 1.25rem 1rem;
    padding: 0.6rem 0.85rem;
    background: #fbe9e9;
    color: #8a3a3a;
    border-radius: 6px;
    font-size: 0.86rem;
  }

  footer {
    padding: 0.85rem 1.25rem;
    border-top: 1px solid var(--border);
    display: flex;
    justify-content: flex-end;
    gap: 0.5rem;
  }
  footer button {
    padding: 0.5rem 1rem;
    border-radius: 6px;
    border: 1px solid var(--border);
    background: white;
    cursor: pointer;
    font: inherit;
  }
  footer button:hover { border-color: var(--accent); }
  footer button[disabled] { opacity: 0.5; cursor: not-allowed; }
  footer .primary {
    background: var(--accent);
    border-color: var(--accent);
    color: white;
    font-weight: 500;
  }
  footer .primary:hover:not([disabled]) { background: var(--accent-strong); }
</style>
