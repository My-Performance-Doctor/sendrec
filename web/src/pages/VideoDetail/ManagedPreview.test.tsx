import { beforeEach, expect, it, vi } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";
import { ManagedPreview } from "./ManagedPreview";
const api = vi.fn();
vi.mock("../../api/client", () => ({ apiFetch: (...args: unknown[]) => api(...args) }));
beforeEach(() => api.mockReset());
it("retries authorization failures without falling back to public playback", async () => {
 api.mockRejectedValueOnce(new Error("Sign in again")).mockResolvedValueOnce({previewUrl:"/mpd-preview#handoff=synthetic"});
 render(<ManagedPreview id="synthetic" mediaVersion={2} />);
 expect(await screen.findByRole("alert")).toHaveTextContent("Sign in again");
 expect(screen.queryByTitle("Staff preview")).not.toBeInTheDocument();
 fireEvent.click(screen.getByText("Retry preview"));
 expect(await screen.findByTitle("Staff preview")).toHaveAttribute("src",window.location.origin+"/mpd-preview#handoff=synthetic");
 expect(api).toHaveBeenCalledWith("/api/videos/synthetic/preview",expect.objectContaining({method:"POST"}));
});
it("rejects a preview URL from an unexpected origin", async () => {
 api.mockResolvedValue({previewUrl:"https://unexpected.example/mpd-preview#handoff=synthetic"});
 render(<ManagedPreview id="synthetic" />);
 expect(await screen.findByRole("alert")).toHaveTextContent("Preview unavailable");
 expect(screen.queryByTitle("Staff preview")).not.toBeInTheDocument();
});
