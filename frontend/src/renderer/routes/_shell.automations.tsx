import { createFileRoute, redirect } from "@tanstack/react-router";
import { AutomationsView } from "../components/AutomationsView";
import { isPrivateWeb } from "../lib/private-web";

export const Route = createFileRoute("/_shell/automations")({
	beforeLoad: () => {
		// Private work enters through the coordinator; this desktop form creates workers directly.
		if (isPrivateWeb()) throw redirect({ to: "/" });
	},
	component: AutomationsView,
});
