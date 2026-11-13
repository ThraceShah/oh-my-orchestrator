import { aoBridge } from "./bridge";
import { setApiBaseUrl, setApiDaemonStatus } from "./api-client";

export type DaemonStatus = Awaited<ReturnType<typeof aoBridge.daemon.getStatus>>;

export function applyDaemonStatus(nextStatus: DaemonStatus): void {
	setApiDaemonStatus(nextStatus);
	if (import.meta.env.VITE_AO_WEB === "1") {
		// Web requests stay behind the same-origin proxy; the reported daemon
		// port is a readiness fact, not an address reachable by this browser.
		setApiBaseUrl(null);
	} else if (nextStatus.state === "ready" && nextStatus.port) {
		setApiBaseUrl(`http://127.0.0.1:${nextStatus.port}`);
	} else {
		setApiBaseUrl(null);
	}
}

export async function refreshDaemonStatus(): Promise<DaemonStatus> {
	const nextStatus = await readDaemonStatus();
	applyDaemonStatus(nextStatus);
	return nextStatus;
}

export function readDaemonStatus(): Promise<DaemonStatus> {
	return aoBridge.daemon.getStatus();
}
