export type SourceKind = "rss" | "web" | "csv";

export type SourceProfile = {
  summary?: string;
  timestamp_path?: string | null;
  fields?: Array<{
    path: string;
    role: string;
    description?: string;
    unit?: string | null;
  }>;
};

export type Source = {
  id: string;
  name: string;
  kind: string;
  config?: { url?: string; file_name?: string } | string | null;
  profile?: SourceProfile | null;
  record_count?: number | string;
  recordCount?: number | string;
  poll_interval_s?: number | null;
  pollIntervalSeconds?: number;
  last_polled_at?: string | null;
  lastPolledAt?: string;
  last_error?: string | null;
  lastError?: string;
  created_at?: string;
  createdAt?: string;
};

type SourceListResponse = { sources?: Source[] };

async function parseResponse<T>(response: Response): Promise<T> {
  const body = await response.json().catch(() => ({}));
  if (!response.ok) {
    const message =
      typeof body.error === "string"
        ? body.error
        : `Request failed (${response.status})`;
    throw new Error(message);
  }
  return body as T;
}

export async function listSources(signal?: AbortSignal): Promise<Source[]> {
  const response = await fetch("/api/sources", {
    method: "GET",
    cache: "no-store",
    signal,
  });
  const body = await parseResponse<SourceListResponse>(response);
  return body.sources ?? [];
}

export async function getSource(id: string, signal?: AbortSignal): Promise<Source> {
  const response = await fetch(`/api/sources/${encodeURIComponent(id)}`, {
    method: "GET",
    cache: "no-store",
    signal,
  });
  return parseResponse<Source>(response);
}

export async function createSource(input: {
  name: string;
  kind: SourceKind;
  config: Record<string, unknown>;
  pollIntervalSeconds: number;
}): Promise<Source> {
  const response = await fetch("/api/sources", {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({
      name: input.name,
      kind: input.kind,
      config: input.config,
      poll_interval_s: input.pollIntervalSeconds,
    }),
  });
  return parseResponse<Source>(response);
}
