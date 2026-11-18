/// <reference types="node" />
// Runs the staff preview's real player script (internal/video/mpd_player.js) against the real page
// markup (internal/video/mpd_preview.html) in a DOM, with the server's answers stubbed.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";

const playerSource = readFileSync(resolve(process.cwd(), "../internal/video/mpd_player.js"), "utf8");
const pageBody = readFileSync(resolve(process.cwd(), "../internal/video/mpd_preview.html"), "utf8");

type Reply = { status: number; body?: unknown };
type Routes = Record<string, Reply | (() => Reply)>;

const CLOSED_STAFF_OS = "This preview has closed. Open it again from Staff OS.";
const CLOSED_SENDREC = "This preview has closed. Open the preview again.";
const ENDED = "Access ended. Sign in again and open a new preview.";
const UNAVAILABLE = "The preview isn't available right now. Try again.";
const HANDOFF = "#handoff=synthetic-handoff&nonce=synthetic-nonce&parentOrigin=" + encodeURIComponent("https://staff.example.test");

const redeemed = { previewToken: "synthetic-token", videoId: "synthetic", title: "Synthetic recording", recorded: "Recorded 18 Nov 2026", linkState: "Patients can open the link" };
const media = (linkState = "Patients can open the link") => ({ status: 200, body: { videoUrl: "https://media.example.test/clip.webm", transcriptUrl: "", linkState } });
const session = { status: 201, body: { playbackSessionId: "synthetic-session", mediaVersion: 1, expiresAt: new Date(Date.now() + 60000).toISOString() } };

let autoLoad = true;
const flush = async () => { for (let i = 0; i < 8; i++) await new Promise(resolve => setTimeout(resolve, 0)); };
const el = (id: string) => document.getElementById(id) as HTMLElement;
const player = () => el("player") as HTMLVideoElement;

function start(hash: string, routes: Routes, parent?: unknown) {
  document.body.innerHTML = pageBody;
  history.replaceState(null, "", "/mpd-preview" + hash);
  if (parent) vi.spyOn(window, "parent", "get").mockReturnValue(parent as Window);
  const fetchMock = vi.fn(async (path: string) => {
    const route = routes[path];
    const reply = typeof route === "function" ? route() : route ?? { status: 500 };
    return { ok: reply.status >= 200 && reply.status < 300, status: reply.status, json: async () => reply.body };
  });
  vi.stubGlobal("fetch", fetchMock);
  new Function(playerSource)();
  return fetchMock;
}

beforeEach(() => {
  autoLoad = true;
  vi.stubGlobal("requestAnimationFrame", (callback: FrameRequestCallback) => setTimeout(() => callback(0), 0));
  vi.spyOn(HTMLMediaElement.prototype, "pause").mockImplementation(() => {});
  vi.spyOn(HTMLMediaElement.prototype, "play").mockResolvedValue(undefined);
  vi.spyOn(HTMLMediaElement.prototype, "load").mockImplementation(function (this: HTMLMediaElement) {
    if (autoLoad && this.getAttribute("src")) queueMicrotask(() => this.dispatchEvent(new Event("loadedmetadata")));
  });
});
afterEach(() => { vi.restoreAllMocks(); vi.unstubAllGlobals(); });

const opened = (overrides: Routes = {}): Routes => ({
  "/api/mpd/preview/redeem": { status: 200, body: redeemed },
  "/api/mpd/preview/media": media(),
  "/api/mpd/preview/session": session,
  ...overrides,
});

describe("staff preview header", () => {
  it("names the recording as text, only once its media loads, in its own tab", async () => {
    autoLoad = false;
    const title = "<img src=x onerror=alert(1)> Synthetic";
    start(HANDOFF, opened({ "/api/mpd/preview/redeem": { status: 200, body: { ...redeemed, title } } }));
    await flush();
    expect(el("mpd-meta").classList.contains("shown")).toBe(false);
    expect(el("mpd-title").textContent).toBe("Staff preview");
    player().dispatchEvent(new Event("loadedmetadata"));
    await flush();
    expect(el("mpd-meta").classList.contains("shown")).toBe(true);
    expect(el("mpd-title").textContent).toBe(title);
    expect(el("mpd-meta").querySelector("img")).toBeNull();
    expect(el("mpd-detail").textContent).toBe("Recorded 18 Nov 2026 · Patients can open the link");
    expect(document.title).not.toContain("Synthetic");
    expect(location.hash).toBe("");
  });

  it("stays visually hidden when framed", async () => {
    const parent = { location: { origin: "https://staff.example.test" }, postMessage: vi.fn() };
    Object.defineProperty(parent, "location", { get: () => { throw new DOMException("cross-origin", "SecurityError"); } });
    start(HANDOFF, opened(), parent);
    await flush();
    expect(player().getAttribute("src")).toBeTruthy();
    expect(el("mpd-title").textContent).toBe("Synthetic recording");
    expect(el("mpd-meta").classList.contains("shown")).toBe(false);
  });

  it("follows the link state on each media renewal", async () => {
    let state = "Patients can open the link";
    start(HANDOFF, opened({ "/api/mpd/preview/media": () => media(state) }));
    await flush();
    state = "Patients can't open the link";
    player().dispatchEvent(new Event("error"));
    await flush();
    expect(el("mpd-detail").textContent).toBe("Recorded 18 Nov 2026 · Patients can't open the link");
  });

  it("clears the header and says access ended when a renewal is refused", async () => {
    let refuse = false;
    start(HANDOFF, opened({ "/api/mpd/preview/media": () => (refuse ? { status: 403 } : media()) }));
    await flush();
    refuse = true;
    player().dispatchEvent(new Event("error"));
    await flush();
    expect(el("mpd-status").textContent).toBe(ENDED);
    expect(el("mpd-meta").classList.contains("shown")).toBe(false);
    expect(el("mpd-title").textContent).toBe("Staff preview");
    expect(el("mpd-detail").textContent).toBe("");
    expect(player().getAttribute("src")).toBeNull();
  });

  it("shows the full-title toggle only while the title is longer than two lines", async () => {
    start(HANDOFF, opened());
    await flush();
    const title = el("mpd-title");
    let whole = 90;
    vi.spyOn(title, "getBoundingClientRect").mockReturnValue({ height: 45 } as DOMRect);
    Object.defineProperty(title, "scrollHeight", { configurable: true, get: () => whole });
    window.dispatchEvent(new Event("resize"));
    await flush();
    const toggle = el("mpd-more") as HTMLButtonElement;
    expect(toggle.hidden).toBe(false);
    toggle.click();
    await flush();
    expect(el("mpd-meta").classList.contains("full")).toBe(true);
    expect(toggle.textContent).toBe("Show less");
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(toggle.getAttribute("title")).toBeNull();
    // Wider window: the whole title now fits in two lines.
    whole = 45;
    window.dispatchEvent(new Event("resize"));
    window.dispatchEvent(new Event("resize"));
    await flush();
    expect(toggle.hidden).toBe(true);
    expect(el("mpd-meta").classList.contains("full")).toBe(false);
    expect(toggle.textContent).toBe("Show full title");
  });
});

describe("staff preview refusals", () => {
  it("says the preview has closed after a refresh, without asking the server", async () => {
    const fetchMock = start("", opened());
    await flush();
    expect(el("mpd-status").textContent).toBe(CLOSED_STAFF_OS);
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it.each([
    [410, CLOSED_STAFF_OS],
    [403, ENDED],
    [503, UNAVAILABLE],
  ])("tells staff what to do when the redeem answers %i", async (status, message) => {
    start(HANDOFF, opened({ "/api/mpd/preview/redeem": { status } }));
    await flush();
    expect(el("mpd-status").textContent).toBe(message);
    expect(el("mpd-meta").classList.contains("shown")).toBe(false);
  });

  it("does not mention Staff OS inside SendRec's own frame", async () => {
    const parent = { location: { origin: location.origin }, postMessage: vi.fn() };
    start(HANDOFF, opened({ "/api/mpd/preview/redeem": { status: 410 } }), parent);
    await flush();
    expect(el("mpd-status").textContent).toBe(CLOSED_SENDREC);
  });
});
