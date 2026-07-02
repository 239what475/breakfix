const BASE = '/api'

export function token(): string | null {
  return localStorage.getItem('token')
}

export function setToken(t: string) {
  localStorage.setItem('token', t)
}

export function clearToken() {
  localStorage.removeItem('token')
}

export function isLoggedIn(): boolean {
  return !!token()
}

async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { 'Content-Type': 'application/json' }
  const tok = token()
  if (tok) headers['Authorization'] = `Bearer ${tok}`

  const res = await fetch(BASE + path, {
    method,
    headers,
    body: body ? JSON.stringify(body) : undefined,
  })

  const data = await res.json()
  if (!res.ok) throw new Error(data.error || `${res.status}`)
  return data as T
}

async function multipartRequest<T>(path: string, form: FormData): Promise<T> {
  const headers: Record<string, string> = {}
  const tok = token()
  if (tok) headers['Authorization'] = `Bearer ${tok}`

  const res = await fetch(BASE + path, {
    method: 'POST',
    headers,
    body: form,
  })

  const data = await res.json()
  if (!res.ok) throw new Error(data.error || `${res.status}`)
  return data as T
}

export const api = {
  register: (username: string, password: string) =>
    request<{ totp_secret: string; totp_url: string }>('POST', '/auth/register', { username, password }),

  login: (username: string, password: string, totp_code: string) =>
    request<{ token: string; user_id: string; name: string }>('POST', '/auth/login', { username, password, totp_code }),

  listChallenges: () =>
    request<{ challenges: Challenge[] }>('GET', '/challenges'),

  startChallenge: (id: string) =>
    request<{ challenge_title: string }>('POST', `/challenges/${id}/start`),

  submitChallenge: (id: string) =>
    request<{ passed: boolean; exit_code: number; output: string }>('POST', `/challenges/${id}/submit`),

  resetChallenge: (id: string) =>
    request<{ challenge_title: string }>('POST', `/challenges/${id}/reset`),

  reviewGenerationDraft: (topic: string) =>
    request<GenerateDraftResponse>('POST', '/generate/draft', { topic }),

  createGenerationJob: (draft: ChallengeDraft) =>
    request<GenerationJobResponse>('POST', '/generate', { draft }),

  getGenerationJob: (id: string) =>
    request<GenerationJobResponse>('GET', `/generate/jobs/${id}`),

  createVerifySubmission: (artifact: File) => {
    const form = new FormData()
    form.append('artifact', artifact)
    return multipartRequest<VerifySubmissionResponse>('/verify/submissions', form)
  },

  getVerifyTask: (id: string) =>
    request<VerifyTaskResponse>('GET', `/verify/tasks/${id}`),
}

export interface Challenge {
  id: string
  title: string
  type: string
  difficulty: string
  tags: string[]
  description: string
  solved: boolean
  active: boolean
}

export interface ChallengeDraft {
  title: string
  difficulty: string
  tags: string[]
  description: string
  goal: string
  symptoms: string
  fault_mechanism: string
  environment_shape: string
  acceptance_criteria: string
  difficulty_reason: string
  notes?: string
}

export interface GenerateDraftResponse {
  status: string
  verdict: string
  reason: string
  warnings?: string[]
  draft?: ChallengeDraft
}

export interface GenerationJobResponse {
  job_id?: string
  challenge_id?: string
  status: string
  message: string
  started_at?: string
  completed_at?: string
}

export interface VerifySubmissionResponse {
  verify_task_id?: string
  submission_id?: string
  status: string
}

export interface VerifyIssue {
  code?: string
  message?: string
}

export interface VerifyReport {
  build_passed?: boolean
  answer_passed?: boolean
  verify_passed?: boolean
  summary?: string
  issues?: VerifyIssue[]
}

export interface VerifyTaskResponse {
  verify_task_id?: string
  submission_id?: string
  status: string
  message: string
  started_at?: string
  completed_at?: string
  report?: VerifyReport
}
