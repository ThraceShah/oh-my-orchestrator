import { isRedirect } from "@tanstack/react-router";
import { afterEach, describe, expect, it, vi } from "vitest";
import { Route } from "../routes/_shell.automations";

vi.mock("../components/AutomationsView", () => ({ AutomationsView: () => null }));
afterEach(() => vi.unstubAllEnvs());

describe("automations entry policy", () => {
	it("redirects a direct private web URL before the worker-creating form can mount", () => {
		vi.stubEnv("VITE_AO_WEB", "1");
		let failure: unknown;
		try { Route.options.beforeLoad?.({} as never); } catch (error) { failure = error; }
		expect(isRedirect(failure)).toBe(true);
		if (isRedirect(failure)) expect(failure.options.to).toBe("/");
	});
	it("preserves the desktop automation route", () => {
		vi.stubEnv("VITE_AO_WEB", "0");
		expect(() => Route.options.beforeLoad?.({} as never)).not.toThrow();
	});
});
