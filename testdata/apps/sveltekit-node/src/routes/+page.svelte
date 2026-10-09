<script>
  import { enhance } from "$app/forms";
  let { data, form } = $props();
  let count = $state(0);
</script>

<svelte:head><title>og sveltekit</title></svelte:head>

<h1 class="brand">{data.greeting}</h1>
<p id="visits">visits {data.visits}</p>
<button id="count" onclick={() => count++}>count {count}</button>

<form method="POST" action="?/visit" use:enhance>
  <input name="by" value="2" />
  <button id="visit" type="submit">visit</button>
</form>
{#if form?.visits}<p id="result">now {form.visits}</p>{/if}
{#if form?.error}<p id="error">{form.error}</p>{/if}

<style>
  .brand {
    color: rgb(1, 99, 3);
  }
</style>
