// HTTP 客户端封装：
//  - 统一注入 Authorization: Bearer <access_token>
//  - 写请求注入 X-CSRF-Token（双提交 Cookie 值，与后端 lanmei_csrf 一致）
//  - access token 过期时静默用 refresh token 刷新一次后重放原请求
//  - 会话复用 / 刷新失败时触发登出回调
import type { ApiErrorBody } from '@/types/api'

export const LS_ACCESS = 'lanmei_access_token'
export const LS_REFRESH = 'lanmei_refresh_token'
export const LS_CSRF = 'lanmei_csrf_token'

export const CSRF_COOKIE = 'lanmei_csrf'

export class ApiError extends Error {
  code: string
  status: number
  constructor(status: number, message: string, code = '') {
    super(message)
    this.name = 'ApiError'
    this.status = status
    this.code = code
  }
}

// 会话失效（SESSION_REUSED / 刷新失败）回调，由 stores/auth 注入
let onSessionExpired: (() => void) | null = null
export function setSessionExpiredHandler(fn: () => void) {
  onSessionExpired = fn
}

export function getAccessToken(): string {
  return localStorage.getItem(LS_ACCESS) ?? ''
}
export function getRefreshToken(): string {
  return localStorage.getItem(LS_REFRESH) ?? ''
}
export function setTokens(access: string, refresh: string) {
  localStorage.setItem(LS_ACCESS, access)
  localStorage.setItem(LS_REFRESH, refresh)
}
export function clearTokens() {
  localStorage.removeItem(LS_ACCESS)
  localStorage.removeItem(LS_REFRESH)
  localStorage.removeItem(LS_CSRF)
}

/** 从 Cookie 解析 CSRF 值（HTTPOnly=false，前端可读） */
export function readCsrfFromCookie(): string {
  const m = document.cookie
    .split(';')
    .map((s) => s.trim())
    .find((s) => s.startsWith(`${CSRF_COOKIE}=`))
  return m ? decodeURIComponent(m.slice(CSRF_COOKIE.length + 1)) : ''
}

/** 刷新 access token（公开端点，无需 CSRF） */
async function refreshAccessToken(): Promise<boolean> {
  const refresh = getRefreshToken()
  if (!refresh) return false
  try {
    const res = await fetch('/api/auth/refresh', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ refresh_token: refresh }),
      credentials: 'same-origin',
    })
    const data = await res.json().catch(() => ({}))
    if (!res.ok) {
      if (data?.code === 'SESSION_REUSED') onSessionExpired?.()
      return false
    }
    setTokens(data.access_token, data.refresh_token)
    localStorage.setItem(LS_CSRF, readCsrfFromCookie())
    return true
  } catch {
    return false
  }
}

interface RequestOptions {
  method?: string
  body?: unknown
  /** 携带 step-up token（高危操作） */
  stepUpToken?: string
  /** 原始响应（不 JSON 解析），用于流式读取 */
  raw?: boolean
  /** 禁止自动刷新重放 */
  noRefresh?: boolean
  /** 外部中断信号（SSE 取消） */
  signal?: AbortSignal
}

/** 基础请求：注入鉴权与 CSRF，401 时自动刷新一次后重放 */
export async function request<T>(path: string, opts: RequestOptions = {}): Promise<T> {
  const { method = 'GET', body, stepUpToken, raw, noRefresh, signal } = opts

  const headers: Record<string, string> = {}
  const token = getAccessToken()
  if (token) headers['Authorization'] = `Bearer ${token}`
  if (stepUpToken) headers['X-Step-Up-Token'] = stepUpToken

  const isWrite = method !== 'GET' && method !== 'HEAD'
  if (isWrite) {
    const csrf = localStorage.getItem(LS_CSRF) ?? readCsrfFromCookie()
    if (csrf) headers['X-CSRF-Token'] = csrf
  }
  // FormData（文件上传）不能手动指定 Content-Type：需要浏览器补齐 multipart boundary
  const isForm = body instanceof FormData
  if (body !== undefined && !isForm) headers['Content-Type'] = 'application/json'

  const doFetch = () =>
    fetch(path, {
      method,
      headers,
      body: body === undefined ? undefined : isForm ? (body as FormData) : JSON.stringify(body),
      credentials: 'same-origin',
      signal,
    })

  let res = await doFetch()

  if (res.status === 401 && !noRefresh) {
    if (await refreshAccessToken()) {
      const t = getAccessToken()
      if (t) headers['Authorization'] = `Bearer ${t}`
      res = await doFetch()
    } else {
      onSessionExpired?.()
    }
  }

  if (raw) {
    if (!res.ok) throw await parseError(res)
    return res as unknown as T
  }

  if (!res.ok) throw await parseError(res)
  return (await res.json()) as T
}

async function parseError(res: Response): Promise<ApiError> {
  let body: ApiErrorBody | undefined
  try {
    body = (await res.json()) as ApiErrorBody
  } catch {
    // 非 JSON 响应
  }
  return new ApiError(res.status, body?.error ?? `请求失败（${res.status}）`, body?.code ?? '')
}

/** 读取 SSE 事件流（带鉴权头，用于 Trace 实时推送） */
export function openSSE<T>(
  path: string,
  onEvent: (name: string, data: T) => void,
  onError?: (err: Error) => void,
): () => void {
  const ctrl = new AbortController()
  let closed = false
  const close = () => {
    if (!closed) {
      closed = true
      ctrl.abort()
    }
  }

  ;(async () => {
    try {
      const res = await request<Response>(path, { raw: true, noRefresh: true, signal: ctrl.signal })
      const reader = (res as unknown as Response).body?.getReader()
      if (!reader) throw new Error('SSE 响应无 body')
      const decoder = new TextDecoder()
      let buf = ''
      for (;;) {
        const { done, value } = await reader.read()
        if (done) break
        buf += decoder.decode(value, { stream: true })
        // SSE 事件以空行分隔
        let idx: number
        while ((idx = buf.indexOf('\n\n')) >= 0) {
          const block = buf.slice(0, idx)
          buf = buf.slice(idx + 2)
          const event = parseSSEBlock(block)
          if (event) onEvent(event.name, JSON.parse(event.data) as T)
        }
      }
      // 服务端正常关闭流（非本地 abort）→ 交由上层决定是否重连
      if (!closed) onError?.(new Error('SSE 流被服务端关闭'))
    } catch (err) {
      if (!closed) onError?.(err instanceof Error ? err : new Error(String(err)))
    }
  })()

  return close
}

/** 解析单个 SSE 块（data:/event: 行） */
function parseSSEBlock(block: string): { name: string; data: string } | null {
  let name = 'message'
  let data = ''
  for (const line of block.split('\n')) {
    if (line.startsWith(':')) continue // 注释（心跳）
    if (line.startsWith('event:')) name = line.slice(6).trim()
    else if (line.startsWith('data:')) data = line.slice(5).trim()
  }
  if (!data) return null
  return { name, data }
}

/** 会话复用检测：返回 true 表示需要清除本地凭据 */
export function isSessionReused(err: unknown): boolean {
  return err instanceof ApiError && err.code === 'SESSION_REUSED'
}
