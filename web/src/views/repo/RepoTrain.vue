<template>
  <div class="flex flex-col gap-4">
    <Panel v-if="loadError">
      <span class="text-wp-error-100">{{ $t('repo.train.load_error', { error: loadError }) }}</span>
    </Panel>

    <div v-else-if="!train" class="text-wp-text-100 flex justify-center">
      <Icon name="spinner" class="animate-spin" />
    </div>

    <Panel v-else-if="!train.settings.enabled">
      <div class="flex flex-col items-start gap-2">
        <span>{{ $t('repo.train.off', { branch: train.settings.branch, target: train.settings.target }) }}</span>
        <Button
          v-if="repoPermissions.admin"
          :text="$t('repo.train.open_settings')"
          start-icon="settings"
          :to="{ name: 'repo-settings' }"
        />
      </div>
    </Panel>

    <template v-else>
      <!-- headline: what the train does now -->
      <div class="grid grid-cols-1 gap-4 md:grid-cols-3">
        <div :class="tileClass">
          <div class="flex items-center gap-2">
            <div :class="tileTitleClass">{{ $t('repo.train.status') }}</div>
            <IconButton
              icon="refresh"
              class="ml-auto h-6 w-6"
              :title="$t('repo.train.reload')"
              :is-loading="loading"
              @click="load()"
            />
          </div>
          <div class="flex items-center gap-2 text-xl font-bold" :class="kindTextClass[phaseKind(state.phase)]">
            <Icon :name="state.phase === 'frozen' ? 'frozen' : 'train'" />
            {{ phaseLabel[state.phase] }}
          </div>
          <div class="text-sm">
            <router-link
              v-if="state.phase === 'promoting' || state.phase === 'investigating'"
              :to="pipelineRoute(state.pipeline)"
              class="hover:underline"
            >
              {{ phaseDescription }}
            </router-link>
            <span v-else>{{ phaseDescription }}</span>
          </div>
          <div v-if="state.frozen && state.frozen_reason" class="text-wp-error-100 text-sm break-words">
            {{ $t('repo.train.branch_locked', { branch: train.settings.branch, reason: state.frozen_reason }) }}
          </div>
          <div class="text-wp-text-alt-100 text-xs">
            {{ $t('repo.train.updated', { time: state.updated ? timeAgo(state.updated * 1000) : '-' }) }}
          </div>
        </div>

        <div :class="tileClass">
          <div :class="tileTitleClass">{{ $t('repo.train.route') }}</div>
          <div class="text-wp-text-200 flex items-center gap-2 text-xl font-bold">
            <Icon name="branch" />
            <!-- eslint-disable-next-line @intlify/vue-i18n/no-raw-text -->
            <span class="truncate">{{ train.settings.branch }} &rarr; {{ train.settings.target }}</span>
          </div>
          <div class="text-sm">
            {{ $t('repo.train.cars_waiting', { count: cars.length }, cars.length) }}
          </div>
          <div v-if="repoPermissions.push" class="mt-auto flex flex-wrap gap-2 pt-2">
            <Button
              v-if="canResume(state)"
              color="green"
              start-icon="play"
              :text="$t('repo.train.resume')"
              :title="$t('repo.train.resume_desc', { branch: train.settings.branch })"
              :is-loading="isResuming"
              @click="resume()"
            />
            <Button
              v-if="canRetry(state)"
              start-icon="refresh"
              :text="$t('repo.train.retry')"
              :title="$t('repo.train.retry_desc', { branch: train.settings.branch })"
              :is-loading="isRetrying"
              @click="retry()"
            />
          </div>
        </div>

        <div :class="tileClass">
          <div :class="tileTitleClass">{{ $t('repo.train.last_promote') }}</div>
          <template v-if="state.last_promoted">
            <div class="text-wp-text-200 flex items-center gap-2 text-xl font-bold">
              <Icon name="commit" />
              <span class="font-mono">{{ shortSha(state.last_promoted) }}</span>
            </div>
            <router-link
              v-if="state.last_promote_pipeline"
              :to="pipelineRoute(state.last_promote_pipeline)"
              class="text-sm hover:underline"
            >
              {{ $t('repo.train.last_promote_pipeline', { pipeline: state.last_promote_pipeline }) }}
            </router-link>
          </template>
          <div v-else class="text-sm">{{ $t('repo.train.last_promote_none') }}</div>
          <div v-if="state.last_result" class="text-wp-text-alt-100 text-xs break-words">{{ state.last_result }}</div>
        </div>
      </div>

      <Panel :title="$t('repo.train.cars')">
        <div v-if="cars.length === 0" class="text-wp-text-alt-100 text-sm">{{ $t('repo.train.cars_none') }}</div>
        <ul v-else class="flex flex-col">
          <li
            v-for="car in cars"
            :key="car.sha"
            class="border-wp-background-400 flex min-w-0 flex-wrap items-center gap-x-3 gap-y-1 border-t py-1.5 text-sm first:border-t-0"
          >
            <span class="text-wp-text-alt-100 font-mono">{{ shortSha(car.sha) }}</span>
            <span class="min-w-0 flex-1 truncate" :title="car.title">{{ car.title }}</span>
            <!-- eslint-disable-next-line @intlify/vue-i18n/no-raw-text -->
            <span v-if="car.pr" class="text-wp-text-alt-100">#{{ car.pr }}</span>
            <span class="text-wp-text-alt-100 text-xs" :title="(car.files ?? []).join('\n')">
              {{ $t('repo.train.files', { count: car.files?.length ?? 0 }, car.files?.length ?? 0) }}
            </span>
            <span v-if="carMark(state, car.sha)" class="text-xs font-bold" :class="markClass[carMark(state, car.sha)!]">
              {{ markLabel[carMark(state, car.sha)!] }}
            </span>
          </li>
        </ul>
      </Panel>

      <Panel v-if="investigations.length > 0" :title="$t('repo.train.investigations')">
        <div class="flex flex-col gap-3">
          <div v-for="inv in investigations" :key="inv.workflow" class="flex flex-col gap-1 text-sm">
            <div class="flex flex-wrap items-center gap-2">
              <span class="text-wp-text-200 font-bold">{{ inv.workflow }}</span>
              <span class="text-wp-text-alt-100">
                {{ inv.outcome ? outcomeLabel[inv.outcome] : stepLabel[inv.step] }}
              </span>
              <span v-if="inv.step === 'bisect'" class="text-wp-text-alt-100 text-xs">
                {{
                  $t(
                    'repo.train.suspects',
                    { count: remaining(inv).length, total: inv.candidates.length },
                    remaining(inv).length,
                  )
                }}
              </span>
              <span v-if="inv.culprit" class="text-wp-error-100">
                {{ carTitle(inv.culprit) }}
              </span>
            </div>
            <div v-if="inv.detail" class="text-wp-text-alt-100 text-xs break-words">{{ inv.detail }}</div>
            <div v-if="inv.probes?.length" class="flex flex-wrap gap-x-3 gap-y-1 text-xs">
              <router-link
                v-for="probe in inv.probes"
                :key="probe.pipeline"
                :to="pipelineRoute(probe.pipeline)"
                class="hover:underline"
                :class="probe.result ? probeClass[probe.result] : kindTextClass.running"
              >
                {{
                  probe.index < 0
                    ? $t('repo.train.probe_head', { pipeline: probe.pipeline })
                    : $t('repo.train.probe', { pipeline: probe.pipeline, sha: shortSha(probe.sha) })
                }}
                {{ probeLabel[probe.result ?? 'running'] }}
              </router-link>
            </div>
          </div>
        </div>
      </Panel>

      <div class="grid grid-cols-1 gap-4 xl:grid-cols-2">
        <Panel :title="$t('repo.train.reverts')">
          <div v-if="reverts.length === 0" class="text-wp-text-alt-100 text-sm">
            {{ $t('repo.train.reverts_none') }}
          </div>
          <ul v-else class="flex flex-col">
            <li
              v-for="revert in reverts"
              :key="revert.car"
              class="border-wp-background-400 flex flex-col border-t py-1.5 text-sm first:border-t-0"
            >
              <div class="flex min-w-0 gap-2">
                <span class="min-w-0 flex-1 truncate" :title="revert.title">{{ revert.title }}</span>
                <!-- eslint-disable-next-line @intlify/vue-i18n/no-raw-text -->
                <span v-if="revert.pr" class="text-wp-text-alt-100">#{{ revert.pr }}</span>
              </div>
              <div class="text-wp-text-alt-100 flex gap-2 text-xs">
                <router-link :to="pipelineRoute(revert.promote)" class="hover:underline">
                  {{
                    $t('repo.train.revert_broke', { workflows: revert.workflows.join(', '), pipeline: revert.promote })
                  }}
                </router-link>
                <span class="ml-auto shrink-0">{{ timeAgo(revert.created * 1000) }}</span>
              </div>
            </li>
          </ul>
        </Panel>

        <Panel :title="$t('repo.train.history')">
          <div v-if="history.length === 0" class="text-wp-text-alt-100 text-sm">
            {{ $t('repo.train.history_none') }}
          </div>
          <ul v-else class="flex max-h-96 flex-col overflow-y-auto">
            <li
              v-for="(event, index) in history"
              :key="index"
              class="border-wp-background-400 flex min-w-0 gap-2 border-t py-1 text-sm first:border-t-0"
            >
              <span
                class="text-wp-text-alt-100 w-28 shrink-0 text-xs"
                :title="toLocaleString(new Date(event.time * 1000))"
              >
                {{ timeAgo(event.time * 1000) }}
              </span>
              <span class="text-wp-text-alt-100 w-16 shrink-0 text-xs font-bold">{{ event.kind }}</span>
              <router-link
                v-if="event.pipeline"
                :to="pipelineRoute(event.pipeline)"
                class="min-w-0 flex-1 break-words hover:underline"
              >
                {{ event.message }}
              </router-link>
              <span v-else class="min-w-0 flex-1 break-words">{{ event.message }}</span>
            </li>
          </ul>
        </Panel>
      </div>
    </template>
  </div>
</template>

<script lang="ts" setup>
import { useDebounceFn, useIntervalFn } from '@vueuse/core';
import { computed, onMounted, ref, watch } from 'vue';
import type { RouteLocationRaw } from 'vue-router';
import { useI18n } from 'vue-i18n';

import Button from '~/components/atomic/Button.vue';
import Icon from '~/components/atomic/Icon.vue';
import IconButton from '~/components/atomic/IconButton.vue';
import Panel from '~/components/layout/Panel.vue';
import { kindTextClass } from '~/components/repo/insights/stats';
import type { CarMark } from '~/components/repo/train/train';
import {
  canResume,
  canRetry,
  carMark,
  newestFirst,
  phaseKind,
  remaining,
  shortSha,
} from '~/components/repo/train/train';
import useApiClient from '~/compositions/useApiClient';
import { useAsyncAction } from '~/compositions/useAsyncAction';
import { useDate } from '~/compositions/useDate';
import { requiredInject } from '~/compositions/useInjectProvide';
import useNotifications from '~/compositions/useNotifications';
import { useWPTitle } from '~/compositions/useWPTitle';
import type { TrainInvestigation, TrainPhase, TrainProbe, TrainState, TrainStatus } from '~/lib/api/types';

const apiClient = useApiClient();
const notifications = useNotifications();
const { t } = useI18n();
const { toLocaleString, timeAgo } = useDate();

const repo = requiredInject('repo');
const repoPermissions = requiredInject('repo-permissions');
const repoPipelines = requiredInject('pipelines');

const tileClass =
  'border-wp-background-400 dark:border-wp-background-100 bg-wp-background-100 text-wp-text-100 flex min-w-0 flex-col gap-1 rounded-md border p-4';
const tileTitleClass = 'text-wp-text-alt-100 text-xs font-bold uppercase';

const markClass: Record<CarMark, string> = {
  reverted: 'text-wp-state-neutral-100',
  culprit: 'text-wp-error-100',
  suspect: 'text-wp-state-warn-100',
};

const probeClass: Record<NonNullable<TrainProbe['result']>, string> = {
  success: kindTextClass.success,
  failure: kindTextClass.failure,
  inconclusive: kindTextClass.canceled,
};

const phaseLabel = computed<Record<TrainPhase, string>>(() => ({
  idle: t('repo.train.phase.idle'),
  promoting: t('repo.train.phase.promoting'),
  investigating: t('repo.train.phase.investigating'),
  held: t('repo.train.phase.held'),
  frozen: t('repo.train.phase.frozen'),
}));

const markLabel = computed<Record<CarMark, string>>(() => ({
  reverted: t('repo.train.mark.reverted'),
  culprit: t('repo.train.mark.culprit'),
  suspect: t('repo.train.mark.suspect'),
}));

const stepLabel = computed<Record<TrainInvestigation['step'], string>>(() => ({
  recheck: t('repo.train.step.recheck'),
  flake_check: t('repo.train.step.flake_check'),
  bisect: t('repo.train.step.bisect'),
  done: t('repo.train.step.done'),
}));

const outcomeLabel = computed<Record<NonNullable<TrainInvestigation['outcome']>, string>>(() => ({
  culprit: t('repo.train.outcome.culprit'),
  flake: t('repo.train.outcome.flake'),
  base_broken: t('repo.train.outcome.base_broken'),
  inconclusive: t('repo.train.outcome.inconclusive'),
}));

const probeLabel = computed<Record<NonNullable<TrainProbe['result']> | 'running', string>>(() => ({
  running: t('repo.train.probe_result.running'),
  success: t('repo.train.probe_result.success'),
  failure: t('repo.train.probe_result.failure'),
  inconclusive: t('repo.train.probe_result.inconclusive'),
}));

const train = ref<TrainStatus>();
const loading = ref(false);
const loadError = ref<string>();
let requestSeq = 0;

async function load(silent = false) {
  const seq = ++requestSeq;
  loading.value = true;
  try {
    const result = await apiClient.getTrain(repo.value.id);
    if (seq !== requestSeq) {
      return;
    }
    train.value = result;
    loadError.value = undefined;
  } catch (error) {
    if (seq === requestSeq && !silent) {
      train.value = undefined;
      loadError.value = error instanceof Error ? error.message : String(error);
    }
  } finally {
    if (seq === requestSeq) {
      loading.value = false;
    }
  }
}

const state = computed<TrainState>(
  () =>
    train.value?.state ?? {
      phase: 'idle',
      frozen: false,
      frozen_reason: '',
      head: '',
      base: '',
      pipeline: 0,
      updated: 0,
    },
);
const cars = computed(() => state.value.cars ?? []);
const investigations = computed(() => state.value.investigations ?? []);
const reverts = computed(() => [...(state.value.reverts ?? [])].reverse());
const history = computed(() => newestFirst(state.value.history));

const phaseDescription = computed(() => {
  const params = {
    branch: train.value?.settings.branch,
    target: train.value?.settings.target,
    pipeline: state.value.pipeline,
    head: shortSha(state.value.head || state.value.held_head),
  };
  switch (state.value.phase) {
    case 'promoting':
      return t('repo.train.phase_desc.promoting', params);
    case 'investigating':
      return t('repo.train.phase_desc.investigating', params);
    case 'held':
      return t('repo.train.phase_desc.held', params);
    case 'frozen':
      return t('repo.train.phase_desc.frozen', params);
    default:
      return t('repo.train.phase_desc.idle', params);
  }
});

function carTitle(sha: string): string {
  return state.value.cars?.find((c) => c.sha === sha)?.title ?? shortSha(sha);
}

function pipelineRoute(pipeline: number): RouteLocationRaw {
  return { name: 'repo-pipeline', params: { pipelineId: pipeline } };
}

const { doSubmit: resume, isLoading: isResuming } = useAsyncAction(async () => {
  train.value = await apiClient.resumeTrain(repo.value.id);
  notifications.notify({ title: t('repo.train.resumed'), type: 'success' });
});

const { doSubmit: retry, isLoading: isRetrying } = useAsyncAction(async () => {
  train.value = await apiClient.retryTrain(repo.value.id);
  notifications.notify({ title: t('repo.train.retried'), type: 'success' });
});

watch(
  () => repo.value.id,
  async () => load(),
);
onMounted(async () => load());

// the train reconciles on a timer too, so poll quietly while the page is open
useIntervalFn(async () => load(true), 30000);

// and reload soon when a promote pipeline changes (the pipeline store gets server events)
const reloadSoon = useDebounceFn(async () => load(true), 2000);
watch(
  () =>
    repoPipelines.value
      .filter((p) => p.event === 'promote')
      .map((p) => `${p.number}:${p.status}`)
      .join(','),
  (sig, old) => {
    if (sig !== old && train.value) {
      void reloadSoon();
    }
  },
);

useWPTitle(computed(() => [t('repo.train.title'), repo.value.full_name]));
</script>
