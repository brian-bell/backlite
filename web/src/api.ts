import createClient from "openapi-fetch";

import type { components, paths } from "./generated/api";

export type Reading = components["schemas"]["Reading"];
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

export async function listReadings(token: string, limit: number, offset: number): Promise<ReadingPage> {
  const { data, error, response } = await clientFor(token).GET("/api/v1/readings", {
    params: { query: { limit, offset } }
  });
  if (!response.ok || !data) {
    throw new ApiError(response.status, errorMessage(error, "failed to list readings"));
  }
  return data.data;
}

export async function getReading(token: string, id: string): Promise<Reading> {
  const { data, error, response } = await clientFor(token).GET("/api/v1/readings/{id}", {
    params: { path: { id } }
  });
  if (!response.ok || !data) {
    throw new ApiError(response.status, errorMessage(error, "failed to get reading"));
  }
  return data.data;
}
