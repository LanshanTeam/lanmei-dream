<script setup lang="ts">
// LLM Provider 管理：列表 / 创建 / 编辑 / 热切换 / 删除（写操作均需 super + step-up）
import { onMounted, reactive, ref } from 'vue'
import { DialogPlugin, MessagePlugin } from 'tdesign-vue-next'
import { Plus, Power, RefreshCw, Trash2 } from 'lucide-vue-next'
import { llmApi } from '@/api'
import { useAuthStore } from '@/stores/auth'
import StepUpDialog from '@/components/StepUpDialog.vue'
import type { LLMProvider } from '@/types/api'
import type { PrimaryTableCol } from 'tdesign-vue-next'

const columns: PrimaryTableCol[] = [
  { colKey: 'name', title: '名称', width: 140 },
  { colKey: 'base_url', title: 'Base URL' },
  { colKey: 'model', title: '模型', width: 160 },
  { colKey: 'price', title: '价格（入/出）', width: 140 },
  { colKey: 'priority', title: '优先级', width: 80, align: 'center' },
  { colKey: 'enabled', title: '启用', width: 70, align: 'center' },
  { colKey: 'ops', title: '操作', width: 200, fixed: 'right' },
]

const auth = useAuthStore()

const items = ref<LLMProvider[]>([])
const activeName = ref('')
const loading = ref(false)

const stepUpVisible = ref(false)
const pendingAction = ref<((token: string) => Promise<void>) | null>(null)

const editVisible = ref(false)
const editing = ref<LLMProvider | null>(null)
const form = reactive({
  name: '',
  base_url: '',
  api_key: '',
  model: '',
  max_tokens: 4096,
  temperature: 0.7,
  presence_penalty: 0.3,
  frequency_penalty: 0.2,
  top_p: 0.95,
  top_k: 0,
  repetition_penalty: 1.0,
  min_p: 0,
  top_a: 0,
  seed: 0,
  in_price_per_m: 0,
  out_price_per_m: 0,
  enabled: true,
  priority: 0,
})

async function load() {
  loading.value = true
  try {
    const res = await llmApi.providers()
    items.value = res.items
    activeName.value = res.active
  } catch (err) {
    MessagePlugin.error(err instanceof Error ? err.message : '加载失败')
  } finally {
    loading.value = false
  }
}

function openCreate() {
  editing.value = null
  Object.assign(form, {
    name: '', base_url: '', api_key: '', model: '', max_tokens: 4096,
    temperature: 0.7, presence_penalty: 0.3, frequency_penalty: 0.2,
    top_p: 0.95, top_k: 0, repetition_penalty: 1.0, min_p: 0, top_a: 0, seed: 0,
    in_price_per_m: 0, out_price_per_m: 0, enabled: true, priority: 0,
  })
  editVisible.value = true
}

function openEdit(p: LLMProvider) {
  editing.value = p
  Object.assign(form, {
    name: p.name, base_url: p.base_url, api_key: '', model: p.model,
    max_tokens: p.max_tokens, temperature: p.temperature,
    presence_penalty: p.presence_penalty, frequency_penalty: p.frequency_penalty,
    top_p: p.top_p, top_k: p.top_k, repetition_penalty: p.repetition_penalty,
    min_p: p.min_p, top_a: p.top_a, seed: p.seed,
    in_price_per_m: p.in_price_per_m, out_price_per_m: p.out_price_per_m,
    enabled: p.enabled, priority: p.priority,
  })
  editVisible.value = true
}

async function save() {
  if (!form.name || !form.base_url || !form.model) {
    MessagePlugin.warning('名称 / Base URL / 模型不能为空')
    return
  }
  if (!editing.value && !form.api_key) {
    MessagePlugin.warning('API Key 不能为空')
    return
  }
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    const payload = {
      name: form.name,
      base_url: form.base_url,
      model: form.model,
      max_tokens: form.max_tokens,
      temperature: form.temperature,
      presence_penalty: form.presence_penalty,
      frequency_penalty: form.frequency_penalty,
      top_p: form.top_p,
      top_k: form.top_k,
      repetition_penalty: form.repetition_penalty,
      min_p: form.min_p,
      top_a: form.top_a,
      seed: form.seed,
      in_price_per_m: form.in_price_per_m,
      out_price_per_m: form.out_price_per_m,
      enabled: form.enabled,
      priority: form.priority,
      ...(form.api_key ? { api_key: form.api_key } : {}),
    }
    if (editing.value) {
      await llmApi.update(editing.value.id, payload, token)
      MessagePlugin.success('已更新')
    } else {
      await llmApi.create(payload, token)
      MessagePlugin.success('已创建')
    }
    editVisible.value = false
    await load()
  }
}

async function activate(p: LLMProvider) {
  stepUpVisible.value = true
  pendingAction.value = async (token) => {
    const res = await llmApi.activate(p.id, token)
    MessagePlugin.success(`已热切换到 ${res.active}（${res.model}）`)
    await load()
  }
}

async function remove(p: LLMProvider) {
  const confirm = DialogPlugin.confirm({
    header: '删除 Provider',
    body: `确定删除 Provider「${p.name}」吗？删除后不可恢复。`,
    theme: 'danger',
    confirmBtn: { content: '删除', theme: 'danger' },
    onConfirm: async () => {
      stepUpVisible.value = true
      pendingAction.value = async (token) => {
        await llmApi.remove(p.id, token)
        MessagePlugin.success('已删除')
        confirm.destroy()
        await load()
      }
    },
  })
}

function onStepUpSuccess(token: string) {
  void pendingAction.value?.(token)
  pendingAction.value = null
}

function fmtPrice(v: number): string {
  return v > 0 ? `¥${v}/百万` : '—'
}

onMounted(load)
</script>

<template>
  <div class="page">
    <div class="page-header">
      <h2 class="page-title">LLM Provider</h2>
      <div class="header-actions">
        <t-button variant="outline" @click="load"><template #icon><RefreshCw :size="16" /></template>刷新</t-button>
        <t-button v-if="auth.isSuper" theme="primary" @click="openCreate">
          <template #icon><Plus :size="16" /></template>
          新建 Provider
        </t-button>
      </div>
    </div>

    <t-alert theme="info" :message="`当前活跃：${activeName || '未配置'}`" class="mb-16" />

    <t-card>
      <t-table :data="items" :loading="loading" row-key="id" :columns="columns">
        <template #name="{ row }">
          <div class="name-cell">
            <t-tag v-if="row.name === activeName" theme="success" variant="light">活跃</t-tag>
            <span>{{ row.name }}</span>
          </div>
        </template>
        <template #base_url="{ row }">{{ row.base_url }}</template>
        <template #model="{ row }">{{ row.model }}</template>
        <template #price="{ row }">{{ fmtPrice(row.in_price_per_m) }} / {{ fmtPrice(row.out_price_per_m) }}</template>
        <template #priority="{ row }">{{ row.priority }}</template>
        <template #enabled="{ row }">
          <t-tag :theme="row.enabled ? 'success' : 'default'" variant="light">{{ row.enabled ? '是' : '否' }}</t-tag>
        </template>
        <template #ops="{ row }">
          <t-space v-if="auth.isSuper" size="4">
            <t-button size="small" variant="base" @click="openEdit(row)">编辑</t-button>
            <t-button v-if="row.enabled && row.name !== activeName" size="small" variant="base" theme="primary" @click="activate(row)">
              <template #icon><Power :size="14" /></template>切换
            </t-button>
            <t-button size="small" variant="base" theme="danger" @click="remove(row)"><template #icon><Trash2 :size="14" /></template>删除</t-button>
          </t-space>
        </template>
      </t-table>
    </t-card>

    <t-dialog
      v-model:visible="editVisible"
      :header="editing ? '编辑 Provider' : '新建 Provider'"
      :confirm-btn="{ content: '保存' }"
      cancel-btn="取消"
      width="560px"
      @confirm="save"
    >
      <t-form label-align="top">
        <t-row :gutter="16">
          <t-col :span="12">
            <t-form-item label="名称"><t-input v-model="form.name" placeholder="如 deepseek-chat" /></t-form-item>
          </t-col>
          <t-col :span="12">
            <t-form-item label="模型"><t-input v-model="form.model" placeholder="模型标识" /></t-form-item>
          </t-col>
        </t-row>
        <t-form-item label="Base URL">
          <t-input v-model="form.base_url" placeholder="如 https://api.deepseek.com/v1" />
        </t-form-item>
        <t-form-item :label="editing ? 'API Key（留空不修改）' : 'API Key'">
          <t-input v-model="form.api_key" type="password" placeholder="OpenAI 兼容 API Key" autocomplete="new-password" />
        </t-form-item>
        <t-row :gutter="16">
          <t-col :span="8">
            <t-form-item label="最大 Token">
              <t-input-number v-model="form.max_tokens" :min="1" :max="262144" :step="1024" />
            </t-form-item>
          </t-col>
          <t-col :span="8">
            <t-form-item label="Temperature">
              <t-input-number v-model="form.temperature" :min="0" :max="2" :step="0.1" />
            </t-form-item>
          </t-col>
          <t-col :span="8">
            <t-form-item label="优先级">
              <t-input-number v-model="form.priority" :min="0" />
            </t-form-item>
          </t-col>
        </t-row>
        <t-row :gutter="16">
          <t-col :span="12">
            <t-form-item label="Presence Penalty（话题重复）">
              <t-input-number v-model="form.presence_penalty" :min="0" :max="2" :step="0.1" />
            </t-form-item>
          </t-col>
          <t-col :span="12">
            <t-form-item label="Frequency Penalty（词频重复）">
              <t-input-number v-model="form.frequency_penalty" :min="0" :max="2" :step="0.1" />
            </t-form-item>
          </t-col>
        </t-row>
        <p class="sampling-hint">
          Top P 为通用核采样；Top K / Repetition Penalty / Min P / Top A 属于本地推理后端
          扩展参数（llama.cpp、oobabooga 等），OpenAI / DeepSeek 等端点请保持关闭（0 / 1）。
        </p>
        <t-row :gutter="16">
          <t-col :span="8">
            <t-form-item label="Top P">
              <t-input-number v-model="form.top_p" :min="0.01" :max="1" :step="0.05" />
            </t-form-item>
          </t-col>
          <t-col :span="8">
            <t-form-item label="Top K（0=关）">
              <t-input-number v-model="form.top_k" :min="0" :max="200" />
            </t-form-item>
          </t-col>
          <t-col :span="8">
            <t-form-item label="Repetition Penalty">
              <t-input-number v-model="form.repetition_penalty" :min="1" :max="2" :step="0.01" />
            </t-form-item>
          </t-col>
        </t-row>
        <t-row :gutter="16">
          <t-col :span="8">
            <t-form-item label="Min P（0=关）">
              <t-input-number v-model="form.min_p" :min="0" :max="1" :step="0.01" />
            </t-form-item>
          </t-col>
          <t-col :span="8">
            <t-form-item label="Top A（0=关）">
              <t-input-number v-model="form.top_a" :min="0" :max="1" :step="0.01" />
            </t-form-item>
          </t-col>
          <t-col :span="8">
            <t-form-item label="Seed（0=随机）">
              <t-input-number v-model="form.seed" :min="0" />
            </t-form-item>
          </t-col>
        </t-row>
        <t-row :gutter="16">
          <t-col :span="12">
            <t-form-item label="输入价格（元/百万）">
              <t-input-number v-model="form.in_price_per_m" :min="0" :step="0.01" />
            </t-form-item>
          </t-col>
          <t-col :span="12">
            <t-form-item label="输出价格（元/百万）">
              <t-input-number v-model="form.out_price_per_m" :min="0" :step="0.01" />
            </t-form-item>
          </t-col>
        </t-row>
        <t-form-item label="启用">
          <t-switch v-model="form.enabled" />
        </t-form-item>
      </t-form>
    </t-dialog>

    <StepUpDialog v-model="stepUpVisible" @success="onStepUpSuccess" />
  </div>
</template>

<style scoped>
.header-actions {
  display: flex;
  gap: 8px;
}
.mb-16 {
  margin-bottom: 16px;
}
.name-cell {
  display: flex;
  align-items: center;
  gap: 6px;
}
.sampling-hint {
  margin: 0 0 12px;
  color: var(--td-text-color-secondary);
  font-size: 12px;
  line-height: 1.6;
}
</style>
