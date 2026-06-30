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

  generateChallenge: (topic: string) =>
    request<{ challenge_id: string; status: string; detail: string }>('POST', '/generate', { topic }),
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
