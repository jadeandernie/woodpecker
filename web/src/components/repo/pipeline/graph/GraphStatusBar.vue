<template>
  <div v-if="states.length > 0" class="flex h-1 gap-px overflow-hidden">
    <div v-for="(state, index) in states" :key="index" class="h-full grow" :class="statusBackground(state)" />
  </div>
</template>

<script lang="ts" setup>
import type { PipelineStatus } from '~/lib/api/types';

import { statusBackground } from './useGateText';

defineProps<{
  states: PipelineStatus[];
}>();
</script>
