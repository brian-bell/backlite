import createClient from "openapi-fetch";

import type { components, paths } from "./generated/api";

export type Reading = components["schemas"]["Reading"];
export type ReadingDetail = components["schemas"]["ReadingDetail"];
export type RelatedReading = components["schemas"]["RelatedReading"];
export type OriginatingTask = components["schemas"]["OriginatingTaskInfo"];
export type ReadingPage = components["schemas"]["ReadingPage"];

export const tokenStorageKey = "backlite.bearerToken";

export class ApiError extends Error {
  status: number;

  constructor(status: number, message: string) {
    super(message);
    this.name = "ApiError";
    this.status = status;
  }
}

function clientFor(token: string) {
  return createClient<paths>({
    baseUrl: window.location.origin,
    fetch: async (request: Request) => {
      const headers = new Headers(request.headers);
      if (token.trim() !== "") {
        headers.set("Authorization", `Bearer ${token.trim()}`);
      }
      return fetch(new Request(request, { headers }));
    }
  });
}

function errorMessage(error: unknown, fallback: string) {
  if (error && typeof error === "object" && "error" in error) {
    const value = (error as { error?: unknown }).error;
    if (typeof value === "string" && value !== "") {
      return value;
    }
  }
  return fallback;
}

export type ListReadingsParams = {
  limit: number;
  offset: number;
  q?: string;
  tag?: string;
};

export async function listReadings(token: string, params: ListReadingsParams): Promise<ReadingPage> {
  const query: Record<string, string | number> = {
    limit: params.limit,
    offset: params.offset
  };
  if (params.q && params.q !== "") {
    query.q = params.q;
  }
  if (params.tag && params.tag !== "") {
    query.tag = params.tag;
  }
  const { data, error, response } = await clientFor(token).GET("/api/v1/readings", {
    params: { query }
  });
  if (!response.ok || !data) {
    throw new ApiError(response.status, errorMessage(error, "failed to list readings"));
  }
  return data.data;
}

export async function getReading(token: string, id: string): Promise<ReadingDetail> {
  const { data, error, response } = await clientFor(token).GET("/api/v1/readings/{id}", {
    params: { path: { id } }
  });
  if (!response.ok || !data) {
    throw new ApiError(response.status, errorMessage(error, "failed to get reading"));
  }
  return data.data;
}
