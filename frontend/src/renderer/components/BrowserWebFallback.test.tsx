import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { BrowserWebFallback } from "./BrowserWebFallback";

describe("browser preview without the native webview", () => {
	it("offers a real external link and never claims the target loaded or tests passed", () => {
		render(<BrowserWebFallback url="https://preview.example.com/app" />);
		expect(screen.getByRole("link", { name: "Open in a new tab" })).toHaveAttribute("href", "https://preview.example.com/app");
		expect(screen.getByText(/has not been loaded here/)).toBeInTheDocument();
		expect(screen.queryByText("Loaded")).not.toBeInTheDocument();
		expect(screen.queryByText("12 passing")).not.toBeInTheDocument();
	});
	it("does not open a worker localhost URL on the user's machine", () => {
		render(<BrowserWebFallback url="http://127.0.0.1:5173" />);
		expect(screen.queryByRole("link")).not.toBeInTheDocument();
		expect(screen.getByText(/belongs to the execution host/)).toBeInTheDocument();
	});
	it("never turns non-web schemes into clickable URLs", () => {
		render(<BrowserWebFallback url="javascript:alert(1)" />);
		expect(screen.queryByRole("link")).not.toBeInTheDocument();
	});
});
