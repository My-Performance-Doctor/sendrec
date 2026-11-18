/// <reference types="node" />
// Runs the staff preview's real player script (internal/video/mpd_player.js) against the real page
// markup (internal/video/mpd_preview.html), each test in a fresh jsdom window, so nothing one test's
// script leaves behind (listeners, timers) reaches another. The server's answers are stubbed.
import { afterEach, describe, expect, it, vi } from "vitest";
import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { JSDOM } from "jsdom";

const playerSource = readFileSync(resolve(process.cwd(), "../internal/video/mpd_player.js"), "utf8");
const pageBody = readFileSync(resolve(process.cwd(), "../internal/video/mpd_preview.html"), "utf8");

type Reply = { status: number; body?: unknown } | "network";
type Routes = Record<string, Reply | (() => Reply)>;

const ORIGIN = "https://sendrec.example.test";
const CLOSED_STAFF_OS = "This preview has closed. Open it again from Staff OS.";
const CLOSED_SENDREC = "This preview has closed. Reload the page to open the preview again.";
const ENDED = "Access ended. Sign in again and open a new preview.";
const UNAVAILABLE = "The preview isn't available right now. Open it again from Staff OS.";
const INVALID = "This preview link isn't valid. Open it again from Staff OS.";
const HANDOFF = "#handoff=synthetic-handoff&nonce=synthetic-nonce&parentOrigin=" + encodeURIComponent("https://staff.example.test");
const RETRY_WAIT = 2300;

const redeemed = { previewToken: "synthetic-token", videoId: "synthetic", title: "Synthetic recording", recorded: "Recorded 18 Nov 2026", linkState: "Patients can open the link" };
const media = (linkState = "Patients can open the link"): Reply => ({ status: 200, body: { videoUrl: "https://media.example.test/clip.webm", transcriptUrl: "", linkState } });
const session: Reply = { status: 201, body: { playbackSessionId: "synthetic-session", mediaVersion: 1, expiresAt: new Date(Date.now() + 60000).toISOString() } };
const opened = (overrides: Routes = {}): Routes => ({
  "/api/mpd/preview/redeem": { status: 200, body: redeemed },
  "/api/mpd/preview/media": media(),
  "/api/mpd/preview/session": session,
  ...overrides,
});

const pages: JSDOM[] = [];
afterEach(() => { while (pages.length) pages.pop()!.window.close(); });

const settle = async (ms = 0) => { await new Promise(done => setTimeout(done, ms)); for (let i = 0; i < 8; i++) await new Promise(done => setTimeout(done, 0)); };

type Playback = { time: number; paused: boolean; intervals: number; loads: number };

function open(hash: string, routes: Routes, options: { parent?: unknown; autoLoad?: boolean; load?: (player: HTMLMediaElement, playback: Playback) => void } = {}) {
  const dom = new JSDOM("<!doctype html><html><body>" + pageBody + "</body></html>", { url: ORIGIN + "/mpd-preview" + hash, runScripts: "outside-only", pretendToBeVisual: true });
  pages.push(dom);
  const win = dom.window;
  const calls: string[] = [];
  win.fetch = (async (path: string) => {
    calls.push(path);
    const route = routes[path];
    const reply = typeof route === "function" ? route() : route ?? { status: 500 };
    if (reply === "network") throw new TypeError("Failed to fetch");
    return { ok: reply.status >= 200 && reply.status < 300, status: reply.status, json: async () => reply.body } as Response;
  }) as typeof fetch;
  // The player's position and paused state, and the renewal timers armed, as the script sees them.
  const playback: Playback = { time: 0, paused: true, intervals: 0, loads: 0 };
  const element = win.document.getElementById("player") as HTMLVideoElement;
  Object.defineProperty(element, "currentTime", { configurable: true, get: () => playback.time, set: (value: number) => { playback.time = value; } });
  Object.defineProperty(element, "paused", { configurable: true, get: () => playback.paused });
  const media = win.HTMLMediaElement.prototype;
  media.pause = () => { playback.paused = true; };
  media.play = async () => { playback.paused = false; };
  media.load = function (this: HTMLMediaElement) {
    if (!this.getAttribute("src")) return;
    playback.loads++;
    if (options.load) options.load(this, playback);
    else if (options.autoLoad !== false) queueMicrotask(() => this.dispatchEvent(new win.Event("loadedmetadata")));
  };
  const arm = win.setInterval.bind(win);
  win.setInterval = ((handler: TimerHandler, ms?: number) => { if (ms === 240000) playback.intervals++; return arm(handler, ms); }) as typeof win.setInterval;
  if (options.parent) Object.defineProperty(win, "parent", { configurable: true, get: () => options.parent });
  win.eval(playerSource);
  const el = (id: string) => win.document.getElementById(id) as HTMLElement;
  return { win, el, calls, playback, player: el("player") as HTMLVideoElement };
}

describe("staff preview header", () => {
  it("names the recording as text, only once its media loads, in its own tab", async () => {
    const title = "<img src=x onerror=alert(1)> Synthetic";
    const page = open(HANDOFF, opened({ "/api/mpd/preview/redeem": { status: 200, body: { ...redeemed, title } } }), { autoLoad: false });
    await settle();
    expect(page.el("mpd-meta").classList.contains("shown")).toBe(false);
    expect(page.el("mpd-title").textContent).toBe("Staff preview");
    page.player.dispatchEvent(new page.win.Event("loadedmetadata"));
    await settle();
    expect(page.el("mpd-meta").classList.contains("shown")).toBe(true);
    expect(page.el("mpd-title").textContent).toBe(title);
    expect(page.el("mpd-meta").querySelector("img")).toBeNull();
    expect(page.el("mpd-detail").textContent).toBe("Recorded 18 Nov 2026 · Patients can open the link");
    expect(page.win.document.title).not.toContain("Synthetic");
    expect(page.win.location.hash).toBe("");
  });

  it("stays visually hidden when framed by Staff OS", async () => {
    const parent = { postMessage: vi.fn() };
    Object.defineProperty(parent, "location", { get: () => { throw new Error("cross-origin"); } });
    const page = open(HANDOFF, opened(), { parent });
    await settle();
    expect(page.player.getAttribute("src")).toBeTruthy();
    expect(page.el("mpd-title").textContent).toBe("Synthetic recording");
    expect(page.el("mpd-meta").classList.contains("shown")).toBe(false);
  });

  it("follows the link state on each media renewal", async () => {
    let state = "Patients can open the link";
    const page = open(HANDOFF, opened({ "/api/mpd/preview/media": () => media(state) }));
    await settle();
    state = "Patients can't open the link";
    page.player.dispatchEvent(new page.win.Event("error"));
    await settle();
    expect(page.el("mpd-detail").textContent).toBe("Recorded 18 Nov 2026 · Patients can't open the link");
  });

  it("shows the full-title toggle only while the title is longer than two lines", async () => {
    const page = open(HANDOFF, opened());
    await settle();
    const title = page.el("mpd-title");
    let whole = 90;
    title.getBoundingClientRect = () => ({ height: 45 }) as DOMRect;
    Object.defineProperty(title, "scrollHeight", { configurable: true, get: () => whole });
    page.win.dispatchEvent(new page.win.Event("resize"));
    await settle(30);
    const toggle = page.el("mpd-more") as HTMLButtonElement;
    expect(toggle.hidden).toBe(false);
    toggle.click();
    await settle();
    expect(page.el("mpd-meta").classList.contains("full")).toBe(true);
    expect(toggle.textContent).toBe("Show less");
    expect(toggle.getAttribute("aria-expanded")).toBe("true");
    expect(toggle.getAttribute("title")).toBeNull();
    // A wider window: the whole title now fits in two lines.
    whole = 45;
    page.win.dispatchEvent(new page.win.Event("resize"));
    page.win.dispatchEvent(new page.win.Event("resize"));
    await settle(30);
    expect(toggle.hidden).toBe(true);
    expect(page.el("mpd-meta").classList.contains("full")).toBe(false);
    expect(toggle.textContent).toBe("Show full title");
  });
});

describe("staff preview renewals", () => {
  it.each([
    [403, ENDED],
    [410, CLOSED_STAFF_OS],
  ])("stops at once and says what to do when a renewal answers %i", async (status, message) => {
    let refuse = false;
    const page = open(HANDOFF, opened({ "/api/mpd/preview/media": () => (refuse ? { status } : media()) }));
    await settle();
    refuse = true;
    page.player.dispatchEvent(new page.win.Event("error"));
    await settle();
    expect(page.el("mpd-status").textContent).toBe(message);
    expect(page.el("mpd-meta").classList.contains("shown")).toBe(false);
    expect(page.el("mpd-title").textContent).toBe("Staff preview");
    expect(page.player.getAttribute("src")).toBeNull();
    expect(page.calls.filter(path => path === "/api/mpd/preview/media")).toHaveLength(2);
  });

  it.each<[string, Reply]>([
    ["409 not ready", { status: 409 }],
    ["503", { status: 503 }],
    ["no answer", "network"],
  ])("tries a renewal that may pass (%s) once more, and keeps playing when it does", async (_name, failure) => {
    let failures = 0;
    const page = open(HANDOFF, opened({ "/api/mpd/preview/media": () => (failures-- > 0 ? failure : media()) }));
    await settle();
    failures = 1;
    page.player.dispatchEvent(new page.win.Event("error"));
    await settle(RETRY_WAIT);
    expect(page.player.getAttribute("src")).toBeTruthy();
    expect(page.el("mpd-meta").classList.contains("shown")).toBe(true);
  }, 10000);

  it("resumes where the viewer was when a failed media load is retried", async () => {
    let failNext = false;
    const page = open(HANDOFF, opened(), {
      load: (player, playback) => {
        if (failNext) {
          // As a browser does, a failed load resets the position and pauses.
          failNext = false;
          playback.time = 0;
          playback.paused = true;
          queueMicrotask(() => player.dispatchEvent(new page.win.Event("error")));
        } else queueMicrotask(() => player.dispatchEvent(new page.win.Event("loadedmetadata")));
      },
    });
    await settle();
    page.playback.time = 12;
    page.playback.paused = false;
    failNext = true;
    page.player.dispatchEvent(new page.win.Event("error"));
    await settle(RETRY_WAIT);
    expect(page.playback.time).toBe(12);
    expect(page.playback.paused).toBe(false);
    expect(page.player.getAttribute("src")).toBeTruthy();
  }, 10000);

  it("retries only a new playback session that fails, not the media", async () => {
    let sessions = 0;
    const page = open(HANDOFF, opened({
      "/api/mpd/preview/media": { status: 200, body: { videoUrl: "https://media.example.test/clip.webm", transcriptUrl: "", linkState: "Patients can open the link", mediaVersion: 2 } },
      "/api/mpd/preview/session": () => (++sessions === 2 ? { status: 503, body: { code: "unavailable" } } : session),
    }));
    await settle();
    const mediaBefore = page.calls.filter(path => path === "/api/mpd/preview/media").length;
    page.player.dispatchEvent(new page.win.Event("error"));
    await settle(RETRY_WAIT);
    expect(page.calls.filter(path => path === "/api/mpd/preview/media").length).toBe(mediaBefore + 1);
    expect(sessions).toBe(3);
    expect(page.player.getAttribute("src")).toBeTruthy();
  }, 10000);

  it("stays stopped when the first media load is refused: no session, no renewal timer", async () => {
    const page = open(HANDOFF, opened({ "/api/mpd/preview/media": { status: 403, body: { code: "signed_out" } } }));
    await settle();
    expect(page.el("mpd-status").textContent).toBe(ENDED);
    expect(page.calls).not.toContain("/api/mpd/preview/session");
    expect(page.playback.intervals).toBe(0);
    page.player.dispatchEvent(new page.win.Event("error"));
    await settle();
    expect(page.calls.filter(path => path === "/api/mpd/preview/media")).toHaveLength(1);
  });

  it("says the preview isn't available when a renewal fails twice", async () => {
    let fail = false;
    const page = open(HANDOFF, opened({ "/api/mpd/preview/media": () => (fail ? { status: 503 } : media()) }));
    await settle();
    fail = true;
    page.player.dispatchEvent(new page.win.Event("error"));
    await settle(RETRY_WAIT);
    expect(page.el("mpd-status").textContent).toBe(UNAVAILABLE);
    expect(page.player.getAttribute("src")).toBeNull();
  }, 10000);
});

describe("staff preview refusals", () => {
  it("says the preview has closed after a refresh, without asking the server", async () => {
    const page = open("", opened());
    await settle();
    expect(page.el("mpd-status").textContent).toBe(CLOSED_STAFF_OS);
    expect(page.calls).toHaveLength(0);
  });

  it.each<[string, Reply, string]>([
    ["410 closed", { status: 410, body: { code: "closed" } }, CLOSED_STAFF_OS],
    ["403 signed out", { status: 403, body: { code: "signed_out" } }, ENDED],
    ["403 origin denied", { status: 403, body: { code: "invalid" } }, INVALID],
    ["400 invalid", { status: 400, body: { code: "invalid" } }, INVALID],
    ["410 without a code", { status: 410 }, CLOSED_STAFF_OS],
  ])("tells staff what to do when the redeem answers %s", async (_name, reply, message) => {
    const page = open(HANDOFF, opened({ "/api/mpd/preview/redeem": reply }));
    await settle();
    expect(page.el("mpd-status").textContent).toBe(message);
    expect(page.el("mpd-meta").classList.contains("shown")).toBe(false);
    expect(page.win.location.hash).toBe("");
  });

  it("retries a redeem that may pass once, then sends staff back to Staff OS, keeping the handoff out of the address", async () => {
    const page = open(HANDOFF, opened({ "/api/mpd/preview/redeem": { status: 503, body: { code: "unavailable" } } }));
    await settle(RETRY_WAIT);
    expect(page.calls.filter(path => path === "/api/mpd/preview/redeem")).toHaveLength(2);
    expect(page.el("mpd-status").textContent).toBe(UNAVAILABLE);
    expect(page.win.location.hash).toBe("");
  }, 10000);

  it("opens on the retry when the first redeem meets a passing fault", async () => {
    let first = true;
    const page = open(HANDOFF, opened({ "/api/mpd/preview/redeem": () => (first ? ((first = false), { status: 503 }) : { status: 200, body: redeemed }) }));
    await settle(RETRY_WAIT);
    expect(page.el("mpd-title").textContent).toBe("Synthetic recording");
    expect(page.win.location.hash).toBe("");
  }, 10000);

  it("does not mention Staff OS inside SendRec's own frame", async () => {
    const parent = { location: { origin: ORIGIN }, postMessage: vi.fn() };
    const page = open(HANDOFF, opened({ "/api/mpd/preview/redeem": { status: 410 } }), { parent });
    await settle();
    expect(page.el("mpd-status").textContent).toBe(CLOSED_SENDREC);
  });
});
