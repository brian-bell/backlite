import { QueryClient, QueryClientProvider, useQuery } from "@tanstack/react-query";
import { useState, type ReactNode } from "react";
import {
  BrowserRouter,
  Link,
  MemoryRouter,
  Navigate,
  Route,
  Routes,
  useParams
} from "react-router-dom";

import { ApiError, getReading, listReadings, tokenStorageKey, type Reading } from "./api";
import "./styles.css";

type AppProps = {
  initialPath?: string;
};

const pageSize = 20;

export function App({ initialPath }: AppProps) {
  const [queryClient] = useState(
    () =>
      new QueryClient({
        defaultOptions: {
          queries: {
            retry: false
          }
        }
      })
  );
  const [token, setToken] = usePersistentToken();
  const router = initialPath ? (
    <MemoryRouter initialEntries={[initialPath]}>
      <AppRoutes token={token} setToken={setToken} />
    </MemoryRouter>
  ) : (
    <BrowserRouter>
      <AppRoutes token={token} setToken={setToken} />
    </BrowserRouter>
  );

  return <QueryClientProvider client={queryClient}>{router}</QueryClientProvider>;
}

function usePersistentToken(): [string, (token: string) => void] {
  const [token, setTokenState] = useState(() => window.localStorage.getItem(tokenStorageKey) ?? "");
  const setToken = (nextToken: string) => {
    const trimmed = nextToken.trim();
    setTokenState(trimmed);
    if (trimmed === "") {
      window.localStorage.removeItem(tokenStorageKey);
    } else {
      window.localStorage.setItem(tokenStorageKey, trimmed);
    }
  };
  return [token, setToken];
}

function AppRoutes({ token, setToken }: { token: string; setToken: (token: string) => void }) {
  return (
    <Shell token={token} setToken={setToken}>
      <Routes>
        <Route path="/" element={<ReadingList token={token} />} />
        <Route path="/readings/:id" element={<ReadingDetail token={token} />} />
        <Route path="*" element={<Navigate to="/" replace />} />
      </Routes>
    </Shell>
  );
}

function Shell({
  children,
  token,
  setToken
}: {
  children: ReactNode;
  token: string;
  setToken: (token: string) => void;
}) {
  return (
    <div className="app-shell">
      <aside className="sidebar" aria-label="Primary">
        <div className="brand">Backlite</div>
        <nav>
          <Link className="nav-link active" to="/">
            Readings
          </Link>
          <span className="nav-link muted">Tasks</span>
        </nav>
      </aside>
      <div className="workspace">
        <header className="topbar">
          <AuthTokenForm token={token} setToken={setToken} />
        </header>
        <main>{children}</main>
      </div>
    </div>
  );
}

function AuthTokenForm({ token, setToken }: { token: string; setToken: (token: string) => void }) {
  const [draft, setDraft] = useState(token);

  return (
    <form
      className="token-form"
      onSubmit={(event) => {
        event.preventDefault();
        setToken(draft);
      }}
    >
      <label htmlFor="bearer-token">Bearer token</label>
      <input
        id="bearer-token"
        type="password"
        autoComplete="off"
        value={draft}
        onChange={(event) => setDraft(event.target.value)}
      />
      <button type="submit">Save</button>
    </form>
  );
}

function ReadingList({ token }: { token: string }) {
  const [offset, setOffset] = useState(0);
  const query = useQuery({
    queryKey: ["readings", token, offset],
    queryFn: () => listReadings(token, pageSize, offset)
  });

  return (
    <section className="page">
      <div className="page-heading">
        <h1>Reading Library</h1>
      </div>

      {query.isLoading ? <StatusMessage>Loading readings</StatusMessage> : null}
      {query.isError ? <ErrorState error={query.error} /> : null}
      {query.isSuccess && query.data.readings.length === 0 ? (
        <StatusMessage>No readings yet</StatusMessage>
      ) : null}
      {query.isSuccess && query.data.readings.length > 0 ? (
        <>
          <div className="reading-list">
            {query.data.readings.map((reading) => (
              <ReadingListItem key={reading.id} reading={reading} />
            ))}
          </div>
          <div className="pager">
            <button type="button" disabled={offset === 0} onClick={() => setOffset(Math.max(0, offset - pageSize))}>
              Previous
            </button>
            <span>Page {Math.floor(offset / pageSize) + 1}</span>
            <button type="button" disabled={!query.data.has_more} onClick={() => setOffset(offset + pageSize)}>
              Next
            </button>
          </div>
        </>
      ) : null}
    </section>
  );
}

function ReadingListItem({ reading }: { reading: Reading }) {
  return (
    <article className="reading-row">
      <div className="reading-row-main">
        <Link to={`/readings/${reading.id}`}>{reading.title || reading.url}</Link>
        <p>{reading.tldr}</p>
        <TagList tags={reading.tags} />
      </div>
      <div className="reading-row-meta">
        <span>{reading.novelty_verdict || "unclassified"}</span>
        <time dateTime={reading.created_at}>{formatDate(reading.created_at)}</time>
      </div>
    </article>
  );
}

function ReadingDetail({ token }: { token: string }) {
  const { id } = useParams();
  const readingID = id ?? "";
  const query = useQuery({
    queryKey: ["reading", token, readingID],
    queryFn: () => getReading(token, readingID),
    enabled: readingID !== ""
  });

  if (query.isLoading) {
    return <StatusMessage>Loading reading</StatusMessage>;
  }
  if (query.isError) {
    return <ErrorState error={query.error} />;
  }
  if (!query.data) {
    return <StatusMessage>Reading not found</StatusMessage>;
  }

  const reading = query.data;
  return (
    <section className="page detail-page">
      <Link className="back-link" to="/">
        Back to readings
      </Link>
      <div className="page-heading detail-heading">
        <div>
          <h1>{reading.title || reading.url}</h1>
          <a href={reading.url} rel="noreferrer" target="_blank">
            {reading.url}
          </a>
        </div>
        <span className="verdict">{reading.novelty_verdict || "unclassified"}</span>
      </div>
      <section className="detail-section">
        <h2>TL;DR</h2>
        <p>{reading.tldr}</p>
      </section>
      <section className="detail-section">
        <h2>Summary</h2>
        <p>{reading.summary}</p>
      </section>
      <section className="detail-grid">
        <EntityGroup label="Tags" values={reading.tags} />
        <EntityGroup label="Keywords" values={reading.keywords} />
        <EntityGroup label="People" values={reading.people} />
        <EntityGroup label="Organizations" values={reading.orgs} />
      </section>
      {reading.connections.length > 0 ? (
        <section className="detail-section">
          <h2>Connections</h2>
          <ul className="connections">
            {reading.connections.map((connection) => (
              <li key={`${connection.reading_id}-${connection.reason}`}>
                <span>{connection.reading_id}</span>
                <p>{connection.reason}</p>
              </li>
            ))}
          </ul>
        </section>
      ) : null}
    </section>
  );
}

function EntityGroup({ label, values }: { label: string; values: string[] }) {
  return (
    <section className="entity-group">
      <h2>{label}</h2>
      {values.length > 0 ? <TagList tags={values} /> : <p>None</p>}
    </section>
  );
}

function TagList({ tags }: { tags: string[] }) {
  if (tags.length === 0) {
    return null;
  }
  return (
    <div className="tags">
      {tags.map((tag) => (
        <span key={tag}>{tag}</span>
      ))}
    </div>
  );
}

function StatusMessage({ children }: { children: ReactNode }) {
  return <div className="status-message">{children}</div>;
}

function ErrorState({ error }: { error: Error }) {
  const message =
    error instanceof ApiError && (error.status === 401 || error.status === 403)
      ? "Authentication failed. Check your bearer token."
      : "Could not load readings.";
  return <div className="error-message">{message}</div>;
}

function formatDate(value: string) {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return value;
  }
  return new Intl.DateTimeFormat(undefined, {
    month: "short",
    day: "numeric",
    year: "numeric"
  }).format(date);
}

export default App;
