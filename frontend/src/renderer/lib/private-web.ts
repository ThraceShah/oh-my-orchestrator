import { APP_SHORTCUTS, shortcutBindingValidationError, type AppShortcutId, type KeybindingOverrides, type ShortcutBinding } from "../../shared/shortcuts";
import { coerceUiSettings, type UiSettings } from "../../shared/ui-locale";

/** The live private web app reuses the desktop renderer without its account or native features. */
export function isPrivateWeb(): boolean {
	return import.meta.env.VITE_AO_WEB === "1";
}

const UI_SETTINGS_KEY = "ao:private-web:ui-settings";
const KEYBINDINGS_KEY = "ao:private-web:keybindings";

function readPreference(key: string): unknown {
	try {
		return JSON.parse(localStorage.getItem(key) ?? "null");
	} catch {
		return null;
	}
}

export function readWebUiSettings(): UiSettings {
	return coerceUiSettings(readPreference(UI_SETTINGS_KEY));
}

export function writeWebUiSettings(patch: Partial<UiSettings>): UiSettings {
	const settings = coerceUiSettings({ ...readWebUiSettings(), ...patch });
	// Let write failures reach the existing save-error UI instead of claiming success.
	localStorage.setItem(UI_SETTINGS_KEY, JSON.stringify(settings));
	return settings;
}

export function privateWebShortcutAvailable(id: AppShortcutId): boolean {
	return !isPrivateWeb() || !["dictate", "open-in-editor", "toggle-browser-devtools"].includes(id);
}

function coerceWebKeybindings(raw: unknown): KeybindingOverrides {
	if (!raw || typeof raw !== "object" || Array.isArray(raw)) return {};
	const source = raw as Record<string, unknown>;
	const result: KeybindingOverrides = {};
	const isMac = /Mac|iPhone|iPad/.test(navigator.platform);
	for (const shortcut of APP_SHORTCUTS) {
		if (shortcut.customizable === false) continue;
		const saved = source[shortcut.id];
		if (!Array.isArray(saved)) continue;
		const bindings: ShortcutBinding[] = [];
		for (const value of saved.slice(0, 2)) {
			if (!value || typeof value !== "object" || typeof value.key !== "string" || !value.key || value.key.length > 32) continue;
			const binding: ShortcutBinding = {
				key: value.key,
				...(typeof value.code === "string" && value.code.length > 0 && value.code.length <= 32 ? { code: value.code } : {}),
				ctrl: value.ctrl === true,
				meta: value.meta === true,
				shift: value.shift === true,
				alt: value.alt === true,
			};
			if (!shortcutBindingValidationError(binding, isMac)) bindings.push(binding);
		}
		if (saved.length === 0 || bindings.length > 0) result[shortcut.id] = bindings;
	}
	return result;
}

export function readWebKeybindings(): KeybindingOverrides {
	return coerceWebKeybindings(readPreference(KEYBINDINGS_KEY));
}

export function writeWebKeybindings(overrides: KeybindingOverrides): KeybindingOverrides {
	const next = coerceWebKeybindings(overrides);
	localStorage.setItem(KEYBINDINGS_KEY, JSON.stringify(next));
	return next;
}
