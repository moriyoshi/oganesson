<script setup lang="ts">
const count = ref(0);
</script>

<template>
  <button id="count" @click="count++">count {{ count }}</button>
</template>
