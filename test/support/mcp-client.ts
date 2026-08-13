import { spawn, type ChildProcessWithoutNullStreams } from "node:child_process";
import { createHmac } from "node:crypto";
import { createInterface } from "node:readline";

type JSONRPCRequest = {
	jsonrpc: "2.0";
	id?: number;
	method: string;
	params?: unknown;
};

type JSONRPCResponse = {
	jsonrpc: "2.0";
	id?: number | string | null;
	result?: unknown;
	error?: { code: number; message: string; data?: unknown };
};

type PendingRequest = {
	resolve: (value: unknown) => void;
	reject: (reason: Error) => void;
};

const totpAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";

export function totpCode(secret: string, at = Date.now()): string {
	const normalized = secret.replace(/=+$/, "").toUpperCase();
	const bytes: number[] = [];
	let bits = 0;
	let accumulator = 0;
	for (const character of normalized) {
		accumulator = (accumulator << 5) | totpAlphabet.indexOf(character);
		bits += 5;
		if (bits >= 8) {
			bytes.push((accumulator >>> (bits - 8)) & 0xff);
			bits -= 8;
		}
	}
	const counter = Buffer.alloc(8);
	counter.writeBigUInt64BE(BigInt(Math.floor(at / 30_000)));
	const hash = createHmac("sha1", Buffer.from(bytes)).update(counter).digest();
	const offset = hash[hash.length - 1] & 0x0f;
	const value =
		((hash[offset] & 0x7f) << 24) |
		((hash[offset + 1] & 0xff) << 16) |
		((hash[offset + 2] & 0xff) << 8) |
		(hash[offset + 3] & 0xff);
	return String(value % 1_000_000).padStart(6, "0");
}

// Registers a fresh user and exchanges the returned TOTP secret for a JWT.
// The token is used only to drive the local connector and the ownership check.
export async function registerAndGetToken(
	baseURL: string,
	username: string,
	password: string,
): Promise<string> {
	const registerResponse = await fetch(`${baseURL}/api/auth/register`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({ username, password }),
	});
	if (!registerResponse.ok) {
		throw new Error(`register failed: ${await registerResponse.text()}`);
	}
	const registration = (await registerResponse.json()) as { totp_secret?: unknown };
	if (typeof registration.totp_secret !== "string" || registration.totp_secret === "") {
		throw new Error("register response is missing totp_secret");
	}
	const loginResponse = await fetch(`${baseURL}/api/auth/login`, {
		method: "POST",
		headers: { "Content-Type": "application/json" },
		body: JSON.stringify({
			username,
			password,
			totp_code: totpCode(registration.totp_secret),
		}),
	});
	if (!loginResponse.ok) {
		throw new Error(`login failed: ${await loginResponse.text()}`);
	}
	const login = (await loginResponse.json()) as { token?: unknown };
	if (typeof login.token !== "string" || login.token === "") {
		throw new Error("login response is missing token");
	}
	return login.token;
}

// MCPConnectorClient speaks newline-delimited JSON-RPC over stdio with the
// local breakfix-mcp binary. It is intentionally minimal: initialize, tool
// listing, and tool calls are all the acceptance path needs.
export class MCPConnectorClient {
	private readonly process: ChildProcessWithoutNullStreams;
	private nextID = 1;
	private readonly pending = new Map<number, PendingRequest>();
	private exited = false;

	constructor(
		private readonly binaryPath: string,
		private readonly configPath: string,
		token: string,
	) {
		this.process = spawn(binaryPath, ["-config", configPath], {
			env: { ...process.env, BREAKFIX_E2E_MCP_TOKEN: token },
			stdio: ["pipe", "pipe", "pipe"],
		});
		const lines = createInterface({ input: this.process.stdout });
		lines.on("line", (line) => {
			const trimmed = line.trim();
			if (!trimmed) return;
			let message: JSONRPCResponse;
			try {
				message = JSON.parse(trimmed) as JSONRPCResponse;
			} catch {
				return;
			}
			if (typeof message.id === "number") {
				const pending = this.pending.get(message.id);
				if (!pending) return;
				this.pending.delete(message.id);
				if (message.error) {
					pending.reject(
						new Error(
							`MCP ${message.error.code}: ${message.error.message}`,
						),
					);
				} else {
					pending.resolve(message.result);
				}
			}
		});
		this.process.on("exit", () => {
			this.exited = true;
			for (const pending of this.pending.values()) {
				pending.reject(new Error("breakfix-mcp exited unexpectedly"));
			}
			this.pending.clear();
		});
		this.process.stderr.on("data", () => {
			// stderr is connector diagnostics, never part of the MCP protocol.
		});
	}

	private send(message: JSONRPCRequest): void {
		if (this.exited) throw new Error("breakfix-mcp has already exited");
		this.process.stdin.write(`${JSON.stringify(message)}\n`);
	}

	request(method: string, params?: unknown): Promise<unknown> {
		const id = this.nextID++;
		return new Promise((resolve, reject) => {
			this.pending.set(id, { resolve, reject });
			this.send({ jsonrpc: "2.0", id, method, params });
		});
	}

	notify(method: string, params?: unknown): void {
		this.send({ jsonrpc: "2.0", method, params });
	}

	async initialize(): Promise<void> {
		await this.request("initialize", {
			protocolVersion: "2025-06-18",
			capabilities: {},
			clientInfo: { name: "breakfix-e2e", version: "0.0.0" },
		});
		this.notify("notifications/initialized", {});
	}

	async listTools(): Promise<Array<{ name: string }>> {
		const result = (await this.request("tools/list")) as {
			tools?: Array<{ name: string }>;
		};
		if (!Array.isArray(result.tools)) {
			throw new Error("tools/list did not return a tool list");
		}
		return result.tools;
	}

	async callTool(name: string, args: Record<string, unknown>): Promise<any> {
		const raw = (await this.request("tools/call", { name, arguments: args })) as {
			isError?: boolean;
			content?: Array<{ type: string; text?: string }>;
			structuredContent?: unknown;
		};
		if (raw.isError) {
			const detail = raw.content
				?.filter((entry) => entry.type === "text")
				.map((entry) => entry.text)
				.join("\n");
			throw new Error(`MCP tool ${name} failed: ${detail ?? "unknown error"}`);
		}
		if (raw.structuredContent !== undefined) return raw.structuredContent;
		const text = raw.content?.find((entry) => entry.type === "text")?.text;
		if (text === undefined) return undefined;
		try {
			return JSON.parse(text) as unknown;
		} catch {
			return text;
		}
	}

	async close(): Promise<void> {
		if (this.exited) return;
		const exited = new Promise<void>((resolve) => {
			this.process.once("exit", () => resolve());
		});
		this.process.kill();
		await exited;
	}
}
