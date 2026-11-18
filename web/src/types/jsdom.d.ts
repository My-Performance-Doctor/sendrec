// The part of jsdom's API the staff preview tests use; jsdom ships no types of its own.
declare module "jsdom" {
  export class JSDOM {
    constructor(html?: string, options?: { url?: string; runScripts?: "outside-only" | "dangerously"; pretendToBeVisual?: boolean });
    readonly window: Window & typeof globalThis;
  }
}
