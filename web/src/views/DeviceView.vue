<script setup lang="ts">
import DevicePane from '../components/DevicePane.vue'
</script>

<template>
  <div class="mb-device-page">
    <DevicePane />
  </div>
</template>

<style scoped>
.mb-device-page {
  padding: 14px 16px 28px;
  max-width: 1500px;
  margin: 0 auto;
}
@media (max-width: 1100px) {
  .mb-device-page {
    padding: 10px 10px 28px;
  }
}
.mb-device-page :deep(.mb-device) {
  max-height: none;
  min-height: 0;
  padding: 0;
}
</style>
