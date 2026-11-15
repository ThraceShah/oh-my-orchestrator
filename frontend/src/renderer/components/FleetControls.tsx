import { useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { Monitor, Zap, RefreshCw, Server } from "@/components/app-icons";
import { useTranslation } from "react-i18next";
import { useAgentReadinessQuery } from "../hooks/useAgentReadinessQuery";
import { apiClient, getApiBaseUrl } from "../lib/api-client";
import { apiErrorException } from "../lib/api-error-exception";
import type { components } from "../../api/schema";
import { agentLabel, type AgentId } from "../lib/agent-options";
import { useUiStore } from "../stores/ui-store";
import { ConfirmDialog } from "./ConfirmDialog";
import { Button } from "./ui/button";
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle, DialogTrigger } from "./ui/dialog";
import { SidebarMenuButton, SidebarMenuItem } from "./ui/sidebar";

type FleetAgent = components["schemas"]["FleetAgentCapability"];
type FleetNode = components["schemas"]["FleetNodeSummary"];

function FleetAgentStatus({ capability, offline = false }: { capability: FleetAgent; offline?: boolean }) {
	const { t } = useTranslation();
	// Older workers report only a boolean. False does not distinguish unknown
	// credentials from a confirmed sign-out, and a heartbeat is not an auth probe.
	const auth = capability.authentication || (capability.loggedIn === true ? "authorized" : "unknown");
	const authLabels: Record<string, string> = {
		authorized: t("fleet.auth.authorized", { defaultValue: "Authorized" }),
		configured: t("fleet.auth.configured", { defaultValue: "Configured" }),
		not_applicable: t("fleet.auth.notRequired", { defaultValue: "No authorization required" }),
		unauthorized: t("fleet.auth.unauthorized", { defaultValue: "Not authorized" }),
		unknown: t("fleet.auth.unknown", { defaultValue: "Authorization unknown" }),
	};
	const freshness = offline ? "stale" : capability.freshness || "unknown";
	const freshnessLabel = freshness === "fresh"
		? t("fleet.freshness.fresh", { defaultValue: "Fresh observation" })
		: freshness === "stale"
			? t("fleet.freshness.stale", { defaultValue: "Stale observation" })
			: t("fleet.freshness.unknown", { defaultValue: "Observation freshness unknown" });
	return <div className="flex min-w-0 flex-wrap items-baseline gap-x-2 gap-y-0.5 py-1 text-xs" data-fleet-agent={capability.harness}>
		<span className="font-medium text-foreground">{agentLabel(capability.harness as AgentId) || capability.harness}</span>
		<span className="text-muted-foreground">{authLabels[auth] || authLabels.unknown} · {freshnessLabel}</span>
		{capability.supportedModes?.length ? <span className="text-muted-foreground">{capability.supportedModes.join(" / ")}</span> : null}
		{capability.checkedAt ? <time className="text-muted-foreground" dateTime={capability.checkedAt}>{capability.checkedAt}</time> : null}
	</div>;
}

/** Coordinator inventory is local; worker inventory is reported by each owner.
 * This is observation and power control, never a human worker-spawn picker. */
export function FleetControls() {
	const { t } = useTranslation();
	const [open, setOpen] = useState(false);
	const [error, setError] = useState("");
	const [busy, setBusy] = useState("");
	const [shutdownNode, setShutdownNode] = useState<FleetNode | null>(null);
	const openSettings = useUiStore((state) => state.openGlobalSettings);
	const local = useAgentReadinessQuery(open);
	const fleet = useQuery({
		queryKey: ["fleet-nodes"],
		enabled: open,
		refetchInterval: open ? 15000 : false,
		queryFn: async (): Promise<FleetNode[]> => {
			const { data, error } = await apiClient.GET("/api/v1/fleet/nodes");
			if (error || !data) throw apiErrorException(error, t("fleet.loadFailed", { defaultValue: "Could not read worker status." }));
			return data.nodes;
		},
	});
	const nodes = (fleet.data || []).filter((node) => node.role !== "control");
	async function power(id: string, action: "wake" | "shutdown") {
		setBusy(id);
		setError("");
		try {
			const response = await fetch(`${getApiBaseUrl()}/fleet/nodes/${encodeURIComponent(id)}/power`, {
				method: "POST", headers: { "content-type": "application/json" }, body: JSON.stringify({ action }),
			});
			if (!response.ok) {
				const body = await response.json();
				throw new Error(body.error?.message || t("fleet.powerFailed", { defaultValue: "The power action failed." }));
			}
			setShutdownNode(null);
			await fleet.refetch();
		} catch (failure) { setError(failure instanceof Error ? failure.message : String(failure)); }
		finally { setBusy(""); }
	}
	return <SidebarMenuItem>
		<Dialog open={open} onOpenChange={setOpen}>
			<DialogTrigger asChild><SidebarMenuButton tooltip={t("fleet.title", { defaultValue: "Fleet" })}><Server className="size-4" /><span>{t("fleet.title", { defaultValue: "Fleet" })}</span></SidebarMenuButton></DialogTrigger>
			<DialogContent className="max-h-[85svh] max-w-2xl overflow-y-auto">
				<DialogHeader><DialogTitle>{t("fleet.title", { defaultValue: "Fleet" })}</DialogTitle><DialogDescription>{t("fleet.description", { defaultValue: "The coordinator runs on the service host and assigns work to worker agents." })}</DialogDescription></DialogHeader>
				<section aria-label={t("fleet.coordinator", { defaultValue: "Coordinator · service host" })}>
					<div className="flex items-center justify-between gap-2"><h2 className="text-sm font-medium">{t("fleet.coordinator", { defaultValue: "Coordinator · service host" })}</h2><Button size="sm" variant="ghost" onClick={() => { setOpen(false); openSettings("harness"); }}>{t("settings.harness", { defaultValue: "Agents" })}</Button></div>
					{local.isPending ? <p className="text-xs text-muted-foreground">{t("fleet.loading", { defaultValue: "Loading observations…" })}</p> : null}
					{local.isError ? <p role="alert" className="text-xs text-destructive">{t("fleet.localFailed", { defaultValue: "Service-host Agent readiness is unavailable." })}</p> : null}
					{local.data?.agents.filter((agent) => agent.installation.state === "installed").map((agent) => <FleetAgentStatus key={agent.id} capability={{ harness: agent.id, loggedIn: agent.authentication.state === "authorized", installation: agent.installation.state, authentication: agent.authentication.state, freshness: agent.authentication.freshness, checkedAt: agent.authentication.checkedAt }} />)}
				</section>
				<section aria-label={t("fleet.workers", { defaultValue: "Workers" })}>
					<div className="flex items-center justify-between gap-2"><h2 className="text-sm font-medium">{t("fleet.workers", { defaultValue: "Workers" })}</h2><Button variant="ghost" size="sm" disabled={fleet.isFetching} onClick={() => { void fleet.refetch(); void local.refetch(); }}><RefreshCw className="size-3.5" />{t("fleet.refresh", { defaultValue: "Refresh" })}</Button></div>
					{fleet.isPending ? <p className="text-xs text-muted-foreground">{t("fleet.loading", { defaultValue: "Loading observations…" })}</p> : null}
					{fleet.isError ? <p role="alert" className="text-xs text-destructive">{fleet.error.message}</p> : null}
					{fleet.isSuccess && nodes.length === 0 ? <p className="text-xs text-muted-foreground">{t("fleet.noWorkers", { defaultValue: "No workers are registered." })}</p> : null}
					<div className="divide-y divide-border">{nodes.map((node) => <div key={node.id} className="py-3" data-fleet-node={node.id}>
						<div className="flex items-start gap-2"><Monitor className="mt-0.5 size-4 shrink-0 text-muted-foreground" /><div className="min-w-0 flex-1">
							<div className="flex flex-wrap items-center gap-2 text-sm font-medium">{node.name || node.id}<span className="text-xs font-normal text-muted-foreground">{node.presence === "online" ? t("fleet.online", { defaultValue: "Online" }) : node.presence === "offline" ? t("fleet.offline", { defaultValue: "Offline" }) : t("fleet.presenceUnknown", { defaultValue: "Connection unknown" })}</span></div>
							<div className="mt-1 text-xs text-muted-foreground">{node.platforms?.join(" · ")}{node.platforms?.length ? " · " : ""}{typeof node.inUse === "number" ? t("fleet.capacity", { defaultValue: "{{inUse}} / {{capacity}} slots in use", inUse: node.inUse, capacity: node.capacity }) : t("fleet.capacityUnknown", { defaultValue: "Capacity {{capacity}} · usage unknown", capacity: node.capacity })}</div>
						</div>{node.availability === "on-demand" ? <div className="flex shrink-0 gap-1"><Button size="sm" variant="outline" disabled={busy === node.id} onClick={() => void power(node.id, "wake")}><Zap className="size-3.5" />{t("fleet.wake", { defaultValue: "Wake" })}</Button><Button size="sm" variant="ghost" disabled={busy === node.id} onClick={() => setShutdownNode(node)}>{t("fleet.shutdown", { defaultValue: "Shut down" })}</Button></div> : null}</div>
						<div className="mt-2 pl-6">{node.capabilities?.filter((agent) => agent.installation !== "not_installed").length ? node.capabilities.filter((agent) => agent.installation !== "not_installed").map((agent) => <FleetAgentStatus key={agent.harness} capability={agent} offline={node.presence !== "online"} />) : <p className="text-xs text-muted-foreground">{t("fleet.noAgentObservation", { defaultValue: "No installed Agent inventory reported." })}</p>}</div>
					</div>)}</div>
				</section>
				<p className="text-xs text-muted-foreground">{t("fleet.manualSetup", { defaultValue: "Install and authorize agents on their own machines. Offline or stale observations do not confirm that an agent can start now." })}</p>
				{error ? <p role="alert" className="text-sm text-destructive">{error}</p> : null}
			</DialogContent>
		</Dialog>
		<ConfirmDialog open={shutdownNode !== null} title={t("fleet.shutdownTitle", { defaultValue: "Shut down worker?" })} description={shutdownNode?.name || shutdownNode?.id || ""} confirmLabel={t("fleet.shutdown", { defaultValue: "Shut down" })} destructive busy={busy !== ""} error={error} onConfirm={() => { if (shutdownNode) void power(shutdownNode.id, "shutdown"); }} onOpenChange={(next) => { if (!next) setShutdownNode(null); }} />
	</SidebarMenuItem>;
}
