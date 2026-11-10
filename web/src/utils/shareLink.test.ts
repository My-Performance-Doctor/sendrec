import { expect, it } from "vitest";
import { watchPath } from "./shareLink";

it("uses the short path only for tokens the server routes there", () => {
  expect(watchPath("dg82ewfrwhb87cq4")).toBe("/dg82ewfrwhb87cq4");
  expect(watchPath("Xk3_9aBcDeFgHiJkLmNo-q")).toBe("/Xk3_9aBcDeFgHiJkLmNo-q");
  expect(watchPath("abc123defghi")).toBe("/watch/abc123defghi");
});
