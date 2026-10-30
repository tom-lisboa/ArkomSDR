<script>
  let { schema, contract } = $props();
  const reference = (value) => value?.$ref?.split('/').at(-1);
  const resolve = (value) => reference(value) ? contract.components.schemas[reference(value)] : value;
  const type = (value) => reference(value) || (value.type === 'array' ? `${reference(value.items) || value.items?.type || 'object'}[]` : value.type || 'object');
  let model = $derived(resolve(schema));
  let name = $derived(reference(schema));
</script>
{#if model}
  {#if name}<div class="schema-label"><span class="tiny-dot"></span>{name}</div>{/if}
  {#if model.properties}
    <div class="schema-fields">
      {#each Object.entries(model.properties) as [key, value]}
        <div class="schema-field">
          <div class="field-title"><code>{key}</code><span class="type-label">{type(value)}{value.nullable ? ' · null' : ''}</span>{#if model.required?.includes(key)}<span class="required">obrigatório</span>{/if}</div>
          {#if value.description}<p>{value.description}</p>{/if}
          {#if value.enum}<div class="enum-values">{#each value.enum as option}<code>{String(option)}</code>{/each}</div>{/if}
          {#if value.minimum !== undefined || value.maximum !== undefined}<p class="field-constraint">{value.minimum !== undefined ? `Mínimo: ${value.minimum}` : ''}{value.maximum !== undefined ? ` · Máximo: ${value.maximum}` : ''}</p>{/if}
          {#if reference(value) || value.properties || reference(value.items)}
            <details class="nested-schema"><summary>Ver estrutura{reference(value) ? ` de ${reference(value)}` : ''}</summary><pre><code>{JSON.stringify(resolve(reference(value.items) ? value.items : value), null, 2)}</code></pre></details>
          {/if}
        </div>
      {/each}
    </div>
  {:else}<pre><code>{JSON.stringify(model, null, 2)}</code></pre>{/if}
{/if}
