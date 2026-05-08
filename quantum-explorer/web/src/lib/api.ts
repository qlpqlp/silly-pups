export const apiBase = (): string =>
  (typeof window !== "undefined" && (window as unknown as { __QE_API__?: string }).__QE_API__) ||
  process.env.NEXT_PUBLIC_API_BASE ||
  "";

export async function fetchJSON<T>(path: string, init?: RequestInit): Promise<T> {
  const url = `${apiBase()}${path}`;
  const res = await fetch(url, {
    ...init,
    headers: { Accept: "application/json", ...(init?.headers || {}) },
  });
  if (!res.ok) {
    const text = await res.text();
    throw new Error(`${res.status} ${text.slice(0, 200)}`);
  }
  return res.json() as Promise<T>;
}
