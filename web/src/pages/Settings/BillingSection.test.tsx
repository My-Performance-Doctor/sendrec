import { render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { BillingSection } from "./BillingSection";
import type { BillingData } from "./types";

function canceled(plan: string): BillingData {
  return { plan, subscriptionId: "sub_1", subscriptionStatus: "canceled", portalUrl: null };
}

// A canceled subscription keeps its plan's features until the period ends:
// the message names that plan, not always Pro.
describe("BillingSection, canceled", () => {
  it.each([
    ["pro", "Pro"],
    ["business", "Business"],
  ])("names the %s plan", (plan, label) => {
    render(<BillingSection billing={canceled(plan)} />);
    expect(screen.getByText(/subscription has been canceled/)).toHaveTextContent(
      `You have access to ${label} features until the end of your billing period.`,
    );
  });
});
