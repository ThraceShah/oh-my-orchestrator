import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("./telemetry", () => ({ captureRendererEvent: vi.fn() }));
vi.mock("./sentry", () => ({ captureApiErrorToSentry: vi.fn() }));

const originalBridge = window.ao;

beforeEach(() => {
	vi.resetModules();
	window.ao = undefined;
	vi.stubEnv("VITE_AO_WEB", "1");
	vi.stubEnv("VITE_NO_ELECTRON", "0");
	vi.stubEnv("BASE_URL", "/apps/orchestrator/");
});

afterEach(() => {
	window.ao = originalBridge;
	vi.unstubAllEnvs();
	vi.restoreAllMocks();
});

describe("live browser renderer", () => {
	it("keeps real API requests and terminal sockets on the page origin and app prefix after a ready status", async () => {
		const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"sessions":[]}'));
		const { applyDaemonStatus } = await import("./daemon-status");
		const { apiClient, getApiBaseUrl } = await import("./api-client");
		const { muxUrlFromApiBase } = await import("./terminal-mux");
		applyDaemonStatus({ state: "ready", port: 3011 });
		const base = `${window.location.origin}/apps/orchestrator`;
		expect(getApiBaseUrl()).toBe(base);
		await apiClient.GET("/api/v1/sessions");
		const [input] = fetchMock.mock.calls[0];
		expect(input instanceof Request ? input.url : String(input)).toBe(`${base}/api/v1/sessions`);
		expect(muxUrlFromApiBase(getApiBaseUrl())).toBe(`${base.replace(/^http/, "ws")}/mux`);
	});

	it("probes the upstream readiness route and reports the daemon port required by the shell", async () => {
		const fetchMock = vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response('{"status":"ready","port":3011,"pid":42}'));
		const { aoBridge } = await import("./bridge");
		await expect(aoBridge.daemon.getStatus()).resolves.toEqual({ state: "ready", port: 3011, pid: 42 });
		expect(fetchMock.mock.calls[0][0]).toBe(`${window.location.origin}/apps/orchestrator/readyz`);
	});

	it.each(['{"status":"ready"}', '{"status":"ok","port":3011}'])("does not mistake an incomplete probe for readiness: %s", async (body) => {
		vi.spyOn(globalThis, "fetch").mockResolvedValue(new Response(body));
		const { aoBridge } = await import("./bridge");
		await expect(aoBridge.daemon.getStatus()).resolves.toMatchObject({ state: "error", code: "daemon_unreachable" });
	});

	it("reports proxy outages without claiming the daemon started", async () => {
		vi.spyOn(globalThis, "fetch").mockRejectedValue(new TypeError("Failed to fetch"));
		const { aoBridge } = await import("./bridge");
		await expect(aoBridge.daemon.getStatus()).resolves.toMatchObject({ state: "error", code: "daemon_unreachable" });
		await expect(aoBridge.daemon.start()).rejects.toThrow("host machine");
	});

	it("preserves the preview fallback and desktop port behavior when live web mode is off", async () => {
		vi.stubEnv("VITE_AO_WEB", "0");
		const { aoBridge } = await import("./bridge");
		await expect(aoBridge.daemon.getStatus()).resolves.toMatchObject({ state: "stopped" });
		const { applyDaemonStatus } = await import("./daemon-status");
		const { getApiBaseUrl } = await import("./api-client");
		applyDaemonStatus({ state: "ready", port: 3011 });
		expect(getApiBaseUrl()).toBe("http://127.0.0.1:3011");
	});
});
