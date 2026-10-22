<script setup lang="ts">
// 知识库管理：知识库列表（元信息 + 分块数）+ 知识文件导入/删除 + 分块浏览/搜索/删除 + 内容重同步
import { onMounted, ref } from 'vue'
import { MessagePlugin } from 'tdesign-vue-next'
import type { PrimaryTableCol, UploadFile } from 'tdesign-vue-next'
import { RefreshCw, Trash2, Upload } from 'lucide-vue-next'
import { contentApi } from '@/api'
import { useAuthStore } from '@/stores/auth'
import StepUpDialog from '@/components/StepUpDialog.vue'
import type { KnowledgeBaseView, KnowledgeChunk, KnowledgeFile } from '@/types/api'

const columns: PrimaryTableCol[] = [
  { colKey: 'id', title: 'ID', width: 80, align: 'center' },
  { colKey: 'title', title: '标题', width: 220 },
  { colKey: 'content', title: '内容' },
  { colKey: 'source_id', title: '来源', width: 200 },
  { colKey: 'created_at', title: '更新时间', width: 160 },
  { colKey: 'ops', title: '操作', width: 90, fixed: 'right' },
]

const fileColumns: PrimaryTableCol[] = [
  { colKey: 'name', title: '文件', width: 260 },
  { colKey: 'size', title: '大小', width: 120 },
  { colKey: 'updated_at', title: '更新时间', width: 180 },
  { colKey: 'ops', title: '操作', width: 90, fixed: 'right' },
]

// 与后端 handlers.maxImportFileSize / maxImportFileCount 保持一致
const MAX_FILE_SIZE = 8 * 1024 * 1024
const MAX_FILE_COUNT = 10

const auth = useAuthStore()

const bases = ref<KnowledgeBaseView[]>([])
const chunks = ref<KnowledgeChunk[]>([])
const chunksTotal = ref(0)
const loading = ref(false)
const syncing = ref(false)
const currentBase = ref('')
const keyword = ref('')
const page = ref(1)
const pageSize = 20

const stepUpVisible = ref(false)
const pendingAction = ref<((token: string) => Promise<void>) | null>(null)

const files = ref<KnowledgeFile[]>([])
const filesLoading = ref(false)
const uploading = ref(false)
const pendingFiles = ref<UploadFile[]>([])

async function loadBases() {
  try {
    const res = await contentApi.knowledgeBases()
    bases.value = res.items
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '知识库加载失败')
  }
}

async function loadChunks() {
  loading.value = true
  try {
    const res = await contentApi.knowledgeChunks({
      base: currentBase.value || undefined,
      keyword: keyword.value || undefined,
      page: page.value,
      pageSize,
    })
    chunks.value = res.items
    chunksTotal.value = res.total
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '分块加载失败')
  } finally {
    loading.value = false
  }
}

async function loadFiles() {
  filesLoading.value = true
  try {
    const res = await contentApi.knowledgeFiles(currentBase.value)
    files.value = res.items
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '知识文件加载失败')
  } finally {
    filesLoading.value = false
  }
}

function pageChange(p: { current: number }) {
  page.value = p.current
  void loadChunks()
}

function search() {
  page.value = 1
  void loadChunks()
}

function selectBase(id: string) {
  currentBase.value = id
  page.value = 1
  void loadChunks()
  void loadFiles()
}

// 上传前的本地校验：类型与大小先挡一道，避免白跑一次 step-up 与一次大体积请求
function pickFiles(): File[] | null {
  const picked = pendingFiles.value
    .map((f) => f.raw)
    .filter((f): f is File => f instanceof File)
  if (picked.length === 0) {
    MessagePlugin.warning('请先选择文件')
    return null
  }
  if (picked.length > MAX_FILE_COUNT) {
    MessagePlugin.error(`单次最多上传 ${MAX_FILE_COUNT} 个文件`)
    return null
  }
  const invalidExt = picked.find((f) => !/\.(md|csv)$/i.test(f.name))
  if (invalidExt) {
    MessagePlugin.error(`不支持的文件类型：${invalidExt.name}（仅支持 .md / .csv）`)
    return null
  }
  const tooLarge = picked.find((f) => f.size > MAX_FILE_SIZE)
  if (tooLarge) {
    MessagePlugin.error(`文件 ${tooLarge.name} 超过 8MB 上限`)
    return null
  }
  return picked
}

function uploadFiles() {
  const picked = pickFiles()
  if (!picked) return
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    uploading.value = true
    try {
      const res = await contentApi.uploadKnowledgeFiles(picked, currentBase.value, token)
      MessagePlugin.success(`已导入 ${res.files.length} 个文件并完成同步`)
      pendingFiles.value = []
      await Promise.all([loadFiles(), loadBases(), loadChunks()])
    } catch (err) {
      MessagePlugin.error(err instanceof Error ? err.message : '文件导入失败')
    } finally {
      uploading.value = false
    }
  }
}

function removeFile(file: KnowledgeFile) {
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    try {
      await contentApi.deleteKnowledgeFile(file.name, currentBase.value, token)
      MessagePlugin.success('文件已删除，对应分块已清理')
      await Promise.all([loadFiles(), loadBases(), loadChunks()])
    } catch (err) {
      MessagePlugin.error(err instanceof Error ? err.message : '文件删除失败')
    }
  }
}

function removeChunk(chunk: KnowledgeChunk) {
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    await contentApi.deleteKnowledgeChunk(chunk.id, token)
    MessagePlugin.success('分块已删除')
    await loadChunks()
  }
}

function syncAll() {
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    syncing.value = true
    try {
      await contentApi.syncKnowledge(currentBase.value, token)
      MessagePlugin.success('知识库已重新同步')
      await loadBases()
      await loadChunks()
    } finally {
      syncing.value = false
    }
  }
}

function onStepUpSuccess(token: string) {
  void pendingAction.value?.(token)
  pendingAction.value = null
}

function formatTime(s: string): string {
  return new Date(s).toLocaleString()
}

function formatSize(bytes: number): string {
  if (bytes < 1024) return `${bytes} B`
  if (bytes < 1024 * 1024) return `${(bytes / 1024).toFixed(1)} KB`
  return `${(bytes / 1024 / 1024).toFixed(1)} MB`
}

onMounted(() => {
  void loadBases()
  void loadChunks()
  void loadFiles()
})
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">知识库</h2>
      <t-button v-if="auth.isSuper" theme="primary" variant="outline" :loading="syncing" @click="syncAll">
        <template #icon><RefreshCw :size="16" /></template>
        重新同步
      </t-button>
    </div>

    <t-row :gutter="16" class="mb-16">
      <t-col v-for="b in bases" :key="b.id" :xs="12" :sm="8" :md="6" :lg="4">
        <t-card
          class="base-card"
          :class="{ active: currentBase === b.id }"
          @click="selectBase(b.id)"
        >
          <div class="base-name">{{ b.name }}</div>
          <div class="base-id">{{ b.id }} · {{ b.provider }}</div>
          <div class="base-desc">{{ b.description || '暂无描述' }}</div>
          <div class="base-meta">
            <t-tag :theme="b.enabled ? 'success' : 'default'" variant="light" size="small">
              {{ b.enabled ? '启用' : '停用' }}
            </t-tag>
            <t-tag theme="primary" variant="light" size="small">{{ b.chunks }} 分块</t-tag>
          </div>
        </t-card>
      </t-col>
    </t-row>

    <t-card title="知识文件" class="mb-16">
      <t-space v-if="auth.isSuper" class="mb-16" direction="vertical" :size="8">
        <t-upload
          v-model="pendingFiles"
          :auto-upload="false"
          :multiple="true"
          :max="MAX_FILE_COUNT"
          accept=".md,.csv"
          :tips="`仅支持 .md / .csv，单文件 8MB 以内，单次最多 ${MAX_FILE_COUNT} 个；同名文件直接覆盖`"
        />
        <t-button theme="primary" :loading="uploading" @click="uploadFiles">
          <template #icon><Upload :size="16" /></template>
          上传并同步
        </t-button>
      </t-space>
      <t-table
        :data="files"
        :loading="filesLoading"
        row-key="name"
        size="small"
        :columns="fileColumns"
      >
        <template #name="{ row }">{{ row.name }}</template>
        <template #size="{ row }">{{ formatSize(row.size) }}</template>
        <template #updated_at="{ row }">{{ formatTime(row.updated_at) }}</template>
        <template #ops="{ row }">
          <t-button v-if="auth.isSuper" size="small" variant="base" theme="danger" @click="removeFile(row)">
            <template #icon><Trash2 :size="14" /></template>删除
          </t-button>
        </template>
      </t-table>
    </t-card>

    <t-card title="知识分块">
      <t-space class="mb-16">
        <t-select
          v-model="currentBase"
          placeholder="全部知识库"
          clearable
          :options="bases.map((b) => ({ value: b.id, label: b.name }))"
          :style="{ width: 200 }"
          @change="selectBase"
        />
        <t-input
          v-model="keyword"
          placeholder="搜索标题 / 内容…"
          clearable
          :style="{ width: 240 }"
          @enter="search"
          @clear="search"
        />
        <t-button theme="primary" :loading="loading" @click="search">查询</t-button>
      </t-space>

      <t-table
        :data="chunks"
        :loading="loading"
        row-key="id"
        :pagination="{ current: page, pageSize, total: chunksTotal, onChange: pageChange }"
        :columns="columns"
      >
        <template #id="{ row }">{{ row.id }}</template>
        <template #title="{ row }">{{ row.title || '（无标题）' }}</template>
        <template #content="{ row }">
          <div class="chunk-content">{{ row.content }}</div>
        </template>
        <template #source_id="{ row }">
          <t-tooltip :content="row.source_id" placement="top" show-arrow>
            <span class="source">{{ row.source_id }}</span>
          </t-tooltip>
        </template>
        <template #created_at="{ row }">{{ formatTime(row.updated_at) }}</template>
        <template #ops="{ row }">
          <t-button v-if="auth.isSuper" size="small" variant="base" theme="danger" @click="removeChunk(row)">
            <template #icon><Trash2 :size="14" /></template>删除
          </t-button>
        </template>
      </t-table>
    </t-card>

    <StepUpDialog v-model="stepUpVisible" @success="onStepUpSuccess" />
  </div>
</template>

<style scoped>
.base-card {
  cursor: pointer;
  transition: border-color 0.2s;
  border: 2px solid transparent;
}
.base-card.active {
  border-color: var(--td-brand-color);
}
.base-name {
  font-weight: 600;
  font-size: 15px;
}
.base-id {
  font-size: 12px;
  color: var(--mgr-text-secondary);
  margin: 4px 0;
}
.base-desc {
  font-size: 13px;
  color: var(--mgr-text-secondary);
  min-height: 36px;
  display: -webkit-box;
  -webkit-line-clamp: 2;
  -webkit-box-orient: vertical;
  overflow: hidden;
}
.base-meta {
  display: flex;
  align-items: center;
  gap: 8px;
  margin-top: 8px;
}
.chunk-content {
  max-width: 480px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
}
.source {
  display: inline-block;
  max-width: 180px;
  white-space: nowrap;
  overflow: hidden;
  text-overflow: ellipsis;
  vertical-align: bottom;
}
</style>
