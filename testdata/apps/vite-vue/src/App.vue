<script setup lang="ts">
import { computed, ref } from "vue";

const count = ref<number>(1);
const double = computed(() => count.value * 2);
</script>

<template>
  <h1 class="title">Hello from Vue</h1>
  <button id="inc" @click="count++">count {{ count }}</button>
  <p id="double">double {{ double }}</p>
</template>

<style scoped>
.title {
  color: rgb(10, 20, 30);
}
</style>
