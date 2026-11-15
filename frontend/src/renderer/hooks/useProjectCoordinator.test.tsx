import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { renderHook, waitFor } from "@testing-library/react";
import type { ReactNode } from "react";
import { afterEach, expect, it, vi } from "vitest";

const get = vi.hoisted(() => vi.fn());
vi.mock("../lib/api-client", () => ({ apiClient: { GET: get }, apiErrorMessage: (_error: unknown, message: string) => message }));
import { useProjectCoordinator } from "./useProjectCoordinator";

function wrapper({ children }: { children: ReactNode }) {
	return <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{children}</QueryClientProvider>;
}
afterEach(() => { get.mockReset(); vi.unstubAllEnvs(); });

it("finds only the active service-host coordinator for the chosen project", async () => {
	vi.stubEnv("VITE_AO_WEB", "1");
	get.mockResolvedValue({ data: { sessions: [
		{ id: "other", projectId: "other", isTerminated: false },
		{ id: "old", projectId: "project", isTerminated: true },
		{ id: "current", projectId: "project", isTerminated: false },
	] } });
	const { result } = renderHook(() => useProjectCoordinator("project"), { wrapper });
	await waitFor(() => expect(result.current.isSuccess).toBe(true));
	expect(result.current.data?.id).toBe("current");
	expect(get).toHaveBeenCalledWith("/api/v1/orchestrators");
});

it("does not query coordinators for a standalone web route or a desktop task", () => {
	vi.stubEnv("VITE_AO_WEB", "1");
	const first = renderHook(() => useProjectCoordinator("__standalone__"), { wrapper });
	expect(first.result.current.fetchStatus).toBe("idle");
	vi.stubEnv("VITE_AO_WEB", "0");
	const second = renderHook(() => useProjectCoordinator("project"), { wrapper });
	expect(second.result.current.fetchStatus).toBe("idle");
	expect(get).not.toHaveBeenCalled();
});
