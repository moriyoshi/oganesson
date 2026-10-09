<script lang="ts">
  let count: number = $state(1);
  let double = $derived(count * 2);
</script>

<h1 class="title">Hello from Svelte</h1>
<button id="inc" onclick={() => count++}>count {count}</button>
<p id="double">double {double}</p>

<style>
  .title {
    color: rgb(40, 50, 60);
  }
</style>
