import type {
  AuthoringSession,
	AuthoringStreamComplete,
	AuthoringStreamEvent,
	AssistantConversation,
	AssistantMessageRequest,
	AssistantStreamComplete,
	AssistantStreamEvent,
	ScenarioContent,
	GeneratorGeneration,
	MySpace,
	MySpaceLearningPage,
} from "./types";
import type {
	ScenarioList,
	ScenarioProgress,
	CloseTerminalWindowResponse,
	GetMySpaceLearningData,
	LoginResponse,
	RegisterResponse,
	ResetResponse,
	StartResponse,
	StopResponse,
	TerminalTicketResponse,
} from "./generated";

export type MySpaceLearningQuery = NonNullable<GetMySpaceLearningData["query"]>;

const base = "/api";

export class APIError extends Error {
  readonly status: number;

  constructor(
    status: number,
    message: string,
  ) {
    super(message);
    this.name = "APIError";
    this.status = status;
  }
}

export function token(): string | null {
  return localStorage.getItem("token");
}

export function setToken(value: string) {
  localStorage.setItem("token", value);
}

export function clearToken() {
  localStorage.removeItem("token");
}

export function isLoggedIn() {
  return token() !== null;
}

export function tokenUserName(): string | undefined {
  const payload = token()?.split(".")[1];
  if (!payload) return undefined;
  try {
    const base64 = payload.replace(/-/g, "+").replace(/_/g, "/");
    const bytes = Uint8Array.from(atob(base64.padEnd(Math.ceil(base64.length / 4) * 4, "=")), (value) => value.charCodeAt(0));
    const value = JSON.parse(new TextDecoder().decode(bytes)) as { name?: unknown };
    return typeof value.name === "string" && value.name.trim() ? value.name : undefined;
  } catch {
    return undefined;
  }
}

async function request<T>(
  method: string,
  path: string,
  body?: unknown,
): Promise<T> {
  const headers: Record<string, string> = {};
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const currentToken = token();
  if (currentToken) headers.Authorization = `Bearer ${currentToken}`;

  const response = await fetch(base + path, {
    method,
    headers,
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  const data = await response.json().catch(() => ({}));
  if (!response.ok)
    throw new APIError(
      response.status,
      data.error || `Request failed (${response.status})`,
    );
  return data as T;
}

export interface AssistantStreamHandlers {
  onEvent: (event: AssistantStreamEvent) => void;
  onComplete: (value: AssistantStreamComplete) => void;
  onError: (message: string) => void;
}

export interface AuthoringStreamHandlers {
	onEvent: (event: AuthoringStreamEvent) => void;
	onComplete: (value: AuthoringStreamComplete) => void;
	onError: (message: string) => void;
}

async function consumeEventStream(
	response: Response,
	onEvent: (name: string, data: string) => boolean | void,
): Promise<void> {
	if (!response.body) throw new Error("Event stream is unavailable");
	const reader = response.body.getReader();
	const decoder = new TextDecoder();
  let buffer = "";
  while (true) {
    const { value, done } = await reader.read();
    buffer += decoder.decode(value, { stream: !done });
    const events = buffer.split("\n\n");
    buffer = events.pop() ?? "";
    for (const raw of events) {
		const name = raw.match(/^event: (.+)$/m)?.[1];
		const data = raw.match(/^data: (.+)$/m)?.[1];
		if (!name || !data) continue;
		if (onEvent(name, data)) return;
	}
	if (done) break;
	}
}

async function consumeAssistantStream(response: Response, handlers: AssistantStreamHandlers): Promise<void> {
	await consumeEventStream(response, (name, data) => {
		const parsed = JSON.parse(data) as AssistantStreamEvent | AssistantStreamComplete | { content?: string };
		if (name === "error") {
			handlers.onError((parsed as { content?: string }).content || "Assistant request failed");
			return true;
		}
		if (name === "complete") {
			handlers.onComplete(parsed as AssistantStreamComplete);
			return true;
		}
		handlers.onEvent({ ...(parsed as AssistantStreamEvent), type: name as AssistantStreamEvent["type"] });
	});
}

export async function streamAssistantMessage(
  id: string,
  body: AssistantMessageRequest,
  handlers: AssistantStreamHandlers,
  signal?: AbortSignal,
): Promise<void> {
  const headers: Record<string, string> = { "Content-Type": "application/json" };
  const currentToken = token();
  if (currentToken) headers.Authorization = `Bearer ${currentToken}`;
  const response = await fetch(`${base}/scenarios/${id}/assistant/messages`, {
    method: "POST",
    headers,
    body: JSON.stringify(body),
    signal,
  });
  if (!response.ok) {
    const data = await response.json().catch(() => ({}));
    throw new Error(data.error || `Request failed (${response.status})`);
  }
  await consumeAssistantStream(response, handlers);
}

export async function streamAuthoringMessage(
	id: string,
	content: string,
	idempotencyKey: string,
	handlers: AuthoringStreamHandlers,
	signal?: AbortSignal,
): Promise<void> {
	const headers: Record<string, string> = { "Content-Type": "application/json" };
	const currentToken = token();
	if (currentToken) headers.Authorization = `Bearer ${currentToken}`;
	const response = await fetch(`${base}/authoring/sessions/${id}/messages`, {
		method: "POST",
		headers,
		body: JSON.stringify({ content, idempotency_key: idempotencyKey }),
		signal,
	});
	if (!response.ok) {
		const data = await response.json().catch(() => ({}));
		throw new APIError(response.status, data.error || `Request failed (${response.status})`);
	}
	if (response.status === 202) {
		// The durable receipt already owns this run. The caller refreshes the
		// session instead of starting a second stream consumer.
		await response.json().catch(() => undefined);
		return;
	}
	await consumeEventStream(response, (name, data) => {
		const parsed = JSON.parse(data) as AuthoringStreamEvent | AuthoringStreamComplete | { content?: string };
		if (name === "error") {
			handlers.onError((parsed as { content?: string }).content || "Authoring request failed");
			return true;
		}
		if (name === "complete") {
			handlers.onComplete(parsed as AuthoringStreamComplete);
			return true;
		}
		handlers.onEvent({ ...(parsed as AuthoringStreamEvent), type: name as AuthoringStreamEvent["type"] });
	});
}

export const api = {
  register: (username: string, password: string) =>
    request<RegisterResponse>(
      "POST",
      "/auth/register",
      { username, password },
    ),
  login: (username: string, password: string, totp_code: string) =>
    request<LoginResponse>(
      "POST",
      "/auth/login",
      { username, password, totp_code },
    ),
  listScenarios: () =>
		request<ScenarioList>("GET", "/scenarios"),
	getMySpace: () => request<MySpace>("GET", "/me/space"),
	getMySpaceLearning: ({ cursor, limit = 20, state, runtime }: MySpaceLearningQuery = {}) => {
		const query = new URLSearchParams({ limit: String(limit) });
		if (cursor) query.set("cursor", cursor);
		if (state) query.set("state", state);
		if (runtime) query.set("runtime", runtime);
		return request<MySpaceLearningPage>("GET", `/me/space/learning?${query.toString()}`);
	},
  getScenarioContent: (id: string) =>
    request<ScenarioContent>("GET", `/scenarios/${id}/content`),
  getScenarioProgress: (id: string) =>
		request<ScenarioProgress>(
      "GET",
      `/scenarios/${id}/progress`,
    ),
  getScenarioAssistant: (id: string) =>
    request<AssistantConversation>("GET", `/scenarios/${id}/assistant`),
  startScenario: (id: string) =>
		request<StartResponse>("POST", `/scenarios/${id}/start`),
  resetScenario: (id: string) =>
		request<ResetResponse>("POST", `/scenarios/${id}/reset`),
  stopScenario: (id: string) =>
		request<StopResponse>("POST", `/scenarios/${id}/stop`),
	createTerminalTicket: (id: string, window: string, node?: string) =>
		request<TerminalTicketResponse>("POST", `/scenarios/${id}/terminal-ticket`, { window, node }),
  closeTerminalWindow: (id: string, window: string, node?: string) => {
		const query = new URLSearchParams();
		if (node) query.set("node", node);
		const suffix = query.size ? `?${query.toString()}` : "";
		return request<CloseTerminalWindowResponse>(
			"DELETE",
			`/scenarios/${id}/terminals/${window}${suffix}`,
		);
	},
  createAuthoringSession: () =>
    request<AuthoringSession>("POST", "/authoring/sessions"),
	createAuthoringScenarioRevision: (id: string) =>
		request<AuthoringSession>("POST", `/authoring/scenarios/${id}/revisions`),
	deprecateAuthoringScenario: async (id: string) => {
		await request<unknown>("POST", `/authoring/scenarios/${id}/deprecate`);
	},
  getCurrentAuthoringSession: () =>
    request<AuthoringSession>("GET", "/authoring/sessions/current"),
  getAuthoringSession: (id: string) =>
    request<AuthoringSession>("GET", `/authoring/sessions/${id}`),
  getGeneration: (workflowID: string) =>
    request<GeneratorGeneration>("GET", `/generator/workflows/${workflowID}`),
};
