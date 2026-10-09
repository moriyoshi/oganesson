<script setup lang="ts">
const route = useRoute();
const { data } = await useFetch("/api/hello", { query: { name: route.query.q ?? "nobody" } });
useHead({ title: "og nuxt" });
</script>

<template>
  <main>
    <h1 class="brand">{{ data?.greeting }}</h1>
    <p id="runtime">{{ data?.runtime }}</p>
    <Counter />
  </main>
</template>

<style scoped>
.brand {
  color: rgb(1, 99, 4);
}
</style>
