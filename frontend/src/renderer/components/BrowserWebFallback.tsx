import { useTranslation } from "react-i18next";
import { isWebLink } from "../lib/external-link-policy";
import { isHostLocalWebLink } from "../lib/loopback";
import { Button } from "./ui/button";

/** Browsers cannot render the desktop's native webview or load a worker's localhost. */
export function BrowserWebFallback({ url }: { url: string }) {
	const { t } = useTranslation();
	const hostLocal = isHostLocalWebLink(url);
	const canOpen = isWebLink(url) && !hostLocal;
	return (
		<div className="absolute inset-0 flex flex-col items-center justify-center gap-3 overflow-auto bg-background p-5 text-center text-sm text-muted-foreground" data-testid="browser-web-fallback">
			<p>{t("browser.webPreviewUnavailable", { defaultValue: "The embedded browser requires the desktop app. This page has not been loaded here." })}</p>
			<p className="max-w-full break-all font-mono text-xs">{url}</p>
			{hostLocal ? <p>{t("browser.webPreviewHostLocal", { defaultValue: "This address belongs to the execution host. Publish a URL reachable from your browser before opening it." })}</p> : null}
			{canOpen ? <Button asChild variant="outline" size="sm"><a href={url} target="_blank" rel="noopener noreferrer">{t("browser.openInNewTab", { defaultValue: "Open in a new tab" })}</a></Button> : null}
		</div>
	);
}
