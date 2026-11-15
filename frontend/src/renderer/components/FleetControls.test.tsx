import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { FleetControls } from "./FleetControls";
import { SidebarProvider } from "./ui/sidebar";
import { TooltipProvider } from "./ui/tooltip";

const getNodes = vi.hoisted(() => vi.fn());
vi.mock("../lib/api-client", () => ({ apiClient: { GET: getNodes }, apiErrorMessage: (_error: unknown, fallback: string) => fallback, getApiBaseUrl: () => "https://fleet.test" }));
vi.mock("../hooks/useAgentReadinessQuery", () => ({ useAgentReadinessQuery: () => ({
	isPending: false, isError: false, refetch: vi.fn(),
	data: { agents: [{ id: "codex", installation: { state: "installed" }, authentication: { state: "authorized", freshness: "fresh" } }] },
}) }));

const nodes = [{
	id: "worker-a", name: "Worker A", role: "worker", presence: "online", availability: "always-on", capacity: 3, inUse: 1,
	capabilities: [
		{ harness: "pi", loggedIn: false, installation: "installed", authentication: "unknown", freshness: "unknown" },
		{ harness: "copilot", loggedIn: true, installation: "installed", authentication: "configured", freshness: "fresh", supportedModes: ["chat"] },
	],
}, {
	id: "worker-b", name: "Worker B", role: "worker", presence: "offline", availability: "on-demand", capacity: 1, inUse: 0,
	capabilities: [{ harness: "qwen", loggedIn: true, installation: "installed", authentication: "authorized", freshness: "fresh" }],
}];

beforeEach(() => { getNodes.mockReset().mockResolvedValue({ data: { nodes } }); });
afterEach(() => { vi.unstubAllGlobals(); });
async function openFleet() {
	render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><TooltipProvider><SidebarProvider><ul><FleetControls /></ul></SidebarProvider></TooltipProvider></QueryClientProvider>);
	await userEvent.click(screen.getByRole("button", { name: "Fleet" }));
	await screen.findByText("Worker A");
	return screen.getByRole("dialog");
}

describe("Fleet observations", () => {
	it("keeps coordinator inventory separate and shows installed worker agents with unknown or configured auth", async () => {
		const dialog = await openFleet();
		const coordinator = within(dialog).getByRole("region", { name: "Coordinator · service host" });
		expect(within(coordinator).getByText("Codex")).toBeInTheDocument();
		const worker = dialog.querySelector('[data-fleet-node="worker-a"]') as HTMLElement;
		expect(within(worker).getByText("Pi")).toBeInTheDocument();
		expect(within(worker).getByText("Authorization unknown · Observation freshness unknown")).toBeInTheDocument();
		expect(within(worker).getByText("Configured · Fresh observation")).toBeInTheDocument();
		expect(within(worker).getByText("1 / 3 slots in use")).toBeInTheDocument();
		expect(getNodes).toHaveBeenCalledWith("/api/v1/fleet/nodes");
	});
	it("marks offline observations stale even if the last worker report was fresh", async () => {
		const dialog = await openFleet();
		const worker = dialog.querySelector('[data-fleet-node="worker-b"]') as HTMLElement;
		expect(within(worker).getByText("Offline")).toBeInTheDocument();
		expect(within(worker).getByText("Authorized · Stale observation")).toBeInTheDocument();
		expect(within(worker).queryByText(/Fresh observation/)).not.toBeInTheDocument();
		expect(within(dialog).getByText(/Install and authorize agents on their own machines/)).toBeInTheDocument();
	});
});
