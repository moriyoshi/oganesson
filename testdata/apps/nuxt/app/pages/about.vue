<script setup lang="ts">
const built = useState("built", () => "prerendered");
</script>

<template>
  <main>
    <h1>About</h1>
    <p id="built">{{ built }}</p>
  </main>
</template>
