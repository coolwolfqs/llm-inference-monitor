<script setup lang="ts">
import { computed } from 'vue'
import { Box, Clock3, Folder, MoreHorizontal, Play, Star } from 'lucide-vue-next'
import type { ModelArtifact } from '../types/model'

const props = defineProps<{ model: ModelArtifact; running: boolean; favorite: boolean }>()
const emit = defineEmits<{
  deploy: [model: ModelArtifact]
  detail: [model: ModelArtifact]
  favorite: [model: ModelArtifact]
  rename: [model: ModelArtifact]
  remove: [model: ModelArtifact]
}>()

const title = computed(() => props.model.family || props.model.alias || props.model.classification?.general_name || '未分类')
const tags = computed(() => [
  props.model.role !== 'model' ? props.model.category : '',
  props.model.format,
  props.model.quant_type,
  props.model.classification?.parameters,
  ...props.model.tags,
].filter(Boolean))
</script>

<template>
  <article class="model-card" :class="{ running }">
    <div class="model-main">
      <div class="model-title-line">
        <h3>{{ title }}</h3>
        <span v-if="running" class="running-pill"><i />运行中</span>
      </div>
      <div class="tag-row">
        <span v-for="tag in tags" :key="String(tag)" class="tag" :data-kind="String(tag).toLowerCase()">{{ tag }}</span>
      </div>
      <p class="filename" :title="model.relative_path">{{ model.name }}</p>
      <div class="metadata">
        <span><Box :size="14" />{{ model.size_human }}</span>
        <span v-if="model.relative_dir"><Folder :size="14" />{{ model.relative_dir }}</span>
        <span><Clock3 :size="14" />{{ new Date(model.modified * 1000).toLocaleString('zh-CN', { month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit' }) }}</span>
      </div>
    </div>
    <div class="model-actions">
      <button v-if="model.deployable" class="icon-button star" :class="{ active: favorite }" :aria-label="favorite ? '取消收藏' : '收藏'" @click="emit('favorite', model)">
        <Star :size="17" :fill="favorite ? 'currentColor' : 'none'" />
      </button>
      <button v-if="!running && model.deployable" class="button primary compact" @click="emit('deploy', model)"><Play :size="15" fill="currentColor" />部署</button>
      <button class="button ghost compact" @click="emit('detail', model)">详情</button>
      <details v-if="!running" class="more-menu">
        <summary class="icon-button" aria-label="更多操作"><MoreHorizontal :size="18" /></summary>
        <div class="menu-popover">
          <button @click="emit('rename', model)">重命名</button>
          <button class="danger-text" @click="emit('remove', model)">移至回收站</button>
        </div>
      </details>
    </div>
  </article>
</template>
