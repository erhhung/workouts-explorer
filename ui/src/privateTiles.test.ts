import { afterEach, beforeEach, describe, expect, test, vi } from "vitest";
import {
  PrivateTiles,
  PRIVATE_TILE_QUEUE_BUDGET_MS,
  PRIVATE_TILE_SERVICE_BUDGET_MS,
  PRIVATE_TILE_MAX_BYTES,
  PRIVATE_TILE_MAX_JOBS,
} from "./privateTiles";

const protocols = vi.hoisted(() => new Map<string, Function>());
vi.mock("maplibre-gl", () => ({
  default: {
    addProtocol: vi.fn((name: string, handler: Function) => protocols.set(name, handler)),
    removeProtocol: vi.fn((name: string) => protocols.delete(name)),
  },
}));

const origin = "https://workouts.test";
const path = `/api/map-selections/${"A".repeat(32)}/route-tiles/7/{z}/{x}/{y}.pbf`;
const coverage = path.replace("route-tiles", "coverage-tiles");
const response = (status = 200, body: BodyInit | null = new Uint8Array([26, 0]), headers = {}) =>
  new Response(status === 204 ? null : body, {
    status,
    headers: { "Content-Type": "application/vnd.mapbox-vector-tile", ...headers },
  });
const controllers: AbortController[] = [];
let tiles: PrivateTiles;
let template: string;
let unauthorized: ReturnType<typeof vi.fn>;
let unavailable: ReturnType<typeof vi.fn>;
function load(x = 0, url = template, controller = new AbortController()) {
  controllers.push(controller);
  const promise = tiles.load({ url: url.replace("{z}/{x}/{y}", `10/${x}/1`) }, controller);
  // Keep deliberately pending failures observed while fake time advances.
  void promise.catch(() => undefined);
  return promise;
}
const flush = async () => {
  for (let i = 0; i < 20; i++) await Promise.resolve();
  await vi.advanceTimersByTimeAsync(0);
};

beforeEach(() => {
  vi.useFakeTimers();
  vi.spyOn(Math, "random").mockReturnValue(0.5);
  unauthorized = vi.fn();
  unavailable = vi.fn();
  tiles = new PrivateTiles(origin, { unauthorized, unavailable });
  template = tiles.setSource("routes", origin + path);
});
afterEach(async () => {
  tiles.close();
  for (const controller of controllers.splice(0)) controller.abort();
  await flush();
  expect(vi.getTimerCount()).toBe(0);
  expect(protocols.size).toBe(0);
  vi.restoreAllMocks();
  vi.useRealTimers();
});

describe("private vector scheduling", () => {
  test("shares four active fetches across both sources and drains FIFO", async () => {
    const replies: Array<(value: Response) => void> = [];
    const fetch = vi
      .spyOn(globalThis, "fetch")
      .mockImplementation(() => new Promise((resolve) => replies.push(resolve)));
    const other = tiles.setSource("coverage", origin + coverage);
    const results = Array.from({ length: 12 }, (_, i) => load(i, i % 2 ? other : template));
    expect(fetch).toHaveBeenCalledTimes(4);
    replies[0](response());
    await flush();
    expect(fetch).toHaveBeenCalledTimes(5);
    expect(fetch.mock.calls[4][0]).toBe(origin + path.replace("{z}/{x}/{y}", "10/4/1"));
    for (let i = 1; i < 12; i++) {
      replies[i](response());
      await flush();
    }
    await Promise.all(results);
    expect(fetch).toHaveBeenCalledTimes(12);
  });

  test("bounds all outstanding jobs and removes canceled queued work before HTTP", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise(() => {}));
    const pending = Array.from({ length: PRIVATE_TILE_MAX_JOBS }, (_, i) => load(i));
    await expect(load(PRIVATE_TILE_MAX_JOBS)).rejects.toMatchObject({ status: 503 });
    controllers[10].abort();
    await expect(pending[10]).rejects.toMatchObject({ name: "AbortError" });
    const replacement = load(PRIVATE_TILE_MAX_JOBS + 1);
    expect(fetch).toHaveBeenCalledTimes(4);
    tiles.close();
    await expect(replacement).rejects.toMatchObject({ name: "AbortError" });
  });

  test("coalesces followers, detaches individually, and aborts only the last follower", async () => {
    let signal: AbortSignal | undefined;
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation((_url, init) => {
      signal = init?.signal as AbortSignal;
      return new Promise(() => {});
    });
    const first = load();
    const second = load();
    expect(fetch).toHaveBeenCalledTimes(1);
    controllers[0].abort();
    await expect(first).rejects.toMatchObject({ name: "AbortError" });
    expect(signal?.aborted).toBe(false);
    controllers[1].abort();
    await expect(second).rejects.toMatchObject({ name: "AbortError" });
    expect(signal?.aborted).toBe(true);
  });

  test("coalesced success buffers can be transferred independently and are not cached", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(response());
    const [a, b] = await Promise.all([load(), load()]);
    expect(a.data).not.toBe(b.data);
    expect(new Uint8Array(a.data)).toEqual(new Uint8Array(b.data));
    fetch.mockResolvedValueOnce(response());
    await load();
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  test.each([502, 503, 504])(
    "retries only a failed tile for %i and releases its slot during backoff",
    async (status) => {
      let attempts = 0;
      const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async (url) => {
        if (String(url).endsWith("/0/1.pbf") && ++attempts < 3) return response(status);
        return response();
      });
      const failed = load();
      await flush();
      const other = await load(1);
      expect(other.data.byteLength).toBe(2);
      await vi.advanceTimersByTimeAsync(250);
      expect(attempts).toBe(2);
      await vi.advanceTimersByTimeAsync(750);
      await expect(failed).resolves.toHaveProperty("data");
      expect(fetch.mock.calls.filter(([url]) => String(url).endsWith("/1/1.pbf"))).toHaveLength(1);
    },
  );

  test("puts retries behind already queued work and caps Retry-After", async () => {
    const replies: Array<(value: Response) => void> = [];
    const fetch = vi
      .spyOn(globalThis, "fetch")
      .mockImplementation(() => new Promise((resolve) => replies.push(resolve)));
    const pending = Array.from({ length: 10 }, (_, i) => load(i));
    replies[0](response(503, null, { "Retry-After": "120" }));
    await flush();
    expect(fetch).toHaveBeenCalledTimes(5);
    await vi.advanceTimersByTimeAsync(1000);
    for (let i = 1; i < 11; i++) {
      replies[i](response());
      await flush();
    }
    await Promise.all(pending);
    expect(fetch.mock.calls[10][0]).toBe(fetch.mock.calls[0][0]);
  });

  test("caps attempts per tile but a later viewport tile has a fresh budget", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async () => response(503));
    const first = load();
    await vi.advanceTimersByTimeAsync(2000);
    await expect(first).rejects.toMatchObject({ status: 503 });
    expect(fetch).toHaveBeenCalledTimes(3);
    const next = load(1);
    await vi.advanceTimersByTimeAsync(2000);
    await expect(next).rejects.toMatchObject({ status: 503 });
    expect(fetch).toHaveBeenCalledTimes(6);
  });

  test("cancels backoff immediately without another attempt", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async () => response(502));
    const pending = load();
    await flush();
    controllers[0].abort();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    await vi.advanceTimersByTimeAsync(2000);
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  test.each([4000, 6000])(
    "forty tiles taking %ims each can wait over 15s before their first dispatch",
    async (duration) => {
      const submitted = Date.now();
      const starts: number[] = [];
      let active = 0,
        maximum = 0;
      const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(
        () =>
          new Promise((resolve) => {
            starts.push(Date.now() - submitted);
            active++;
            maximum = Math.max(maximum, active);
            setTimeout(() => {
              active--;
              resolve(response());
            }, duration);
          }),
      );
      const other = tiles.setSource("coverage", origin + coverage);
      const results = Promise.allSettled(
        Array.from({ length: 40 }, (_, i) => load(i, i % 2 ? other : template)),
      );
      await vi.advanceTimersByTimeAsync(duration * 10 + 100);
      expect((await results).every((result) => result.status === "fulfilled")).toBe(true);
      expect(starts.at(-1)).toBeGreaterThan(PRIVATE_TILE_SERVICE_BUDGET_MS);
      expect(fetch).toHaveBeenCalledTimes(40);
      expect(maximum).toBe(4);
      expect(active).toBe(0);
    },
  );

  test("never-admitted tiles have a distinct 60s queue deadline, not a 15s service deadline", async () => {
    const signals: AbortSignal[] = [];
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation((_url, init) => {
      signals.push(init!.signal as AbortSignal);
      return new Promise(() => {});
    });
    const pending = Array.from({ length: 5 }, (_, i) => load(i));
    let queuedSettled = false;
    void pending[4]
      .finally(() => {
        queuedSettled = true;
      })
      .catch(() => undefined);
    await vi.advanceTimersByTimeAsync(PRIVATE_TILE_SERVICE_BUDGET_MS);
    for (const result of pending.slice(0, 4))
      await expect(result).rejects.toMatchObject({
        status: 503,
        message: "Private tile service deadline exceeded",
      });
    expect(queuedSettled).toBe(false);
    expect(signals).toHaveLength(4);
    expect(signals.every((signal) => signal.aborted)).toBe(true);
    await vi.advanceTimersByTimeAsync(
      PRIVATE_TILE_QUEUE_BUDGET_MS - PRIVATE_TILE_SERVICE_BUDGET_MS - 1,
    );
    expect(queuedSettled).toBe(false);
    const follower = load(4);
    await vi.advanceTimersByTimeAsync(1);
    for (const result of [pending[4], follower])
      await expect(result).rejects.toMatchObject({
        status: 503,
        message: "Private tile queue deadline exceeded",
      });
    expect(fetch).toHaveBeenCalledTimes(4);
  });

  test("aborting a tile queued longer than 15s prevents HTTP and clears its queue deadline", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise(() => {}));
    const pending = Array.from({ length: 5 }, (_, i) => load(i));
    await vi.advanceTimersByTimeAsync(20_000);
    controllers[4].abort();
    await expect(pending[4]).rejects.toMatchObject({ name: "AbortError" });
    await vi.advanceTimersByTimeAsync(PRIVATE_TILE_QUEUE_BUDGET_MS);
    expect(fetch).toHaveBeenCalledTimes(4);
    expect(vi.getTimerCount()).toBe(0);
  });

  test("one service budget covers all attempts and backoff without resetting on retries", async () => {
    const submitted = Date.now();
    const starts: number[] = [];
    const signals: AbortSignal[] = [];
    vi.spyOn(globalThis, "fetch").mockImplementation(
      (_url, init) =>
        new Promise((resolve, reject) => {
          starts.push(Date.now() - submitted);
          const signal = init!.signal as AbortSignal;
          signals.push(signal);
          if (starts.length < 3) setTimeout(() => resolve(response(502)), 6000);
          else
            signal.addEventListener(
              "abort",
              () => reject(new DOMException("Aborted", "AbortError")),
              { once: true },
            );
        }),
    );
    const pending = load();
    await vi.advanceTimersByTimeAsync(PRIVATE_TILE_SERVICE_BUDGET_MS - 1);
    expect(starts).toEqual([0, 6250, 13000]);
    expect(signals[2].aborted).toBe(false);
    await vi.advanceTimersByTimeAsync(1);
    await expect(pending).rejects.toMatchObject({
      status: 503,
      message: "Private tile service deadline exceeded",
    });
    expect(signals[2].aborted).toBe(true);
  });

  test("the service budget continues while a retry waits behind other tiles", async () => {
    const fetch = vi
      .spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(response(503))
      .mockImplementation(() => new Promise(() => {}));
    const pending = Array.from({ length: 5 }, (_, i) => load(i));
    await vi.advanceTimersByTimeAsync(PRIVATE_TILE_SERVICE_BUDGET_MS - 1);
    expect(fetch.mock.calls.filter(([url]) => String(url).endsWith("/0/1.pbf"))).toHaveLength(1);
    await vi.advanceTimersByTimeAsync(1);
    await expect(pending[0]).rejects.toMatchObject({
      status: 503,
      message: "Private tile service deadline exceeded",
    });
    expect(fetch.mock.calls.filter(([url]) => String(url).endsWith("/0/1.pbf"))).toHaveLength(1);
  });

  test("service expiry aborts an in-progress HTTP body and releases its reader", async () => {
    let signal: AbortSignal | undefined;
    let body: ReadableStream;
    vi.spyOn(globalThis, "fetch").mockImplementation(async (_url, init) => {
      signal = init!.signal as AbortSignal;
      body = new ReadableStream({
        start(controller) {
          controller.enqueue(new Uint8Array([26, 0]));
          signal!.addEventListener(
            "abort",
            () => controller.error(new DOMException("Aborted", "AbortError")),
            { once: true },
          );
        },
      });
      return response(200, body);
    });
    const pending = load();
    await vi.advanceTimersByTimeAsync(PRIVATE_TILE_SERVICE_BUDGET_MS);
    await expect(pending).rejects.toMatchObject({
      status: 503,
      message: "Private tile service deadline exceeded",
    });
    await flush();
    expect(signal?.aborted).toBe(true);
    expect(body!.locked).toBe(false);
  });

  test.each([200, 401, 404])(
    "source replacement fences old messages and late %i results",
    async (status) => {
      let reply: (value: Response) => void = () => {};
      const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(
        () =>
          new Promise((resolve) => {
            reply = resolve;
          }),
      );
      const old = load();
      const updated = tiles.setSource("routes", origin + path, true);
      expect(updated).not.toBe(template);
      await expect(old).rejects.toMatchObject({ name: "AbortError" });
      reply(response(status));
      await flush();
      await expect(load(1)).rejects.toMatchObject({ name: "AbortError" });
      expect(fetch).toHaveBeenCalledTimes(1);
      expect(unavailable).not.toHaveBeenCalled();
      expect(unauthorized).not.toHaveBeenCalled();
    },
  );

  test("aborting a running fetch promptly starts the next needed tile", async () => {
    const signals: AbortSignal[] = [];
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(
      (_url, init) =>
        new Promise((_resolve, reject) => {
          const signal = init!.signal as AbortSignal;
          signals.push(signal);
          signal.addEventListener(
            "abort",
            () => reject(new DOMException("Aborted", "AbortError")),
            { once: true },
          );
        }),
    );
    const pending = Array.from({ length: 6 }, (_, i) => load(i));
    controllers[0].abort();
    await expect(pending[0]).rejects.toMatchObject({ name: "AbortError" });
    await flush();
    expect(signals[0].aborted).toBe(true);
    expect(fetch).toHaveBeenCalledTimes(5);
    expect(signals.filter((signal) => !signal.aborted)).toHaveLength(4);
  });

  test("lets queued worker cancellation messages drain before refilling an aborted slot", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(
      (_url, init) =>
        new Promise((_resolve, reject) => {
          init!.signal!.addEventListener(
            "abort",
            () => reject(new DOMException("Aborted", "AbortError")),
            { once: true },
          );
        }),
    );
    const pending = Array.from({ length: 12 }, (_, i) => load(i));
    controllers[0].abort();
    setTimeout(() => {
      for (const controller of controllers.slice(4)) controller.abort();
    }, 0);
    await vi.advanceTimersByTimeAsync(0);
    for (const result of [pending[0], ...pending.slice(4)])
      await expect(result).rejects.toMatchObject({ name: "AbortError" });
    expect(fetch).toHaveBeenCalledTimes(4);
  });

  test("bounds coalesced followers without allowing a follower to extend the deadline", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(() => new Promise(() => {}));
    const pending = [load()];
    await vi.advanceTimersByTimeAsync(PRIVATE_TILE_SERVICE_BUDGET_MS - 1000);
    pending.push(...Array.from({ length: 63 }, () => load()));
    await expect(load()).rejects.toMatchObject({ status: 503 });
    expect(fetch).toHaveBeenCalledTimes(1);
    await vi.advanceTimersByTimeAsync(1000);
    for (const result of pending) await expect(result).rejects.toMatchObject({ status: 503 });
  });

  test("retries a transport failure without turning it into an empty success", async () => {
    const fetch = vi
      .spyOn(globalThis, "fetch")
      .mockRejectedValueOnce(new TypeError("Network failure"))
      .mockResolvedValueOnce(response());
    const pending = load();
    await vi.advanceTimersByTimeAsync(250);
    expect((await pending).data.byteLength).toBe(2);
    expect(fetch).toHaveBeenCalledTimes(2);
  });

  test("reset revokes source URLs while keeping the map protocol registration", async () => {
    tiles.reset();
    await expect(load()).rejects.toMatchObject({ name: "AbortError" });
    expect(protocols.has(tiles.protocol)).toBe(true);
    expect(tiles.setSource("routes", origin + path)).not.toBe(template);
  });

  test("style cancellation retains logical identity but drops every pending job", async () => {
    vi.spyOn(globalThis, "fetch").mockImplementation(async () => response(503));
    const pending = load();
    await flush();
    tiles.cancel();
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    expect(tiles.setSource("routes", origin + path)).toBe(template);
    expect(vi.getTimerCount()).toBe(0);
  });
});

describe("private protocol boundary", () => {
  test.each([401, 404])(
    "invalidates %i once across both sources before rejecting, never as empty",
    async (status) => {
      const fetch = vi.spyOn(globalThis, "fetch").mockImplementation(async () => response(status));
      const other = tiles.setSource("coverage", origin + coverage);
      const results = [load(), load(1, other)];
      await expect(results[0]).rejects.toMatchObject({ status: status === 404 ? 410 : status });
      await expect(results[1]).rejects.toMatchObject({ name: "AbortError" });
      await expect(load(2, other)).rejects.toMatchObject({ status: status === 404 ? 410 : status });
      expect(status === 401 ? unauthorized : unavailable).toHaveBeenCalledTimes(1);
      expect(status === 401 ? unavailable : unauthorized).not.toHaveBeenCalled();
      expect(fetch).toHaveBeenCalledTimes(2);
      const newPath = path.replace("A".repeat(32), "B".repeat(32));
      template = tiles.setSource("routes", origin + newPath);
      await expect(load()).rejects.toMatchObject({ status: status === 404 ? 410 : status });
      expect(status === 401 ? unauthorized : unavailable).toHaveBeenCalledTimes(2);
    },
  );

  test.each([200, 204])("accepts genuinely empty %i responses", async (status) => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(response(status, null));
    await expect(load()).resolves.toMatchObject({ data: new ArrayBuffer(0) });
    expect(unavailable).not.toHaveBeenCalled();
  });

  test.each([400, 403, 500])("does not retry nontransient HTTP %i", async (status) => {
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(response(status));
    await expect(load()).rejects.toMatchObject({ status });
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  test("bounds streamed bytes even without Content-Length", async () => {
    const cancel = vi.fn();
    const body = new ReadableStream({
      start(controller) {
        controller.enqueue(new Uint8Array(PRIVATE_TILE_MAX_BYTES));
        controller.enqueue(new Uint8Array(1));
      },
      cancel,
    });
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(response(200, body));
    await expect(load()).rejects.toMatchObject({ status: 413 });
    expect(cancel).toHaveBeenCalledTimes(1);
    expect(fetch).toHaveBeenCalledTimes(1);
  });

  test("accepts the exact size limit and rejects oversized advertised bodies", async () => {
    vi.spyOn(globalThis, "fetch")
      .mockResolvedValueOnce(response(200, new Uint8Array(PRIVATE_TILE_MAX_BYTES)))
      .mockResolvedValueOnce(
        response(200, null, { "Content-Length": String(PRIVATE_TILE_MAX_BYTES + 1) }),
      );
    expect((await load()).data.byteLength).toBe(PRIVATE_TILE_MAX_BYTES);
    await expect(load(1)).rejects.toMatchObject({ status: 413 });
  });

  test("rejects non-MVT success bodies rather than caching or returning empty", async () => {
    vi.spyOn(globalThis, "fetch").mockResolvedValue(
      response(200, "<html>", { "Content-Type": "text/html" }),
    );
    await expect(load()).rejects.toMatchObject({ status: 422 });
  });

  test.each([
    "https://elsewhere.test" + path,
    "https://user:secret@workouts.test" + path,
    origin + path + "?token=secret",
    origin + path + "#fragment",
    origin + path.replace("route-tiles", "route-points"),
    origin + path.replace("map-selections", "other/../map-selections"),
    origin + path.replace("A".repeat(32), "%41".repeat(32)),
  ])("rejects unapproved templates without HTTP: %s", (url) => {
    const fetch = vi.spyOn(globalThis, "fetch");
    expect(() => tiles.setSource("other", url)).toThrow("Invalid private tile template");
    expect(fetch).not.toHaveBeenCalled();
  });

  test("ignores incoming headers/credentials and preserves the exact approved HTTP path", async () => {
    const fetch = vi.spyOn(globalThis, "fetch").mockResolvedValue(response());
    const logical = origin + path.replace("A".repeat(32), "aAbB".repeat(8));
    template = tiles.setSource("routes", logical);
    const request = {
      url: template.replace("{z}/{x}/{y}", "10/0/1"),
      headers: { Authorization: "secret" },
      credentials: "include",
    };
    await tiles.load(request, new AbortController());
    expect(fetch).toHaveBeenCalledWith(logical.replace("{z}/{x}/{y}", "10/0/1"), {
      credentials: "same-origin",
      mode: "same-origin",
      cache: "no-store",
      redirect: "error",
      signal: expect.any(AbortSignal),
    });
  });

  test("rejects crafted protocol paths and out-of-range coordinates", async () => {
    const fetch = vi.spyOn(globalThis, "fetch");
    for (const suffix of [
      "10/1/1.pbf?host=elsewhere",
      "10/../1.pbf",
      "10/%31/1.pbf",
      "10/01/1.pbf",
    ])
      await expect(
        tiles.load({ url: template.split("{z}")[0] + suffix }, new AbortController()),
      ).rejects.toMatchObject({ name: "AbortError" });
    await expect(
      tiles.load({ url: template.replace("{z}/{x}/{y}", "1/2/0") }, new AbortController()),
    ).rejects.toMatchObject({ status: 400 });
    expect(fetch).not.toHaveBeenCalled();
  });

  test("maps have independent registrations and closing rejects stale worker messages", async () => {
    const second = new PrivateTiles(origin, { unauthorized, unavailable });
    expect(protocols.size).toBe(2);
    tiles.close();
    await expect(load()).rejects.toMatchObject({ name: "AbortError" });
    expect(protocols.has(second.protocol)).toBe(true);
    second.close();
  });
});
