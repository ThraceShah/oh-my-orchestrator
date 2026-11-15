import { useQuery } from "@tanstack/react-query";
import { apiClient } from "../lib/api-client";
import { apiErrorException } from "../lib/api-error-exception";
import { STANDALONE_WORKSPACE_ID } from "../types/workspace";

/** Human web tasks always enter the project's existing service-host coordinator. */
export function useProjectCoordinator(projectId?: string) {
	return useQuery({
		queryKey: ["project-coordinator", projectId ?? ""],
		enabled: import.meta.env.VITE_AO_WEB === "1" && Boolean(projectId) && projectId !== STANDALONE_WORKSPACE_ID,
		staleTime: 0,
		retry: false,
		queryFn: async () => {
			const { data, error } = await apiClient.GET("/api/v1/orchestrators");
			if (error) throw apiErrorException(error);
			return data?.sessions.find((session) => session.projectId === projectId && !session.isTerminated) ?? null;
		},
	});
}
