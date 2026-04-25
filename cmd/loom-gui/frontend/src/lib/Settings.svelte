<script lang="ts">
  import { onMount } from "svelte";
  import { Loom, type Settings, type OllamaModel } from "./loom";

  export let onSaved: () => void = () => {};

  let loaded = false;
  let loading = false;
  let saving = false;
  let saveErr = "";
  let saveOk = "";
  let configPath = "";
  let bootError = "";

  let provider: "ollama" | "openai" | "anthropic" = "ollama";
  let model = "";
  let endpoint = "";
  let apiKeyEnv = "";

  let availableModels: OllamaModel[] = [];
  let modelsErr = "";

  onMount(async () => {
    loading = true;
    try {
      const s: Settings = await Loom.settings();
      configPath = s.config_path;
      bootError = s.error ?? "";
      provider = (s.provider as any) || "ollama";
      model = s.model;
      endpoint = s.endpoint;
      apiKeyEnv = s.api_key_env;
      if (provider === "ollama") await refreshModels();
    } catch (e: any) {
      saveErr = e?.message ?? String(e);
    } finally {
      loading = false;
      loaded = true;
    }
  });

  let modelsLoading = false;
  async function refreshModels() {
    if (provider !== "ollama") return;
    modelsErr = "";
    modelsLoading = true;
    try {
      const out = await Loom.listOllamaModels(endpoint);
      availableModels = out ?? [];
      if (availableModels.length === 0) {
        modelsErr = "ollama responded but reports no installed models — try `ollama pull <name>` first";
      }
    } catch (e: any) {
      availableModels = [];
      modelsErr = e?.message ?? String(e);
    } finally {
      modelsLoading = false;
    }
  }

  async function save() {
    if (saving) return;
    saving = true;
    saveErr = "";
    saveOk = "";
    try {
      const status = await Loom.saveSettings(provider, model, endpoint, apiKeyEnv);
      if (!status.ok) {
        saveErr = status.error ?? "save failed";
      } else {
        saveOk = `connected to ${status.llm_name}`;
        bootError = "";
      }
      onSaved();
    } catch (e: any) {
      saveErr = e?.message ?? String(e);
    } finally {
      saving = false;
    }
  }

  function presetEndpoint(): string {
    switch (provider) {
      case "ollama":    return "http://localhost:11434";
      case "openai":    return "https://api.openai.com";
      case "anthropic": return "https://api.anthropic.com";
    }
  }

  function placeholderModel(): string {
    switch (provider) {
      case "ollama":    return "e.g. qwen3.5:9b or gpt-oss:20b-cloud";
      case "openai":    return "e.g. gpt-4o-mini";
      case "anthropic": return "e.g. claude-sonnet-4-6";
    }
  }
</script>

<section class="settings">
  <header>
    <h2>Settings</h2>
    {#if configPath}
      <p class="dim">config: <code>{configPath}</code></p>
    {/if}
  </header>

  {#if !loaded || loading}
    <p class="dim">loading…</p>
  {:else}
    {#if bootError}
      <div class="banner error">
        <strong>Engine couldn't start:</strong> {bootError}
      </div>
    {/if}

    <fieldset disabled={saving}>
      <label>
        <span>provider</span>
        <div class="radios">
          {#each ["ollama", "openai", "anthropic"] as p}
            <label class="radio">
              <input type="radio" bind:group={provider} value={p}
                on:change={() => { endpoint = endpoint || presetEndpoint(); if (p === "ollama") refreshModels(); }} />
              {p}
            </label>
          {/each}
        </div>
      </label>

      <label>
        <span>model</span>
        {#if provider === "ollama"}
          <div class="row">
            <select bind:value={model} class="grow" disabled={availableModels.length === 0}>
              {#if availableModels.length === 0}
                <option value="">{modelsLoading ? "loading…" : "(no models loaded)"}</option>
              {:else}
                {#each availableModels as m}
                  <option value={m.name}>{m.name}</option>
                {/each}
              {/if}
            </select>
            <input type="text" bind:value={model} placeholder={placeholderModel()} />
            <button type="button" on:click={refreshModels} title="re-query /api/tags" disabled={modelsLoading}>↻</button>
          </div>
          <small class="dim">pick from your Ollama tags or type a model name manually (cloud-suffix models work too)</small>
          {#if modelsErr}
            <small class="warn">{modelsErr}</small>
          {/if}
        {:else}
          <input type="text" bind:value={model} placeholder={placeholderModel()} />
        {/if}
      </label>

      <label>
        <span>endpoint</span>
        <div class="row">
          <input type="text" bind:value={endpoint} placeholder={presetEndpoint()} class="grow"
            on:blur={() => provider === "ollama" && refreshModels()} />
          {#if provider === "ollama"}
            <button type="button" on:click={refreshModels}>↻</button>
          {/if}
        </div>
      </label>

      <label>
        <span>api key env var</span>
        <input type="text" bind:value={apiKeyEnv} placeholder={provider === "ollama" ? "(unused for Ollama)" : "e.g. OPENAI_API_KEY"} />
        <small class="dim">name of the environment variable holding the key (the value is never stored)</small>
      </label>
    </fieldset>

    {#if saveErr}<div class="banner error">{saveErr}</div>{/if}
    {#if saveOk}<div class="banner ok">{saveOk}</div>{/if}

    <div class="actions">
      <button class="primary" on:click={save} disabled={saving || !model}>
        {saving ? "saving…" : "save & reload"}
      </button>
    </div>
  {/if}
</section>

<style>
  .settings {
    padding: 1.5rem 2rem;
    max-width: 640px;
    display: flex;
    flex-direction: column;
    gap: 1.2rem;
  }
  header h2 {
    margin: 0 0 0.3rem;
    font-weight: 500;
  }
  header .dim {
    margin: 0;
    font-size: 0.78rem;
    color: var(--muted);
  }
  code {
    font-family: var(--mono);
    background: var(--panel);
    padding: 1px 5px;
    border-radius: 4px;
  }
  fieldset {
    border: none;
    padding: 0;
    margin: 0;
    display: flex;
    flex-direction: column;
    gap: 1rem;
  }
  label {
    display: flex;
    flex-direction: column;
    gap: 0.3rem;
  }
  label > span {
    font-size: 0.74rem;
    text-transform: uppercase;
    letter-spacing: 0.06em;
    color: var(--muted);
  }
  input[type="text"], select {
    font: inherit;
    padding: 0.45rem 0.6rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: white;
    width: 100%;
  }
  .row {
    display: flex;
    gap: 0.4rem;
  }
  .row .grow { flex: 1; }
  .row button {
    padding: 0.45rem 0.7rem;
    border: 1px solid var(--border);
    border-radius: 6px;
    background: white;
    cursor: pointer;
    font: inherit;
  }
  .radios {
    display: flex;
    gap: 0.6rem;
  }
  .radio {
    flex-direction: row;
    align-items: center;
    gap: 0.3rem;
    cursor: pointer;
    font-size: 0.86rem;
    color: var(--text);
    text-transform: none;
    letter-spacing: 0;
  }
  small {
    font-size: 0.74rem;
  }
  small.dim { color: var(--muted); }
  small.warn { color: #b04a4a; }

  .banner {
    padding: 0.6rem 0.9rem;
    border-radius: 6px;
    font-size: 0.86rem;
  }
  .banner.error { background: #fdecec; color: #8a3a3a; }
  .banner.ok { background: #ecf5ec; color: #2e6a3e; }

  .actions {
    display: flex;
    justify-content: flex-end;
  }
  .actions button {
    padding: 0.55rem 1.2rem;
    border-radius: 6px;
    border: none;
    background: var(--accent);
    color: white;
    cursor: pointer;
    font: inherit;
  }
  .actions button:disabled {
    opacity: 0.5;
    cursor: not-allowed;
  }
</style>
