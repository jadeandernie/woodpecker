<template>
  <Container full-width class="flex flex-col gap-4 md:px-4">
    <Panel v-if="showErrorNotice">
      <div class="flex flex-wrap items-center justify-between gap-4">
        <div class="flex items-center gap-2">
          <Icon name="status-error" class="text-wp-error-100" />
          <span>{{ $t('repo.pipeline.we_got_some_errors') }}</span>
        </div>
        <Button color="red" :text="$t('repo.pipeline.show_errors')" :to="{ name: 'repo-pipeline-errors' }" />
      </div>
    </Panel>

    <Panel v-else-if="pipeline.status === 'blocked'">
      <div class="flex flex-wrap items-center justify-between gap-4">
        <div class="flex items-center gap-2">
          <Icon name="status-blocked" />
          <span>{{ $t('repo.pipeline.protected.awaits') }}</span>
        </div>
        <Button :text="$t('repo.pipeline.tasks')" :to="{ name: 'repo-pipeline' }" />
      </div>
    </Panel>

    <WorkflowGraph :pipeline="pipeline" />
  </Container>
</template>

<script lang="ts" setup>
import { computed } from 'vue';
import { useI18n } from 'vue-i18n';

import Button from '~/components/atomic/Button.vue';
import Icon from '~/components/atomic/Icon.vue';
import Container from '~/components/layout/Container.vue';
import Panel from '~/components/layout/Panel.vue';
import WorkflowGraph from '~/components/repo/pipeline/graph/WorkflowGraph.vue';
import { requiredInject } from '~/compositions/useInjectProvide';
import { useWPTitle } from '~/compositions/useWPTitle';
import { pipelineHasNonWarningErrors } from '~/lib/pipeline';

// the pipeline is provided by PipelineWrapper and kept up to date by the pipeline store's event stream
const pipeline = requiredInject('pipeline');
const repo = requiredInject('repo');
const i18n = useI18n();

const showErrorNotice = computed(() => pipelineHasNonWarningErrors(pipeline.value));

useWPTitle(
  computed(() => [
    i18n.t('repo.pipeline.graph.title'),
    i18n.t('repo.pipeline.pipeline', { pipelineId: pipeline.value.number }),
    repo.value.full_name,
  ]),
);
</script>
