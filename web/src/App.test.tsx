import { cleanup, render, screen, waitFor, waitForElementToBeRemoved, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { tokenStorageKey } from "./api";
import { App } from "./App";

const readingPage = {
  data: {
    readings: [
      {
        id: "bf_READ_ONE",
        task_id: "bf_TASK_ONE",
        url: "https://example.com/one",
        title: "Operator Notes",
        tldr: "A concise result from the reader agent.",
        tags: ["agents", "ops"],
        keywords: ["sqlite"],
        people: [],
        orgs: ["Backlite"],
        novelty_verdict: "new",
        connections: [],
        summary: "Full text is available on the detail route.",
        created_at: "2026-04-25T16:00:00Z"
      }
    ],
    limit: 20,
    offset: 0,
    has_more: false
  }
};

const emptyPage = {
  data: {
    readings: [],
    limit: 20,
    offset: 0,
    has_more: false
  }
};

const readingDetail = {
  data: {
    id: "bf_READ_ONE",
    task_id: "bf_TASK_ONE",
    url: "https://example.com/one",
    title: "Operator Notes",
    tldr: "A concise result from the reader agent.",
    tags: ["agents", "ops"],
    keywords: ["sqlite"],
    people: ["Ada"],
    orgs: ["Backlite"],
    novelty_verdict: "new",
    connections: [{ reading_id: "bf_READ_TWO", reason: "same operations theme" }],
    summary: "A detailed summary for operators reviewing saved reading results.",
    created_at: "2026-04-25T16:00:00Z"
  }
};

function jsonResponse(body: unknown, status = 200) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" }
  });
}

function stubJSON(body: unknown, status = 200) {
  const fetchMock = vi.fn(async (_request: Request) => jsonResponse(body, status));
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

describe("App", () => {
  beforeEach(() => {
    window.localStorage.clear();
    stubJSON(readingPage);
  });

  afterEach(() => {
    cleanup();
    vi.unstubAllGlobals();
  });

  it("renders the reading library shell and list results", async () => {
    render(<App initialPath="/" />);

    expect(screen.getByRole("heading", { name: "Reading Library" })).toBeInTheDocument();
    expect(screen.getByText("Loading readings")).toBeInTheDocument();

    await waitForElementToBeRemoved(() => screen.queryByText("Loading readings"));

    expect(screen.getByRole("link", { name: "Operator Notes" })).toBeInTheDocument();
    expect(screen.getByText("A concise result from the reader agent.")).toBeInTheDocument();
    expect(screen.getByText("new")).toBeInTheDocument();
    expect(screen.getByText("agents")).toBeInTheDocument();
  });

  it("renders an empty state when no readings exist", async () => {
    stubJSON(emptyPage);

    render(<App initialPath="/" />);

    expect(await screen.findByText("No readings yet")).toBeInTheDocument();
  });

  it("renders a clear auth failure state", async () => {
    stubJSON({ error: "missing or invalid bearer token" }, 401);

    render(<App initialPath="/" />);

    expect(await screen.findByText("Authentication failed. Check your bearer token.")).toBeInTheDocument();
  });

  it("persists a bearer token and sends it on authenticated requests", async () => {
    const fetchMock = stubJSON(emptyPage);
    const user = userEvent.setup();

    render(<App initialPath="/" />);
    expect(await screen.findByText("No readings yet")).toBeInTheDocument();

    await user.type(screen.getByLabelText("Bearer token"), "secret-token");
    await user.click(screen.getByRole("button", { name: "Save" }));

    expect(window.localStorage.getItem(tokenStorageKey)).toBe("secret-token");
    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));

    const [request] = fetchMock.mock.calls[1];
    expect(request.headers.get("Authorization")).toBe("Bearer secret-token");
  });

  it("renders reading detail fields", async () => {
    stubJSON(readingDetail);

    render(<App initialPath="/readings/bf_READ_ONE" />);

    expect(await screen.findByRole("heading", { name: "Operator Notes" })).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "https://example.com/one" })).toHaveAttribute(
      "href",
      "https://example.com/one"
    );
    expect(screen.getByText("A concise result from the reader agent.")).toBeInTheDocument();
    expect(screen.getByText("A detailed summary for operators reviewing saved reading results.")).toBeInTheDocument();
    expect(screen.getByText("sqlite")).toBeInTheDocument();
    expect(screen.getByText("Ada")).toBeInTheDocument();
    const orgSection = screen.getByRole("heading", { name: "Organizations" }).closest("section");
    expect(orgSection).not.toBeNull();
    expect(within(orgSection as HTMLElement).getByText("Backlite")).toBeInTheDocument();
    expect(screen.getByText("same operations theme")).toBeInTheDocument();
  });
});
