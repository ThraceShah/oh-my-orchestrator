import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { isPrivateWeb, privateWebShortcutAvailable, readWebKeybindings, readWebUiSettings, writeWebKeybindings, writeWebUiSettings } from "./private-web";

beforeEach(() => { localStorage.clear(); vi.stubEnv("VITE_AO_WEB", "1"); });
afterEach(() => { localStorage.clear(); vi.unstubAllEnvs(); vi.restoreAllMocks(); });

describe("private web preferences", () => {
	it("preserves locale, sound and shell preferences across separate partial updates", () => {
		writeWebUiSettings({ locale: "zh-CN", terminalShell: { kind: "custom", path: "/bin/bash" } });
		writeWebUiSettings({ soundNotificationsEnabled: false });
		expect(readWebUiSettings()).toEqual({ locale: "zh-CN", soundNotificationsEnabled: false, terminalShell: { kind: "custom", path: "/bin/bash" } });
	});
	it("recovers defaults from corrupt or unsupported saved settings", () => {
		localStorage.setItem("ao:private-web:ui-settings", "broken json");
		expect(readWebUiSettings().locale).toBe("en");
		localStorage.setItem("ao:private-web:ui-settings", JSON.stringify({ locale: "bad", terminalShell: { kind: "unknown" } }));
		expect(readWebUiSettings()).toEqual({ locale: "en", soundNotificationsEnabled: true, terminalShell: { kind: "auto" } });
	});
	it("persists validated shortcut bindings and intentional unassigned shortcuts", () => {
		const binding = { key: "y", ctrl: true, meta: false, shift: true, alt: false };
		writeWebKeybindings({ "toggle-terminal": [binding], "open-settings": [] });
		expect(readWebKeybindings()).toEqual({ "toggle-terminal": [binding], "open-settings": [] });
	});
	it("ignores unknown shortcuts and corrupt bindings instead of overriding defaults", () => {
		localStorage.setItem("ao:private-web:keybindings", JSON.stringify({ unknown: [{ key: "x" }], "toggle-terminal": [{ key: "" }], "open-settings": [] }));
		expect(readWebKeybindings()).toEqual({ "open-settings": [] });
	});
	it("reports unavailable storage instead of claiming preferences were saved", () => {
		vi.spyOn(window.localStorage, "setItem").mockImplementation(() => { throw new Error("Storage unavailable"); });
		expect(() => writeWebUiSettings({ locale: "ja" })).toThrow("Storage unavailable");
		expect(() => writeWebKeybindings({})).toThrow("Storage unavailable");
	});
	it("hides native shortcuts only for private web and preserves desktop availability", () => {
		expect(isPrivateWeb()).toBe(true);
		expect(privateWebShortcutAvailable("dictate")).toBe(false);
		expect(privateWebShortcutAvailable("open-settings")).toBe(true);
		vi.stubEnv("VITE_AO_WEB", "0");
		expect(privateWebShortcutAvailable("dictate")).toBe(true);
	});
});
