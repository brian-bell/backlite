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
    related: [
      {
        reading_id: "bf_READ_TWO",
        reason: "same operations theme",
        title: "Related Notes",
        tldr: "Companion summary for operators",
        url: "https://example.com/related",
        novelty_verdict: "extends_existing"
      }
    ],
    originating_task: {
      id: "bf_TASK_ONE",
      status: "completed",
      task_mode: "read",
      repo_url: "",
      pr_url: "",
      output_url: "/api/v1/tasks/bf_TASK_ONE/output",
      error: "",
      created_at: "2026-04-25T15:30:00Z",
      completed_at: "2026-04-25T15:35:00Z"
    },
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

  it("submits a search query and refetches with q on the request URL", async () => {
    const fetchMock = vi.fn(async (_request: Request) => jsonResponse(readingPage));
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    render(<App initialPath="/" />);

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(1));
    const initialUrl = new URL(fetchMock.mock.calls[0][0].url);
    expect(initialUrl.searchParams.get("q")).toBeNull();

    const searchInput = screen.getByLabelText("Search readings");
    await user.type(searchInput, "sqlite");
    await user.click(screen.getByRole("button", { name: "Search" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    const refetchUrl = new URL(fetchMock.mock.calls[1][0].url);
    expect(refetchUrl.searchParams.get("q")).toBe("sqlite");
  });

  it("hydrates the search input from the URL ?q= param", async () => {
    stubJSON(readingPage);

    render(<App initialPath="/?q=cached" />);

    const input = (await screen.findByLabelText("Search readings")) as HTMLInputElement;
    expect(input.value).toBe("cached");
  });

  it("renders the related readings panel with title, tldr, url, and reason", async () => {
    stubJSON(readingDetail);

    render(<App initialPath="/readings/bf_READ_ONE" />);

    const heading = await screen.findByRole("heading", { name: "Related Readings" });
    const panel = heading.closest("section") as HTMLElement;
    expect(panel).not.toBeNull();
    const link = within(panel).getByRole("link", { name: "Related Notes" });
    expect(link).toHaveAttribute("href", "/readings/bf_READ_TWO");
    expect(within(panel).getByText("Companion summary for operators")).toBeInTheDocument();
    expect(within(panel).getByText("https://example.com/related")).toBeInTheDocument();
    expect(within(panel).getByText("same operations theme")).toBeInTheDocument();
  });

  it("renders the originating task panel with status and output link", async () => {
    stubJSON(readingDetail);

    render(<App initialPath="/readings/bf_READ_ONE" />);

    const heading = await screen.findByRole("heading", { name: "Originating Task" });
    const panel = heading.closest("section") as HTMLElement;
    expect(within(panel).getByText("bf_TASK_ONE")).toBeInTheDocument();
    expect(within(panel).getByText(/completed/i)).toBeInTheDocument();
    const outputLink = within(panel).getByRole("link", { name: /agent output/i });
    expect(outputLink).toHaveAttribute("href", "/api/v1/tasks/bf_TASK_ONE/output");
  });

  it("renders an unavailable state when the originating task is null", async () => {
    stubJSON({
      data: {
        ...readingDetail.data,
        originating_task: null
      }
    });

    render(<App initialPath="/readings/bf_READ_ONE" />);

    const heading = await screen.findByRole("heading", { name: "Originating Task" });
    const panel = heading.closest("section") as HTMLElement;
    expect(within(panel).getByText(/originating task is unavailable/i)).toBeInTheDocument();
  });

  it("renders an empty state when no related readings are returned", async () => {
    stubJSON({
      data: {
        ...readingDetail.data,
        connections: [],
        related: []
      }
    });

    render(<App initialPath="/readings/bf_READ_ONE" />);

    const heading = await screen.findByRole("heading", { name: "Related Readings" });
    const panel = heading.closest("section") as HTMLElement;
    expect(within(panel).getByText("No related readings yet")).toBeInTheDocument();
  });

  it("filters by clicking a tag chip and clears with the active-tag control", async () => {
    const fetchMock = vi.fn(async (_request: Request) => jsonResponse(readingPage));
    vi.stubGlobal("fetch", fetchMock);
    const user = userEvent.setup();

    render(<App initialPath="/" />);

    // Click the "agents" tag chip on the first reading row.
    await user.click(await screen.findByRole("button", { name: "Filter by tag agents" }));

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(2));
    const tagUrl = new URL(fetchMock.mock.calls[1][0].url);
    expect(tagUrl.searchParams.get("tag")).toBe("agents");

    // Active-tag indicator appears and clears the filter when activated.
    const clearTag = await screen.findByRole("button", { name: /clear tag agents/i });
    await user.click(clearTag);

    await waitFor(() => expect(fetchMock).toHaveBeenCalledTimes(3));
    const clearedUrl = new URL(fetchMock.mock.calls[2][0].url);
    expect(clearedUrl.searchParams.get("tag")).toBeNull();
  });
});
